package upgrade

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/install"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type ActivateResult struct {
	Status                 string `json:"status"`
	CurrentVersion         string `json:"current_version"`
	PreviousVersion        string `json:"previous_version,omitempty"`
	SnapshotID             string `json:"snapshot_id,omitempty"`
	SnapshotManifestSHA256 string `json:"snapshot_manifest_sha256,omitempty"`
	Rollback               string `json:"rollback,omitempty"`
}

func Activate(
	ctx context.Context,
	planFile, expectedPlanSHA256 string,
	hooks ...ActivationHooks,
) (returned ActivateResult, returnedErr error) {
	plan, err := readAuthenticatedPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return ActivateResult{}, errors.Join(err, abortPendingFromPlanPath(
			planFile, "plan.json", phaseActivationPending, "ACTIVATION_PENDING", errorCodeOf(err),
		))
	}
	if err := claimOperation(plan.RuntimeRoot, plan.ID, phaseActivationPending); err != nil {
		return ActivateResult{}, err
	}
	defer func() {
		returnedErr = errors.Join(returnedErr, releaseTerminalOperation(plan.RuntimeRoot, plan.ID, returnedErr))
	}()
	verifiedPlan, err := verifyPreparedPlan(ctx, plan)
	if err != nil {
		return ActivateResult{}, writeFailureState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		}, err)
	}
	plan = verifiedPlan
	if !waitForHealthState(ctx, plan.Port, "", false, 15*time.Second) {
		err := failure("DASHBOARD_STILL_RUNNING", nil)
		return ActivateResult{}, writeFailureState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		}, err)
	}
	currentSchema, err := databaseSchemaVersion(filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3"))
	if err != nil || currentSchema > plan.TargetMaxSchema || currentSchema > plan.PreviousMaxSchema {
		if err == nil {
			err = failure("UPGRADE_SCHEMA_INCOMPATIBLE", nil)
		} else {
			err = failure("KNOWLEDGE_SCHEMA_READ_FAILED", err)
		}
		return ActivateResult{}, writeFailureState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		}, err)
	}
	plan.DatabaseSchema = currentSchema
	if err := transitionOperation(plan.RuntimeRoot, plan.ID, phaseActivationPending, phaseActivating); err != nil {
		return ActivateResult{}, err
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion,
	}); err != nil {
		return ActivateResult{}, stateWriteFailure(err)
	}
	if _, err := runtimeentry.BootstrapLegacy(plan.RuntimeRoot, plan.FromVersion); err != nil {
		cause := failure("RUNTIME_GENERATION_BOOTSTRAP_FAILED", err)
		return ActivateResult{}, writeFailureState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: "RUNTIME_GENERATION_BOOTSTRAP_FAILED",
		}, cause)
	}
	snapshot, err := activationSnapshot(ctx, plan)
	if err != nil {
		cause := failure("UPGRADE_SNAPSHOT_FAILED", err)
		return ActivateResult{}, writeFailureState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: "UPGRADE_SNAPSHOT_FAILED",
		}, cause)
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
		SnapshotManifestSHA256: snapshot.ManifestSHA256,
	}); err != nil {
		return ActivateResult{}, stateWriteFailure(err)
	}
	if err := activatePrepared(ctx, plan); err != nil {
		return automaticRollback(ctx, plan, snapshot, err, hooks)
	}
	if plan.RestartDashboard {
		configuration := dashboardActivation(plan, plan.ToVersion)
		if err := configureDashboard(ctx, hooks, configuration); err != nil {
			return automaticRollback(ctx, plan, snapshot, err, hooks)
		}
	}
	state := State{
		Status: "ACTIVE", OperationID: plan.ID, CurrentVersion: plan.ToVersion,
		PreviousVersion: plan.FromVersion, TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
		SnapshotManifestSHA256: snapshot.ManifestSHA256,
	}
	if err := writeState(plan.RuntimeRoot, state); err != nil {
		return automaticRollback(ctx, plan, snapshot, stateWriteFailure(err), hooks)
	}
	if err := pruneSnapshots(plan.RuntimeRoot, snapshot.ID); err != nil {
		return ActivateResult{
			Status: "ACTIVE", CurrentVersion: plan.ToVersion, PreviousVersion: plan.FromVersion,
			SnapshotID: snapshot.ID, SnapshotManifestSHA256: snapshot.ManifestSHA256,
		}, nil
	}
	return ActivateResult{
		Status: "ACTIVE", CurrentVersion: plan.ToVersion, PreviousVersion: plan.FromVersion,
		SnapshotID: snapshot.ID, SnapshotManifestSHA256: snapshot.ManifestSHA256,
	}, nil
}

func activatePrepared(ctx context.Context, plan Plan) error {
	target, err := runtimeentry.MaterializeGeneration(
		plan.RuntimeRoot, plan.ID, plan.ToVersion, plan.BinaryPath, plan.BinarySHA256,
	)
	if err != nil {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	if err := runtimeentry.InstallLauncher(plan.RuntimeRoot, plan.BinaryPath, plan.BinarySHA256); err != nil {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	if _, err := install.MaterializePlugins(plan.CodexRoot, plan.SourceRoot, plan.BinaryPath); err != nil {
		return failure("PLUGIN_ACTIVATION_FAILED", err)
	}
	if err := runtimeentry.Activate(plan.RuntimeRoot, target); err != nil {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	active, targetBinary, err := runtimeentry.ReadActive(plan.RuntimeRoot)
	if err != nil || active.Generation != plan.ID || active.Version != plan.ToVersion ||
		active.BinarySHA256 != plan.BinarySHA256 {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	hash, err := safeio.FileSHA256(targetBinary)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(plan.BinarySHA256)) != 1 {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	version, schema, err := inspectReleaseBinary(ctx, targetBinary)
	if err != nil || version != plan.ToVersion || schema != plan.TargetMaxSchema {
		return failure("RUNTIME_ACTIVATION_FAILED", err)
	}
	return validateInstalledPlugins(plan)
}

func validateInstalledPlugins(plan Plan) error {
	pluginsRoot := filepath.Join(plan.CodexRoot, "plugins")
	var manifest struct {
		Schema  string `json:"schema"`
		Version string `json:"version"`
		Plugins []struct {
			Name       string `json:"name"`
			TreeSHA256 string `json:"tree_sha256"`
		} `json:"plugins"`
	}
	if err := safeio.ReadJSON(filepath.Join(pluginsRoot, ".ytqjk-managed-plugins.json"), &manifest); err != nil {
		return failure("PLUGIN_ACTIVATION_FAILED", err)
	}
	if manifest.Schema != "ytqjk-managed-plugins/v1" || manifest.Version != plan.ToVersion || len(manifest.Plugins) != len(pluginNames) {
		return failure("PLUGIN_ACTIVATION_FAILED", nil)
	}
	sort.Slice(manifest.Plugins, func(i, j int) bool { return manifest.Plugins[i].Name < manifest.Plugins[j].Name })
	expectedNames := append([]string(nil), pluginNames...)
	sort.Strings(expectedNames)
	for index, entry := range manifest.Plugins {
		if entry.Name != expectedNames[index] || !hexDigestPattern.MatchString(entry.TreeSHA256) {
			return failure("PLUGIN_ACTIVATION_FAILED", nil)
		}
		root := filepath.Join(pluginsRoot, entry.Name)
		hash, err := safeio.TreeHash(root)
		if err != nil || hash != entry.TreeSHA256 {
			return failure("PLUGIN_ACTIVATION_FAILED", err)
		}
		binaryHash, err := safeio.FileSHA256(filepath.Join(root, "bin", runtimeBinaryName()))
		if err != nil || binaryHash != plan.BinarySHA256 {
			return failure("PLUGIN_ACTIVATION_FAILED", err)
		}
		data, err := os.ReadFile(filepath.Join(root, ".codex-plugin", "plugin.json"))
		if err != nil {
			return failure("PLUGIN_ACTIVATION_FAILED", err)
		}
		var plugin struct{ Name, Version string }
		if json.Unmarshal(data, &plugin) != nil || plugin.Name != entry.Name || plugin.Version != plan.ToVersion {
			return failure("PLUGIN_ACTIVATION_FAILED", nil)
		}
	}
	return nil
}

func automaticRollback(
	ctx context.Context,
	plan Plan,
	snapshot Snapshot,
	cause error,
	hooks []ActivationHooks,
) (ActivateResult, error) {
	if err := restoreSnapshot(plan, snapshot, true); err != nil {
		cause = failure("UPGRADE_ROLLBACK_FAILED", errors.Join(cause, err))
		result := ActivateResult{
			Status: "ROLLBACK_FAILED", CurrentVersion: plan.FromVersion,
			PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID,
			SnapshotManifestSHA256: snapshot.ManifestSHA256, Rollback: "FAILED",
		}
		return result, writeFailureState(plan.RuntimeRoot, State{
			Status: "ROLLBACK_FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
			SnapshotManifestSHA256: snapshot.ManifestSHA256, ErrorCode: "UPGRADE_ROLLBACK_FAILED",
		}, cause)
	}
	configurationErr := error(nil)
	if plan.RestartDashboard && snapshot.RuntimeBinary {
		configurationErr = configureDashboard(ctx, hooks, dashboardActivation(plan, plan.FromVersion))
	}
	if configurationErr != nil {
		cause = failure("UPGRADE_ROLLBACK_HEALTH_FAILED", errors.Join(cause, configurationErr))
		result := ActivateResult{
			Status: "ROLLBACK_FAILED", CurrentVersion: plan.FromVersion,
			PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID,
			SnapshotManifestSHA256: snapshot.ManifestSHA256, Rollback: "FAILED",
		}
		return result, writeFailureState(plan.RuntimeRoot, State{
			Status: "ROLLBACK_FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
			SnapshotManifestSHA256: snapshot.ManifestSHA256, ErrorCode: "UPGRADE_ROLLBACK_HEALTH_FAILED",
		}, cause)
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ROLLED_BACK", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		PreviousVersion: plan.ToVersion, TargetVersion: plan.ToVersion,
		SnapshotID: snapshot.ID, SnapshotManifestSHA256: snapshot.ManifestSHA256,
		ErrorCode: errorCodeOf(cause),
	}); err != nil {
		return rollbackStateWriteResult(plan, snapshot, cause, err)
	}
	return ActivateResult{
		Status: "ROLLED_BACK", CurrentVersion: plan.FromVersion,
		PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID,
		SnapshotManifestSHA256: snapshot.ManifestSHA256, Rollback: "SUCCEEDED",
	}, failure("UPGRADE_ACTIVATION_ROLLED_BACK", cause)
}

func dashboardActivation(plan Plan, version string) DashboardActivation {
	return DashboardActivation{
		RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot,
		KnowledgeRoot: plan.KnowledgeRoot, Version: version, Port: plan.Port,
	}
}

func rollbackStateWriteResult(
	plan Plan,
	snapshot Snapshot,
	cause error,
	writeErr error,
) (ActivateResult, error) {
	return ActivateResult{
			Status: "ROLLED_BACK", CurrentVersion: plan.FromVersion,
			PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID,
			SnapshotManifestSHA256: snapshot.ManifestSHA256, Rollback: "UNKNOWN",
		}, errors.Join(
			stateWriteFailure(writeErr),
			failure("UPGRADE_ACTIVATION_ROLLED_BACK", cause),
		)
}

func waitForHealthState(ctx context.Context, port int, version string, running bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		current := healthVersion(port)
		if running && current == version {
			return true
		}
		if !running && current == "" {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(150 * time.Millisecond):
		}
	}
}

func healthVersion(port int) string {
	dialer := &net.Dialer{Timeout: 400 * time.Millisecond}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, DisableKeepAlives: true,
		MaxIdleConns: 0, TLSHandshakeTimeout: 400 * time.Millisecond,
	}
	client := &http.Client{Transport: transport, Timeout: 800 * time.Millisecond}
	request, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/health", port), nil)
	request.Host = fmt.Sprintf("127.0.0.1:%d", port)
	response, err := client.Do(request)
	if err != nil {
		return ""
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength > 64*1024 {
		return ""
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return ""
	}
	var value struct {
		OK      bool   `json:"ok"`
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if json.Unmarshal(body, &value) != nil || !value.OK || value.Status != "RUNNING" || !semverPattern.MatchString(value.Version) {
		return ""
	}
	return strings.TrimSpace(value.Version)
}

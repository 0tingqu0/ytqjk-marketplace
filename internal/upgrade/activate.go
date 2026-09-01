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
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/install"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type ActivateResult struct {
	Status          string `json:"status"`
	CurrentVersion  string `json:"current_version"`
	PreviousVersion string `json:"previous_version,omitempty"`
	SnapshotID      string `json:"snapshot_id,omitempty"`
	Rollback        string `json:"rollback,omitempty"`
}

func Activate(ctx context.Context, planFile string) (ActivateResult, error) {
	plan, err := readPlan(planFile)
	if err != nil {
		return ActivateResult{}, err
	}
	if err := verifyPreparedPlan(ctx, plan); err != nil {
		_ = writeState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		})
		return ActivateResult{}, err
	}
	if !waitForHealthState(ctx, plan.Port, "", false, 15*time.Second) {
		err := failure("DASHBOARD_STILL_RUNNING", nil)
		_ = writeState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		})
		return ActivateResult{}, err
	}
	currentSchema, err := databaseSchemaVersion(filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3"))
	if err != nil || currentSchema > plan.TargetMaxSchema || currentSchema > plan.PreviousMaxSchema {
		if err == nil {
			err = failure("UPGRADE_SCHEMA_INCOMPATIBLE", nil)
		} else {
			err = failure("KNOWLEDGE_SCHEMA_READ_FAILED", err)
		}
		_ = writeState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: errorCodeOf(err),
		})
		return ActivateResult{}, err
	}
	plan.DatabaseSchema = currentSchema
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion,
	}); err != nil {
		return ActivateResult{}, failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	snapshot, err := captureSnapshot(ctx, plan)
	if err != nil {
		_ = writeState(plan.RuntimeRoot, State{
			Status: "FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: "UPGRADE_SNAPSHOT_FAILED",
		})
		return ActivateResult{}, failure("UPGRADE_SNAPSHOT_FAILED", err)
	}
	_ = writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
	})
	if err := activatePrepared(ctx, plan); err != nil {
		return automaticRollback(ctx, plan, snapshot, err)
	}
	if plan.RestartDashboard {
		if err := startDashboard(ctx, plan, plan.ToVersion); err != nil {
			return automaticRollback(ctx, plan, snapshot, err)
		}
	}
	state := State{
		Status: "ACTIVE", OperationID: plan.ID, CurrentVersion: plan.ToVersion,
		PreviousVersion: plan.FromVersion, TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
	}
	if err := writeState(plan.RuntimeRoot, state); err != nil {
		return automaticRollback(ctx, plan, snapshot, failure("UPGRADE_STATE_WRITE_FAILED", err))
	}
	if err := pruneSnapshots(plan.RuntimeRoot, snapshot.ID); err != nil {
		return ActivateResult{
			Status: "ACTIVE", CurrentVersion: plan.ToVersion, PreviousVersion: plan.FromVersion,
			SnapshotID: snapshot.ID,
		}, nil
	}
	return ActivateResult{
		Status: "ACTIVE", CurrentVersion: plan.ToVersion, PreviousVersion: plan.FromVersion,
		SnapshotID: snapshot.ID,
	}, nil
}

func verifyPreparedPlan(ctx context.Context, plan Plan) error {
	state := Status(plan.RuntimeRoot, plan.FromVersion)
	if state.OperationID != plan.ID || (state.Status != "PREPARED" && state.Status != "ACTIVATION_PENDING") {
		return failure("UPGRADE_STATE_CONFLICT", nil)
	}
	hash, err := safeio.FileSHA256(plan.BinaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(plan.BinarySHA256)) != 1 {
		return failure("RELEASE_BINARY_INVALID", err)
	}
	version, schema, err := inspectReleaseBinary(ctx, plan.BinaryPath)
	if err != nil || version != plan.ToVersion || schema != plan.TargetMaxSchema {
		return failure("RELEASE_BINARY_INVALID", err)
	}
	return validateSource(plan.SourceRoot, plan.ToVersion)
}

func activatePrepared(ctx context.Context, plan Plan) error {
	if _, err := install.MaterializePlugins(plan.CodexRoot, plan.SourceRoot, plan.BinaryPath); err != nil {
		return failure("PLUGIN_ACTIVATION_FAILED", err)
	}
	targetBinary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	if err := transactionalRestore(plan.ID, []restoreItem{{
		Target: targetBinary, Source: plan.BinaryPath, Present: true,
	}}); err != nil {
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

func automaticRollback(ctx context.Context, plan Plan, snapshot Snapshot, cause error) (ActivateResult, error) {
	_ = stopDashboard(ctx, plan.BinaryPath, plan.KnowledgeRoot, plan.Port)
	if err := restoreSnapshot(plan, snapshot, true); err != nil {
		_ = writeState(plan.RuntimeRoot, State{
			Status: "ROLLBACK_FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID, ErrorCode: "UPGRADE_ROLLBACK_FAILED",
		})
		return ActivateResult{
			Status: "ROLLBACK_FAILED", CurrentVersion: plan.FromVersion,
			PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID, Rollback: "FAILED",
		}, failure("UPGRADE_ROLLBACK_FAILED", errors.Join(cause, err))
	}
	restartErr := error(nil)
	if plan.RestartDashboard && snapshot.RuntimeBinary {
		restartErr = startDashboard(ctx, Plan{
			RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot, KnowledgeRoot: plan.KnowledgeRoot,
			Port: plan.Port, RestartDashboard: true,
		}, plan.FromVersion)
	}
	if restartErr != nil {
		_ = writeState(plan.RuntimeRoot, State{
			Status: "ROLLBACK_FAILED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID, ErrorCode: "UPGRADE_ROLLBACK_HEALTH_FAILED",
		})
		return ActivateResult{
			Status: "ROLLBACK_FAILED", CurrentVersion: plan.FromVersion,
			PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID, Rollback: "FAILED",
		}, failure("UPGRADE_ROLLBACK_HEALTH_FAILED", errors.Join(cause, restartErr))
	}
	_ = writeState(plan.RuntimeRoot, State{
		Status: "ROLLED_BACK", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		PreviousVersion: plan.ToVersion, TargetVersion: plan.ToVersion,
		SnapshotID: snapshot.ID, ErrorCode: errorCodeOf(cause),
	})
	return ActivateResult{
		Status: "ROLLED_BACK", CurrentVersion: plan.FromVersion,
		PreviousVersion: plan.ToVersion, SnapshotID: snapshot.ID, Rollback: "SUCCEEDED",
	}, failure("UPGRADE_ACTIVATION_ROLLED_BACK", cause)
}

func startDashboard(ctx context.Context, plan Plan, expectedVersion string) error {
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	assets := filepath.Join(plan.CodexRoot, "plugins", "ytqjk-agentic-orchestrator", "skills", "ytqjk", "dashboard")
	if _, err := runLifecycle(ctx, binary, "dashboard", "start", "--knowledge-root", plan.KnowledgeRoot, "--assets", assets, "--port", strconv.Itoa(plan.Port)); err != nil {
		return failure("UPGRADE_HEALTH_FAILED", err)
	}
	if !waitForHealthState(ctx, plan.Port, expectedVersion, true, 15*time.Second) {
		return failure("UPGRADE_HEALTH_FAILED", nil)
	}
	return nil
}

func stopDashboard(ctx context.Context, binary, knowledgeRoot string, port int) error {
	_, err := runLifecycle(ctx, binary, "dashboard", "stop", "--knowledge-root", knowledgeRoot, "--port", strconv.Itoa(port))
	if err != nil && healthVersion(port) != "" {
		return err
	}
	if !waitForHealthState(ctx, port, "", false, 10*time.Second) {
		return errors.New("dashboard did not stop")
	}
	return nil
}

func runLifecycle(parent context.Context, binary string, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdin = nil
	output, err := command.CombinedOutput()
	if err != nil {
		return "", err
	}
	if len(output) > 64*1024 {
		return "", errors.New("lifecycle output is too large")
	}
	return string(output), nil
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

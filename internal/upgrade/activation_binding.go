package upgrade

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

// ActivationBinding is the immutable subset needed to authenticate a
// maintenance canary before any upgrade or rollback mutation begins.
type ActivationBinding struct {
	OperationID            string
	RuntimeRoot            string
	CodexRoot              string
	KnowledgeRoot          string
	PlanSHA256             string
	SnapshotManifestSHA256 string
	TargetBinarySHA256     string
	TargetVersion          string
	Port                   int
	RestartDashboard       bool
}

func ReadActivationBinding(planFile, expectedPlanSHA256 string) (ActivationBinding, error) {
	plan, err := readAuthenticatedPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return ActivationBinding{}, err
	}
	if plan.SnapshotID == "" || plan.SnapshotManifestSHA256 == "" {
		return ActivationBinding{}, failure("UPGRADE_PLAN_INVALID", errors.New("activation snapshot is not bound"))
	}
	return ActivationBinding{
		OperationID: plan.ID, RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot,
		KnowledgeRoot: plan.KnowledgeRoot, PlanSHA256: expectedPlanSHA256,
		SnapshotManifestSHA256: plan.SnapshotManifestSHA256,
		TargetBinarySHA256:     plan.BinarySHA256, TargetVersion: plan.ToVersion,
		Port: plan.Port, RestartDashboard: plan.RestartDashboard,
	}, nil
}

func ReadRollbackBinding(planFile, expectedPlanSHA256 string) (ActivationBinding, error) {
	plan, err := readAuthenticatedRollbackPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return ActivationBinding{}, err
	}
	return ActivationBinding{
		OperationID: plan.ID, RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot,
		KnowledgeRoot: plan.KnowledgeRoot, PlanSHA256: expectedPlanSHA256,
		SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		TargetBinarySHA256:     plan.TargetBinarySHA256, TargetVersion: plan.TargetVersion,
		Port: plan.Port, RestartDashboard: plan.RestartDashboard,
	}, nil
}

func ReadActivationStateEvidence(runtimeRoot, operationID string) (State, string, error) {
	state, err := readOperationState(runtimeRoot)
	if err != nil || state.OperationID != operationID || !safeTerminalStatus(state.Status) {
		return State{}, "", failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	operation, exists, err := readOperationRecord(runtimeRoot)
	if err != nil || !exists || operation.Active || operation.Phase != phaseReleased ||
		operation.OperationID != operationID {
		return State{}, "", failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	path := filepath.Join(runtimeRoot, "upgrade", "state.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return State{}, "", failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	digest, err := safeio.FileSHA256(path)
	if err != nil {
		return State{}, "", failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return state, digest, nil
}

func snapshotRuntimeBinarySHA256(snapshot Snapshot) (string, error) {
	key := snapshotItemKeyFrom(snapshotRootRuntime, filepathRuntimeBinary())
	for _, item := range snapshot.Items {
		if snapshotItemKey(item) == key && item.Present && item.Kind == snapshotKindFile &&
			hexDigestPattern.MatchString(item.SHA256) {
			return item.SHA256, nil
		}
	}
	return "", failure("UPGRADE_SNAPSHOT_INVALID", errors.New("snapshot runtime binary is unavailable"))
}

func filepathRuntimeBinary() string {
	return "bin/" + runtimeBinaryName()
}

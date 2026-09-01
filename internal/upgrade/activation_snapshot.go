package upgrade

import (
	"context"
	"errors"

	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

// BindActivationSnapshot captures the rollback generation while the parent
// owns exclusive maintenance admission, then persists its immutable identity
// into the authenticated plan before child-process handoff.
func BindActivationSnapshot(ctx context.Context, plan Plan) (Plan, error) {
	path := planPath(plan)
	if ctx == nil {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", errors.New("context is required"))
	}
	if err := validatePlan(plan, path); err != nil {
		return Plan{}, err
	}
	if plan.SnapshotID != "" {
		snapshot, err := readSnapshot(plan.RuntimeRoot, plan.SnapshotID)
		if err != nil || snapshot.ManifestSHA256 != plan.SnapshotManifestSHA256 {
			return Plan{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
		}
		return plan, nil
	}
	if _, err := runtimeentry.BootstrapLegacy(plan.RuntimeRoot, plan.FromVersion); err != nil {
		return Plan{}, failure("RUNTIME_GENERATION_BOOTSTRAP_FAILED", err)
	}
	snapshot, err := captureSnapshot(ctx, plan)
	if err != nil {
		return Plan{}, failure("UPGRADE_SNAPSHOT_FAILED", err)
	}
	plan.SnapshotID = snapshot.ID
	plan.SnapshotManifestSHA256 = snapshot.ManifestSHA256
	if err := safeio.WriteJSON(path, plan); err != nil {
		return Plan{}, planWriteFailure(err)
	}
	if err := validatePlan(plan, path); err != nil {
		return Plan{}, err
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "PREPARED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion, SnapshotID: snapshot.ID,
		SnapshotManifestSHA256: snapshot.ManifestSHA256,
	}); err != nil {
		return Plan{}, stateWriteFailure(err)
	}
	return plan, nil
}

func activationSnapshot(ctx context.Context, plan Plan) (Snapshot, error) {
	if plan.SnapshotID == "" {
		return captureSnapshot(ctx, plan)
	}
	snapshot, err := readSnapshot(plan.RuntimeRoot, plan.SnapshotID)
	if err != nil || snapshot.ManifestSHA256 != plan.SnapshotManifestSHA256 {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	return snapshot, nil
}

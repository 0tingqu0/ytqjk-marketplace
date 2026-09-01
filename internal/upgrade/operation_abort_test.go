package upgrade

import (
	"errors"
	"os"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestAbortPendingActivationFallsBackToBoundPath(t *testing.T) {
	plan, digest := pendingActivationFixture(t)
	if err := os.WriteFile(planPath(plan), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := AbortPendingActivation(planPath(plan), digest, "UPGRADE_PLAN_INVALID")
	assertOperationCode(t, err, "UPGRADE_PLAN_INVALID")
	state, stateErr := readOperationState(plan.RuntimeRoot)
	if stateErr != nil || state.Status != "FAILED" || state.OperationID != plan.ID {
		t.Fatalf("state = %#v, %v", state, stateErr)
	}
	record, _, recordErr := readOperationRecord(plan.RuntimeRoot)
	if recordErr != nil || record.Active || record.Phase != phaseReleased {
		t.Fatalf("record = %#v, %v", record, recordErr)
	}
}

func TestAbortPendingActivationPreservesLockOnStateDurabilityUnknown(t *testing.T) {
	plan, digest := pendingActivationFixture(t)
	originalWriter := writeAbortState
	writeAbortState = func(runtimeRoot string, state State) error {
		if err := writeState(runtimeRoot, state); err != nil {
			return err
		}
		return &safeio.PostCommitError{Operation: "test", Err: errors.New("sync failed")}
	}
	t.Cleanup(func() { writeAbortState = originalWriter })
	err := AbortPendingActivation(planPath(plan), digest, "UPGRADE_PARENT_STILL_RUNNING")
	assertOperationCode(t, err, "UPGRADE_STATE_DURABILITY_UNKNOWN")
	record, _, recordErr := readOperationRecord(plan.RuntimeRoot)
	if recordErr != nil || !record.Active || record.Phase != phaseActivationPending {
		t.Fatalf("record = %#v, %v", record, recordErr)
	}
}

func pendingActivationFixture(t *testing.T) (Plan, string) {
	t.Helper()
	plan := operationUpgradePlan(operationTempDir(t))
	if err := os.MkdirAll(plan.SourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		t.Fatal(err)
	}
	if err := acquireOperation(plan.RuntimeRoot, plan.ID, phaseActivationPending); err != nil {
		t.Fatal(err)
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATION_PENDING", OperationID: plan.ID,
		CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
	}); err != nil {
		t.Fatal(err)
	}
	digest, err := safeio.FileSHA256(planPath(plan))
	if err != nil {
		t.Fatal(err)
	}
	return plan, digest
}

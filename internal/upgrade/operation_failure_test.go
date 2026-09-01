package upgrade

import (
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestOperationStopStartedHelperReturnsKillFailureWithoutWaiting(t *testing.T) {
	originalKill, originalWait, originalTimeout := killStartedHelper, waitStartedHelper, helperStopWait
	t.Cleanup(func() {
		killStartedHelper, waitStartedHelper, helperStopWait = originalKill, originalWait, originalTimeout
	})
	killErr := errors.New("kill blocked")
	waitCalled := false
	killStartedHelper = func(*exec.Cmd) error { return killErr }
	waitStartedHelper = func(*exec.Cmd) error {
		waitCalled = true
		return nil
	}
	if err := stopStartedHelper(&exec.Cmd{}); !errors.Is(err, killErr) || waitCalled {
		t.Fatalf("stop error = %v, wait_called=%v", err, waitCalled)
	}
}

func TestOperationStopStartedHelperWaitIsBounded(t *testing.T) {
	originalKill, originalWait, originalTimeout := killStartedHelper, waitStartedHelper, helperStopWait
	t.Cleanup(func() {
		killStartedHelper, waitStartedHelper, helperStopWait = originalKill, originalWait, originalTimeout
	})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	killStartedHelper = func(*exec.Cmd) error { return nil }
	waitStartedHelper = func(*exec.Cmd) error {
		<-release
		return nil
	}
	helperStopWait = 10 * time.Millisecond
	started := time.Now()
	err := stopStartedHelper(&exec.Cmd{})
	if err == nil || time.Since(started) > time.Second {
		t.Fatalf("stop error = %v, elapsed=%s", err, time.Since(started))
	}
}

func TestOperationLaunchStateConflictTerminatesOwnedPreparation(t *testing.T) {
	root := operationTempDir(t)
	plan := operationUpgradePlan(root)
	if err := os.MkdirAll(plan.SourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.BinaryPath, []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	plan.BinarySHA256, _ = safeio.FileSHA256(plan.BinaryPath)
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		t.Fatal(err)
	}
	if err := acquireOperation(plan.RuntimeRoot, plan.ID, phasePrepared); err != nil {
		t.Fatal(err)
	}
	if err := writeState(plan.RuntimeRoot, State{Status: "PREPARING", OperationID: plan.ID}); err != nil {
		t.Fatal(err)
	}
	assertOperationCode(t, Launch(plan, os.Getpid()), "UPGRADE_RECOVERY_REQUIRED")
	record, _, err := readOperationRecord(plan.RuntimeRoot)
	if err != nil || record.Active || record.Phase != phaseReleased {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestCanRollbackAllowsCompensatedFailureOnly(t *testing.T) {
	state := State{
		Status: "FAILED", CurrentVersion: "0.7.0", PreviousVersion: "0.6.10",
		SnapshotID: testOperationA, SnapshotManifestSHA256: testOperationB,
	}
	if !CanRollback(state, "0.7.0") {
		t.Fatal("compensated failed rollback should remain retryable")
	}
	state.SnapshotManifestSHA256 = ""
	if CanRollback(state, "0.7.0") {
		t.Fatal("failed upgrade without snapshot manifest digest must not be retryable")
	}
}

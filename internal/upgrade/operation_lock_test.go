package upgrade

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	testOperationA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testOperationB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestOperationLockRejectsConcurrentPrepare(t *testing.T) {
	root := operationTempDir(t)
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, operationID := range []string{testOperationA, testOperationB} {
		go func(id string) {
			<-start
			results <- acquireOperation(root, id, phasePreparing)
		}(operationID)
	}
	close(start)
	var success, conflict int
	for range 2 {
		err := <-results
		if err == nil {
			success++
		} else if errorContainsCode(err, "UPGRADE_OPERATION_IN_PROGRESS") {
			conflict++
		} else {
			t.Fatalf("acquire = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestOperationLockRejectsSecondProcess(t *testing.T) {
	if os.Getenv("YTQJK_OPERATION_LOCK_HELPER") == "1" {
		runOperationLockHelper(t)
		return
	}
	root := operationTempDir(t)
	command := exec.Command(os.Args[0], "-test.run=^TestOperationLockRejectsSecondProcess$")
	command.Env = append(os.Environ(), "YTQJK_OPERATION_LOCK_HELPER=1", "YTQJK_OPERATION_ROOT="+root)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || strings.TrimSpace(line) != "READY" {
		t.Fatalf("helper readiness = %q, %v", line, err)
	}
	assertOperationCode(t, acquireOperation(root, testOperationB, phasePreparing), "UPGRADE_OPERATION_IN_PROGRESS")
	_ = stdin.Close()
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationLockDoesNotStealExpiredLiveOwner(t *testing.T) {
	root := operationTempDir(t)
	seedOperationRecord(t, root, testOperationA, os.Getpid(), phasePrepared, time.Now().Add(-time.Minute))
	assertOperationCode(t, acquireOperation(root, testOperationB, phasePreparing), "UPGRADE_OPERATION_IN_PROGRESS")
}

func TestOperationLockReclaimsExpiredDeadPreMutation(t *testing.T) {
	root := operationTempDir(t)
	deadPID := deadOperationPID(t)
	seedOperationRecord(t, root, testOperationA, deadPID, phasePrepared, time.Now().Add(-time.Minute))
	if err := acquireOperation(root, testOperationB, phasePreparing); err != nil {
		t.Fatal(err)
	}
	record, exists, err := readOperationRecord(root)
	if err != nil || !exists || record.OperationID != testOperationB {
		t.Fatalf("record = %#v, %v, %v", record, exists, err)
	}
}

func TestOperationLockBlocksDeadMidMutation(t *testing.T) {
	root := operationTempDir(t)
	seedOperationRecord(t, root, testOperationA, deadOperationPID(t), phaseActivating, time.Now().Add(-time.Minute))
	if err := writeState(root, State{Status: "ACTIVE", OperationID: testOperationA}); err != nil {
		t.Fatal(err)
	}
	assertOperationCode(t, acquireOperation(root, testOperationB, phasePreparing), "UPGRADE_RECOVERY_REQUIRED")
}

func TestOperationLockClaimRequiresTransferredOwner(t *testing.T) {
	root := operationTempDir(t)
	seedOperationRecord(t, root, testOperationA, deadOperationPID(t), phaseActivationPending, time.Now().Add(time.Minute))
	assertOperationCode(t, claimOperation(root, testOperationA, phaseActivationPending), "UPGRADE_RECOVERY_REQUIRED")
}

func TestOperationLockClaimRejectsReusedPIDIdentity(t *testing.T) {
	root := operationTempDir(t)
	seedOperationRecord(t, root, testOperationA, os.Getpid(), phaseActivationPending, time.Now().Add(time.Minute))
	record, _, err := readOperationRecord(root)
	if err != nil {
		t.Fatal(err)
	}
	record.OwnerIdentity = "windows:reused-process-identity"
	if err := safeio.WriteJSON(operationRecordPath(root), record); err != nil {
		t.Fatal(err)
	}
	assertOperationCode(t, claimOperation(root, testOperationA, phaseActivationPending), "UPGRADE_RECOVERY_REQUIRED")
}

func TestOperationLockMissingRecordRejectsNonterminalState(t *testing.T) {
	root := operationTempDir(t)
	if err := writeState(root, State{Status: "PREPARED", OperationID: testOperationA}); err != nil {
		t.Fatal(err)
	}
	assertOperationCode(t, acquireOperation(root, testOperationB, phasePreparing), "UPGRADE_RECOVERY_REQUIRED")
}

func TestOperationLockReleaseRejectsNonterminalState(t *testing.T) {
	root := operationTempDir(t)
	seedOperationRecord(t, root, testOperationA, os.Getpid(), phaseRollingBack, time.Now().Add(time.Minute))
	if err := writeState(root, State{Status: "ROLLBACK_FAILED", OperationID: testOperationA}); err != nil {
		t.Fatal(err)
	}
	assertOperationCode(t, releaseTerminalOperation(root, testOperationA, nil), "UPGRADE_RECOVERY_REQUIRED")
	record, exists, err := readOperationRecord(root)
	if err != nil || !exists || !record.Active {
		t.Fatalf("active record = %#v, %v, %v", record, exists, err)
	}
}

func TestOperationLaunchRequiresCurrentParentPID(t *testing.T) {
	assertOperationCode(t, Launch(Plan{}, os.Getpid()+1), "UPGRADE_HELPER_START_FAILED")
	assertOperationCode(t, LaunchRollback(RollbackPlan{}, os.Getpid()+1), "UPGRADE_HELPER_START_FAILED")
}

func TestOperationPendingAbortAuthenticatesPlan(t *testing.T) {
	root := operationTempDir(t)
	plan := operationUpgradePlan(root)
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
	if err := AbortPendingActivation(planPath(plan), digest, "UPGRADE_PARENT_STILL_RUNNING"); err != nil {
		t.Fatal(err)
	}
	state, err := readOperationState(plan.RuntimeRoot)
	if err != nil || state.Status != "FAILED" {
		t.Fatalf("state = %#v, %v", state, err)
	}
	record, _, err := readOperationRecord(plan.RuntimeRoot)
	if err != nil || record.Active || record.Phase != phaseReleased {
		t.Fatalf("record = %#v, %v", record, err)
	}
}

func TestOperationLockReleaseIsDurableTombstone(t *testing.T) {
	root := operationTempDir(t)
	if err := acquireOperation(root, testOperationA, phasePrepared); err != nil {
		t.Fatal(err)
	}
	if err := writeState(root, State{Status: "FAILED", OperationID: testOperationA}); err != nil {
		t.Fatal(err)
	}
	if err := releaseTerminalOperation(root, testOperationA, nil); err != nil {
		t.Fatal(err)
	}
	record, exists, err := readOperationRecord(root)
	if err != nil || !exists || record.Active || record.Phase != phaseReleased || record.FinishedAt.IsZero() {
		t.Fatalf("released record = %#v, %v, %v", record, exists, err)
	}
	if err := acquireOperation(root, testOperationB, phasePreparing); err != nil {
		t.Fatal(err)
	}
}

func TestOperationLockSerializesTransferAndAcquire(t *testing.T) {
	root := operationTempDir(t)
	if err := acquireOperation(root, testOperationA, phaseActivationPending); err != nil {
		t.Fatal(err)
	}
	unlock, err := lockOperationGuard(filepath.Join(root, "upgrade", "operation.guard"))
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	wait.Add(2)
	results := make(chan error, 2)
	go func() {
		defer wait.Done()
		results <- transferOperation(root, testOperationA, phaseActivationPending, os.Getpid(), os.Getpid())
	}()
	go func() {
		defer wait.Done()
		results <- acquireOperation(root, testOperationB, phasePreparing)
	}()
	time.Sleep(30 * time.Millisecond)
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	wait.Wait()
	close(results)
	var success, conflict int
	for err := range results {
		if err == nil {
			success++
		} else if errorContainsCode(err, "UPGRADE_OPERATION_IN_PROGRESS") {
			conflict++
		} else {
			t.Fatalf("result = %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}

func TestOperationLockRestoresRecordAfterPostCommitError(t *testing.T) {
	root := operationTempDir(t)
	if err := acquireOperation(root, testOperationA, phasePrepared); err != nil {
		t.Fatal(err)
	}
	originalWriter := writeOperationJSON
	calls := 0
	writeOperationJSON = func(path string, value any) error {
		calls++
		if err := safeio.WriteJSON(path, value); err != nil {
			return err
		}
		if calls == 1 {
			return &safeio.PostCommitError{Operation: "test", Err: errors.New("sync failed")}
		}
		return nil
	}
	t.Cleanup(func() { writeOperationJSON = originalWriter })
	assertOperationCode(
		t,
		transitionOperation(root, testOperationA, phasePrepared, phaseActivationPending),
		"UPGRADE_OPERATION_DURABILITY_UNKNOWN",
	)
	record, exists, err := readOperationRecord(root)
	if err != nil || !exists || record.Phase != phasePrepared {
		t.Fatalf("restored record = %#v, %v, %v", record, exists, err)
	}
}

func runOperationLockHelper(t *testing.T) {
	root := os.Getenv("YTQJK_OPERATION_ROOT")
	if err := acquireOperation(root, testOperationA, phasePreparing); err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(os.Stdout, "READY")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func operationUpgradePlan(root string) Plan {
	runtimeRoot := filepath.Join(root, "runtime")
	stageRoot := filepath.Join(runtimeRoot, "upgrade", "staging", testOperationA)
	return Plan{
		Schema: planSchema, ID: testOperationA, PreparedAt: time.Now().UTC(),
		FromVersion: "0.6.10", ToVersion: "0.7.0", PreviousMaxSchema: 4, TargetMaxSchema: 4,
		RuntimeRoot: runtimeRoot, CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"), StageRoot: stageRoot,
		SourceRoot:       filepath.Join(stageRoot, "source"),
		SourceTreeSHA256: testOperationB, BinaryPath: filepath.Join(stageRoot, "source", "ytqjk.exe"),
		BinarySHA256: testOperationB, ArchiveSHA256: testOperationB,
		ReleaseManifestSHA256: testOperationB, SigningKeySHA256: testOperationB, Port: 8765,
	}
}

func seedOperationRecord(
	t *testing.T,
	runtimeRoot, operationID string,
	ownerPID int,
	phase string,
	expiresAt time.Time,
) {
	t.Helper()
	ownerIdentity, err := operationProcessIdentity(ownerPID)
	if err != nil {
		ownerIdentity = fmt.Sprintf("dead-test:%d", ownerPID)
	}
	now := time.Now().UTC().Add(-2 * time.Minute)
	record := operationRecord{
		Schema: operationLockSchema, OperationID: operationID, OwnerPID: ownerPID, OwnerIdentity: ownerIdentity,
		Phase: phase, Active: true, AcquiredAt: now, RenewedAt: now,
		LeaseExpiresAt: expiresAt.UTC(),
	}
	if !record.LeaseExpiresAt.After(record.RenewedAt) {
		record.RenewedAt = record.LeaseExpiresAt.Add(-time.Minute)
		record.AcquiredAt = record.RenewedAt.Add(-time.Minute)
	}
	if err := safeio.WriteJSON(operationRecordPath(runtimeRoot), record); err != nil {
		t.Fatal(err)
	}
}

func deadOperationPID(t *testing.T) int {
	t.Helper()
	for _, pid := range []int{1 << 30, 1<<30 + 1, 1<<29 + 1} {
		alive, err := operationProcessAlive(pid)
		if err == nil && !alive {
			return pid
		}
	}
	t.Skip("could not identify a definitely dead PID")
	return 0
}

func assertOperationCode(t *testing.T, err error, code string) {
	t.Helper()
	if !errorContainsCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

func operationTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "ytqjk-upgrade-operation-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(time.Second)
		for {
			removeErr := os.RemoveAll(root)
			_, statErr := os.Stat(root)
			if errors.Is(statErr, os.ErrNotExist) {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("operation temp cleanup: remove=%v stat=%v", removeErr, statErr)
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return root
}

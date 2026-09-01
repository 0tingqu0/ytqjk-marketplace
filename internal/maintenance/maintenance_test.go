package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	operationA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	operationB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestStateSequenceAndGeneration(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if permit.Fence().Generation != 0 {
		t.Fatalf("initial generation = %d", permit.Fence().Generation)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateMaintenance && record.Generation == 1 &&
			record.Intent.BaseGeneration == 0 && record.Intent.TargetGeneration == 1
	})
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	receipt, err := lease.Complete(OutcomeSucceeded)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Generation != 1 || receipt.Outcome != OutcomeSucceeded {
		t.Fatalf("receipt = %#v", receipt)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 1 && record.Intent == nil &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeSucceeded
	})
}

func TestDrainAbortDoesNotAdvanceGeneration(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err = BeginExclusive(ctx, scope, exclusiveOptions(operationA))
	assertCode(t, err, CodeWriterDrainTimeout)
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 0 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func TestExistingPermitCommitsDuringLaterDraining(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan struct {
		lease *Lease
		err   error
	}, 1)
	go func() {
		lease, beginErr := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
		result <- struct {
			lease *Lease
			err   error
		}{lease: lease, err: beginErr}
	}()
	waitForState(t, scope, StateDraining)
	if _, err := AcquireShared(context.Background(), scope); !IsCode(err, CodeActive) {
		t.Fatalf("new shared permit error = %v", err)
	}
	if err := permit.CheckFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := permit.Commit(func(fence Fence) error {
		if fence.Generation != 0 || fence.OperationID != operationA {
			return errors.New("draining fence did not bind the active operation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	started := <-result
	if started.err != nil {
		t.Fatal(started.err)
	}
	if _, err := started.lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
}

func TestMutationReserveIsMandatory(t *testing.T) {
	scope := newTestScope(t)
	base := time.Now().UTC()
	setTestClock(t, &base)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	base = base.Add(MaxExclusiveDuration - RecoveryReserve)
	if err := lease.BeginMutation(); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("begin mutation error = %v", err)
	}
	if _, err := lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCompleteDoesNotReopenAdmission(t *testing.T) {
	scope := newTestScope(t)
	base := time.Now().UTC()
	setTestClock(t, &base)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	base = base.Add(MaxExclusiveDuration + time.Second)
	_, err = lease.Complete(OutcomeSucceeded)
	assertCode(t, err, CodeRecoveryRequired)
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateRecoveryRequired && record.Generation == 1 && record.Intent != nil
	})
	_, err = AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeRecoveryRequired)
}

func TestControlFilesStayOutsideBusinessRoots(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{scope.RuntimeRoot, scope.CodexRoot, scope.KnowledgeRoot} {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("business root %s contains control files: %v", root, entries)
		}
	}
	controlEntries, err := os.ReadDir(filepath.Join(scope.ControlRoot, "maintenance"))
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(controlEntries))
	for index := range controlEntries {
		names[index] = controlEntries[index].Name()
	}
	if !sameStrings(names, []string{"guard.lock", "record.json", "writers.lock"}) {
		t.Fatalf("control plane entries = %v", names)
	}
}

func TestCorruptRecordFailsClosed(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(control.recordPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeStateCorrupt)
}

func newTestScope(t *testing.T) Scope {
	t.Helper()
	root := t.TempDir()
	scope := Scope{
		ControlRoot: filepath.Join(root, "control"), RuntimeRoot: filepath.Join(root, "runtime"),
		CodexRoot: filepath.Join(root, "codex"), KnowledgeRoot: filepath.Join(root, "knowledge"),
	}
	for _, path := range []string{scope.RuntimeRoot, scope.CodexRoot, scope.KnowledgeRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := BootstrapControlRoot(context.Background(), scope.ControlRoot); err != nil {
		t.Fatal(err)
	}
	return scope
}

func exclusiveOptions(operationID string) ExclusiveOptions {
	return ExclusiveOptions{
		OperationID: operationID, Purpose: "UPGRADE",
		Duration: MaxExclusiveDuration, DrainTimeout: time.Second,
	}
}

func setTestClock(t *testing.T, current *time.Time) {
	t.Helper()
	original := clockNow
	clockNow = func() time.Time { return current.UTC() }
	t.Cleanup(func() { clockNow = original })
}

func assertRecord(t *testing.T, scope Scope, predicate func(Record) bool) {
	t.Helper()
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := readRecord(control)
	if err != nil || !exists {
		t.Fatalf("read record: exists=%v err=%v", exists, err)
	}
	if !predicate(record) {
		t.Fatalf("record = %#v", record)
	}
}

func waitForState(t *testing.T, scope Scope, state State) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		matched := false
		assertRecord(t, scope, func(record Record) bool {
			matched = record.State == state
			return true
		})
		if matched {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for state %s", state)
}

func assertCode(t *testing.T, err error, code string) {
	t.Helper()
	if !IsCode(err, code) {
		t.Fatalf("error = %v, want %s", err, code)
	}
}

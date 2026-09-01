package maintenance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestCanaryTransitionsReconcileExactPostCommitReadback(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	t.Cleanup(func() { writeRecordJSON = original })

	writeRecordJSON = postCommitWriter(original, true)
	err = lease.BeginReopening(os.Getpid(), testCanaryOptions(lease))
	writeRecordJSON = original
	if err != nil {
		t.Fatalf("BeginReopening exact readback: %v", err)
	}

	writeRecordJSON = postCommitWriter(original, true)
	canary, err := ClaimCanary(context.Background(), scope, operationA, 1, testCanaryCapability)
	writeRecordJSON = original
	if err != nil || canary == nil {
		t.Fatalf("ClaimCanary exact readback: lease=%v err=%v", canary, err)
	}

	writeRecordJSON = postCommitWriter(original, true)
	err = canary.MarkReady(testHash("d"))
	writeRecordJSON = original
	if err != nil {
		t.Fatalf("MarkReady exact readback: %v", err)
	}

	writeRecordJSON = postCommitWriter(original, true)
	receipt, err := canary.Complete(OutcomeSucceeded, testHash("e"))
	writeRecordJSON = original
	if err != nil || receipt.Canary == nil {
		t.Fatalf("Complete exact readback: receipt=%#v err=%v", receipt, err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Receipt != nil && record.Receipt.Canary != nil
	})
}

func TestCanaryCompleteUnlockFailureIsPermanentUnknown(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	originalUnlock := canary.lock.unlock
	canary.lock.unlock = func() error {
		return errors.Join(originalUnlock(), errors.New("injected unlock report"))
	}
	receipt, err := canary.Complete(OutcomeSucceeded, testHash("e"))
	assertCode(t, err, CodeCommitResultUnknown)
	if receipt.Canary == nil || !canary.closed {
		t.Fatalf("terminal canary receipt=%#v closed=%v", receipt, canary.closed)
	}
	assertRecord(t, scope, func(record Record) bool { return record.State == StateOpen })
}

func TestCanaryNonexactPostCommitClosesLocalLease(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, false)
	err := canary.MarkReady(testHash("d"))
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	assertCode(t, err, CodeDurabilityUnknown)
	if !canary.closed {
		t.Fatal("ambiguous readiness kept the canary lease usable")
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := acquirePlaneLock(
		context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		t.Fatalf("writer lock leaked: %v", err)
	}
	if err := joinUnlock(nil, writer); err != nil {
		t.Fatal(err)
	}
}

func TestCanaryCompleteRetainsWriterUntilRecoveryIsDurable(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, false)
	_, err := canary.Complete(OutcomeSucceeded, testHash("e"))
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	assertCode(t, err, CodeDurabilityUnknown)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	writer, lockErr := acquirePlaneLock(
		context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
	)
	if lockErr == nil {
		_ = joinUnlock(nil, writer)
		t.Fatal("ambiguous canary OPEN released the writer lock")
	}
	assertCode(t, lockErr, CodeActive)
	if _, reconcileErr := canary.ReconcileCompletion(); !IsCode(reconcileErr, CodeRecoveryRequired) {
		t.Fatalf("canary reconciliation error = %v", reconcileErr)
	}
	assertRecord(t, scope, func(record Record) bool { return record.State == StateRecoveryRequired })
	writer, lockErr = acquirePlaneLock(
		context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
	)
	if lockErr != nil {
		t.Fatalf("writer lock remained after durable recovery: %v", lockErr)
	}
	if err := joinUnlock(nil, writer); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredCanaryRetainsWriterUntilRecoveryIsDurable(t *testing.T) {
	base := time.Now().UTC()
	setTestClock(t, &base)
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	base = canary.record.Intent.Canary.Deadline.Add(time.Nanosecond)
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, false)
	_, completeErr := canary.Complete(OutcomeSucceeded, testHash("e"))
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	assertCode(t, completeErr, CodeDurabilityUnknown)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	writer, lockErr := acquirePlaneLock(
		context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
	)
	if lockErr == nil {
		_ = joinUnlock(nil, writer)
		t.Fatal("ambiguous canary expiry released the writer lock")
	}
	assertCode(t, lockErr, CodeActive)
	if _, reconcileErr := canary.ReconcileCompletion(); !IsCode(reconcileErr, CodeRecoveryRequired) {
		t.Fatalf("canary expiry reconciliation error = %v", reconcileErr)
	}
	assertRecord(t, scope, func(record Record) bool { return record.State == StateRecoveryRequired })
}

package maintenance

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestCompleteReconcilesPostCommitReadbackWithoutFailOpen(t *testing.T) {
	for _, committed := range []bool{false, true} {
		name := "nonexact"
		if committed {
			name = "exact"
		}
		t.Run(name, func(t *testing.T) {
			scope := newTestScope(t)
			lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
			if err != nil {
				t.Fatal(err)
			}
			original := writeRecordJSON
			writeRecordJSON = postCommitWriter(original, committed)
			receipt, completeErr := lease.Complete(OutcomeAborted)
			writeRecordJSON = original
			t.Cleanup(func() { writeRecordJSON = original })
			if committed {
				if completeErr != nil {
					t.Fatalf("exact readback completion failed: %v", completeErr)
				}
			} else {
				assertCode(t, completeErr, CodeDurabilityUnknown)
			}
			control, err := normalizeScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			record, exists, err := readRecord(control)
			if err != nil || !exists {
				t.Fatalf("readback: exists=%v err=%v", exists, err)
			}
			if committed {
				if record.State != StateOpen || receipt.OperationID != operationA {
					t.Fatalf("exact readback record=%#v receipt=%#v", record, receipt)
				}
			} else if record.State != StateMaintenance || receipt.OperationID != "" {
				t.Fatalf("nonexact readback record=%#v receipt=%#v", record, receipt)
			}
			writer, lockErr := acquirePlaneLock(
				context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
			)
			if committed {
				if lockErr != nil {
					t.Fatalf("writer lock remained after exact OPEN: %v", lockErr)
				}
				if err := joinUnlock(nil, writer); err != nil {
					t.Fatal(err)
				}
				return
			}
			assertCode(t, lockErr, CodeActive)
			if _, reconcileErr := lease.ReconcileCompletion(); !IsCode(reconcileErr, CodeRecoveryRequired) {
				t.Fatalf("completion reconciliation error = %v", reconcileErr)
			}
			assertRecord(t, scope, func(record Record) bool {
				return record.State == StateRecoveryRequired
			})
			writer, lockErr = acquirePlaneLock(
				context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
			)
			if lockErr != nil {
				t.Fatalf("writer lock remained after durable recovery: %v", lockErr)
			}
			if err := joinUnlock(nil, writer); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestExpiredCompletionRetainsWriterUntilRecoveryIsDurable(t *testing.T) {
	base := time.Now().UTC()
	setTestClock(t, &base)
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	base = lease.ExpiresAt().Add(time.Nanosecond)
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, false)
	_, completeErr := lease.Complete(OutcomeAborted)
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
		t.Fatal("ambiguous expiry released the writer lock")
	}
	assertCode(t, lockErr, CodeActive)
	if _, reconcileErr := lease.ReconcileCompletion(); !IsCode(reconcileErr, CodeRecoveryRequired) {
		t.Fatalf("expiry reconciliation error = %v", reconcileErr)
	}
	assertRecord(t, scope, func(record Record) bool { return record.State == StateRecoveryRequired })
}

func TestBeginMutationReconcilesExactPostCommit(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, true)
	err = lease.BeginMutation()
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	if err != nil {
		t.Fatalf("exact mutation readback failed: %v", err)
	}
	if lease.record.Intent.MutationStarted == nil || lease.closed {
		t.Fatalf("exact mutation readback was not retained: %#v", lease.record)
	}
	if err := lease.MarkRecoveryRequired(CodeRecoveryRequired, "test cleanup after exact readback"); err != nil {
		t.Fatal(err)
	}
}

func TestBeginMutationNonexactPostCommitClosesLocalLease(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, false)
	err = lease.BeginMutation()
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	assertCode(t, err, CodeDurabilityUnknown)
	if !lease.closed {
		t.Fatal("ambiguous mutation kept the local lease usable")
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

func TestTransferReconcilesExactPostCommitAndTerminalizesSource(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, true)
	err = lease.Transfer(os.Getpid())
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	if err != nil {
		t.Fatalf("exact transfer readback failed: %v", err)
	}
	if !lease.closed || !lease.record.Intent.TransferPending {
		t.Fatalf("source lease was not terminalized: %#v", lease.record)
	}
	claimed, err := ClaimExclusive(context.Background(), scope, operationA, lease.Generation())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := claimed.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
}

func TestMarkRecoveryRequiredReconcilesExactPostCommit(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	writeRecordJSON = postCommitWriter(original, true)
	err = lease.MarkRecoveryRequired(CodeRecoveryRequired, "injected unknown result")
	writeRecordJSON = original
	t.Cleanup(func() { writeRecordJSON = original })
	if err != nil {
		t.Fatalf("exact recovery marker readback failed: %v", err)
	}
	if !lease.closed || lease.record.State != StateRecoveryRequired {
		t.Fatalf("unsafe terminal was not retained: %#v", lease.record)
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

func TestHandleCreatingTransitionsReturnBoundHandleAfterExactReadback(t *testing.T) {
	t.Run("begin drain", func(t *testing.T) {
		scope := newTestScope(t)
		original := writeRecordJSON
		writeRecordJSON = postCommitWriter(original, true)
		drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
		writeRecordJSON = original
		if err != nil || drainer == nil {
			t.Fatalf("BeginDrain() drainer=%v err=%v", drainer, err)
		}
		if _, err := drainer.Abort(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("enter maintenance", func(t *testing.T) {
		scope := newTestScope(t)
		drainer, err := BeginDrain(context.Background(), scope, exclusiveOptions(operationA))
		if err != nil {
			t.Fatal(err)
		}
		original := writeRecordJSON
		writeRecordJSON = postCommitWriter(original, true)
		lease, err := AwaitExclusive(context.Background(), drainer)
		writeRecordJSON = original
		if err != nil || lease == nil {
			t.Fatalf("AwaitExclusive() lease=%v err=%v", lease, err)
		}
		if _, err := lease.Complete(OutcomeAborted); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("claim transfer", func(t *testing.T) {
		scope := newTestScope(t)
		lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
		if err != nil {
			t.Fatal(err)
		}
		generation := lease.Generation()
		if err := lease.Transfer(os.Getpid()); err != nil {
			t.Fatal(err)
		}
		original := writeRecordJSON
		writeRecordJSON = postCommitWriter(original, true)
		claimed, err := ClaimExclusive(context.Background(), scope, operationA, generation)
		writeRecordJSON = original
		if err != nil || claimed == nil {
			t.Fatalf("ClaimExclusive() lease=%v err=%v", claimed, err)
		}
		if _, err := claimed.Complete(OutcomeAborted); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("recover dead owner", func(t *testing.T) {
		scope := newTestScope(t)
		now := clockNow()
		record := Record{
			Schema: recordSchema, State: StateMaintenance, Generation: 5,
			Revision: 9, UpdatedAt: now.Add(-time.Minute),
			Intent: &Intent{
				OperationID: operationA, Purpose: "UPGRADE", Owner: deadOwner(),
				BaseGeneration: 4, TargetGeneration: 5,
				StartedAt: now.Add(-2 * time.Minute), UpdatedAt: now.Add(-time.Minute),
				DrainDeadline: now.Add(-90 * time.Second), ExpiresAt: now.Add(10 * time.Minute),
			},
		}
		writeStaleRecord(t, scope, record)
		original := writeRecordJSON
		writeRecordJSON = postCommitWriter(original, true)
		lease, err := RecoverExclusive(context.Background(), scope, operationA, 5)
		writeRecordJSON = original
		if err != nil || lease == nil {
			t.Fatalf("RecoverExclusive() lease=%v err=%v", lease, err)
		}
		if _, err := lease.Complete(OutcomeAborted); err != nil {
			t.Fatal(err)
		}
	})
}

func postCommitWriter(
	original func(controlPlane, any) error,
	committed bool,
) func(controlPlane, any) error {
	return func(control controlPlane, value any) error {
		if committed {
			if err := original(control, value); err != nil {
				return err
			}
		}
		return &safeio.PostCommitError{Operation: "test transition", Err: errors.New("sync failed")}
	}
}

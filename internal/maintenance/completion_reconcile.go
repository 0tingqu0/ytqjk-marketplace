package maintenance

import (
	"context"
	"errors"
	"reflect"
)

type completionResolution uint8

const (
	completionUnresolved completionResolution = iota
	completionOpen
	completionRecoveryRequired
)

type completionPending struct {
	previous Record
	expected Record
	cause    string
}

func newCompletionPending(previous, expected Record, cause string) completionPending {
	return completionPending{
		previous: cloneRecord(previous), expected: cloneRecord(expected), cause: cause,
	}
}

func reconcileOpenCompletion(
	control controlPlane,
	pending completionPending,
	originalErr error,
) (completionResolution, Record, error) {
	observed, exists, readErr := readRecord(control)
	if readErr == nil && exists && reflect.DeepEqual(observed, pending.expected) {
		if observed.State == StateOpen {
			return completionOpen, observed, nil
		}
		if observed.State == StateRecoveryRequired {
			return completionRecoveryRequired, observed,
				fail(CodeRecoveryRequired, errors.New("recovery-required state is durable"))
		}
	}
	if readErr != nil || !exists || !reflect.DeepEqual(observed, pending.previous) || observed.Intent == nil {
		return completionUnresolved, Record{}, errors.Join(
			originalErr, readErr,
			fail(CodeRecoveryRequired, errors.New("OPEN durability requires explicit reconciliation")),
		)
	}
	next := cloneRecord(pending.expected)
	if next.State != StateRecoveryRequired {
		next = cloneRecord(observed)
		next.State = StateRecoveryRequired
		next.Generation = next.Intent.TargetGeneration
		next.Intent.TransferPending = false
		next.Intent.Recovery = &Recovery{
			Code: CodeRecoveryRequired, Cause: pending.cause, MarkedAt: clockNow(),
		}
		advanceRecord(&next)
	}
	committed, transitionErr := persistTransition(control, observed, next)
	if committed {
		return completionRecoveryRequired, next,
			fail(CodeRecoveryRequired, errors.New("OPEN was not committed; recovery is required"))
	}
	return completionUnresolved, Record{}, errors.Join(
		originalErr, transitionErr,
		fail(CodeRecoveryRequired, errors.New("RECOVERY_REQUIRED durability could not be confirmed")),
	)
}

// ReconcileCompletion resolves an ambiguous final OPEN write without releasing
// the process-local writer lock until OPEN or RECOVERY_REQUIRED is exact.
func (lease *Lease) ReconcileCompletion() (Receipt, error) {
	if lease == nil {
		return Receipt{}, fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return Receipt{}, fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.pending == nil || lease.lock.unlock == nil {
		return Receipt{}, fail(CodeRecoveryRequired, errors.New("no completion requires reconciliation"))
	}
	return lease.reconcileCompletionLocked()
}

func (lease *Lease) reconcileCompletionLocked() (Receipt, error) {
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return Receipt{}, err
	}
	resolution, record, resolutionErr := reconcileOpenCompletion(lease.control, *lease.pending, nil)
	guardErr := joinUnlock(nil, guard)
	if resolution == completionUnresolved {
		return Receipt{}, errors.Join(resolutionErr, guardErr)
	}
	lease.record = record
	lease.pending = nil
	lease.closed = true
	unlockErr := lease.releaseWriterLocked()
	if resolution == completionOpen {
		receipt := cloneReceipt(*record.Receipt)
		if cleanupErr := errors.Join(guardErr, unlockErr); cleanupErr != nil {
			return receipt, fail(CodeCommitResultUnknown, cleanupErr)
		}
		return receipt, nil
	}
	return Receipt{}, errors.Join(resolutionErr, guardErr, unlockErr)
}

// ReconcileCompletion resolves an ambiguous canary OPEN write while retaining
// exclusive admission until an exact durable state is known.
func (lease *CanaryLease) ReconcileCompletion() (Receipt, error) {
	if lease == nil {
		return Receipt{}, fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return Receipt{}, fail(CodeInvalid, errors.New("canary lease was copied"))
	}
	if lease.pending == nil || lease.lock.unlock == nil {
		return Receipt{}, fail(CodeRecoveryRequired, errors.New("no canary completion requires reconciliation"))
	}
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return Receipt{}, err
	}
	resolution, record, resolutionErr := reconcileOpenCompletion(lease.control, *lease.pending, nil)
	guardErr := joinUnlock(nil, guard)
	if resolution == completionUnresolved {
		return Receipt{}, errors.Join(resolutionErr, guardErr)
	}
	lease.record = record
	lease.pending = nil
	lease.closed = true
	unlockErr := lease.releaseWriterLocked()
	if resolution == completionOpen {
		receipt := cloneReceipt(*record.Receipt)
		if cleanupErr := errors.Join(guardErr, unlockErr); cleanupErr != nil {
			return receipt, fail(CodeCommitResultUnknown, cleanupErr)
		}
		return receipt, nil
	}
	return Receipt{}, errors.Join(resolutionErr, guardErr, unlockErr)
}

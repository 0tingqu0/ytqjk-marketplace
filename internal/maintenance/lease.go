package maintenance

import (
	"context"
	"errors"
	"os"
	"time"
)

func (lease *Lease) OperationID() string {
	if lease == nil {
		return ""
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease || lease.record.Intent == nil {
		return ""
	}
	return lease.record.Intent.OperationID
}

func (lease *Lease) Generation() uint64 {
	if lease == nil {
		return 0
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return 0
	}
	if lease.record.Intent == nil {
		return lease.record.Generation
	}
	return lease.record.Intent.TargetGeneration
}

// ExpiresAt is the immutable global maintenance deadline. The package never
// extends it during transfer or recovery.
func (lease *Lease) ExpiresAt() time.Time {
	if lease == nil {
		return time.Time{}
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease || lease.record.Intent == nil {
		return time.Time{}
	}
	return lease.record.Intent.ExpiresAt
}

func (lease *Lease) BeginMutation() error {
	if lease == nil {
		return fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.closed {
		return fail(CodeRecoveryRequired, errors.New("maintenance lease is closed"))
	}
	_, err := lease.updateOwned(func(next *Record) error {
		now := clockNow()
		if next.State != StateMaintenance || next.Intent.TransferPending || next.Intent.MutationStarted != nil {
			return fail(CodeRecoveryRequired, errors.New("maintenance mutation cannot start in the current state"))
		}
		if !now.Before(next.Intent.ExpiresAt.Add(-RecoveryReserve)) {
			return fail(CodeRecoveryRequired, errors.New("maintenance recovery reserve is exhausted"))
		}
		next.Intent.MutationStarted = &now
		return nil
	})
	return err
}

func (lease *Lease) Transfer(childPID int) error {
	if lease == nil || childPID <= 0 {
		return fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.closed {
		return fail(CodeRecoveryRequired, errors.New("maintenance lease is closed"))
	}
	alive, err := processAlive(childPID)
	if err != nil || !alive {
		return fail(CodeLockFailed, errors.Join(err, os.ErrProcessDone))
	}
	identity, err := processIdentity(childPID)
	if err != nil {
		return fail(CodeLockFailed, err)
	}
	committed, err := lease.updateOwned(func(next *Record) error {
		now := clockNow()
		if next.State != StateMaintenance || next.Intent.MutationStarted != nil ||
			!now.Before(next.Intent.ExpiresAt.Add(-RecoveryReserve)) {
			return fail(CodeRecoveryRequired, errors.New("maintenance lease cannot be transferred"))
		}
		next.Intent.Owner = Owner{PID: childPID, Identity: identity}
		next.Intent.TransferPending = true
		return nil
	})
	if !committed {
		return err
	}
	lease.closed = true
	unlockErr := joinUnlock(nil, lease.lock)
	lease.lock = heldLock{}
	if unlockErr != nil {
		err = errors.Join(err, fail(CodeRecoveryRequired, errors.New("maintenance transfer unlock failed")), unlockErr)
	}
	return err
}

func (lease *Lease) Complete(outcome Outcome) (Receipt, error) {
	if lease == nil || !validOutcome(outcome) {
		return Receipt{}, fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return Receipt{}, fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.closed {
		return Receipt{}, fail(CodeRecoveryRequired, errors.New("maintenance lease is closed"))
	}
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return Receipt{}, err
	}
	record, exists, err := readRecord(lease.control)
	owner, ownerErr := currentOwner()
	if err == nil && (!exists || ownerErr != nil) {
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("maintenance record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireOwned(
			record, lease.control, lease.record.Intent.OperationID,
			lease.record.Intent.TargetGeneration, owner,
		)
	}
	expired := err == nil && !clockNow().Before(record.Intent.ExpiresAt)
	safeExpiredPreMutationAbort := expired && lease.recovery && record.Intent.MutationStarted == nil &&
		outcome == OutcomeAborted
	if expired && !safeExpiredPreMutationAbort {
		next := cloneRecord(record)
		next.State = StateRecoveryRequired
		next.Generation = next.Intent.TargetGeneration
		next.Intent.Recovery = &Recovery{
			Code: CodeRecoveryRequired, Cause: "maintenance lease expired before completion", MarkedAt: clockNow(),
		}
		advanceRecord(&next)
		committed, transitionErr := persistTransition(lease.control, record, next)
		err = transitionErr
		resolution := completionUnresolved
		resolvedRecord := Record{}
		if committed {
			lease.record = next
			resolution = completionRecoveryRequired
		} else if IsCode(err, CodeDurabilityUnknown) {
			pending := newCompletionPending(record, next, "maintenance lease expired before completion")
			resolution, resolvedRecord, err = reconcileOpenCompletion(lease.control, pending, err)
			if resolution == completionUnresolved {
				lease.pending = &pending
				lease.closed = true
			}
		}
		guardErr := joinUnlock(nil, guard)
		if resolution != completionUnresolved {
			if !committed {
				lease.record = resolvedRecord
			}
			lease.closed = true
			unlockErr := lease.releaseWriterLocked()
			return Receipt{}, errors.Join(
				err, guardErr,
				fail(CodeRecoveryRequired, errors.New("maintenance lease expired before completion")),
				unlockErr,
			)
		}
		return Receipt{}, errors.Join(err, guardErr)
	}
	if err == nil {
		err = validateCompletion(record, outcome)
	}
	var receipt Receipt
	var expected Record
	transitionAttempted := false
	committed := false
	if err == nil {
		receipt = Receipt{
			OperationID: record.Intent.OperationID, Generation: record.Generation,
			Outcome: outcome, Resources: cloneStrings(record.Intent.Resources), FinishedAt: clockNow(),
		}
		expected = Record{
			Schema: recordSchema, State: StateOpen, Generation: record.Generation,
			Revision: record.Revision + 1, UpdatedAt: receipt.FinishedAt, Receipt: &receipt,
		}
		transitionAttempted = true
		committed, err = persistTransition(lease.control, record, expected)
		if committed {
			lease.record = expected
			lease.closed = true
		}
	}
	resolution := completionUnresolved
	resolvedRecord := Record{}
	if transitionAttempted && !committed && IsCode(err, CodeDurabilityUnknown) {
		pending := newCompletionPending(record, expected, "maintenance OPEN durability could not be confirmed")
		resolution, resolvedRecord, err = reconcileOpenCompletion(lease.control, pending, err)
		if resolution == completionUnresolved {
			lease.pending = &pending
		}
	}
	guardErr := joinUnlock(nil, guard)
	if transitionAttempted && (committed || resolution != completionUnresolved) {
		lease.closed = true
		lease.pending = nil
		if !committed {
			lease.record = resolvedRecord
		}
		unlockErr := lease.releaseWriterLocked()
		if committed || resolution == completionOpen {
			if cleanupErr := errors.Join(err, guardErr, unlockErr); cleanupErr != nil {
				return cloneReceipt(receipt), fail(CodeCommitResultUnknown, cleanupErr)
			}
			return cloneReceipt(receipt), nil
		}
		return Receipt{}, errors.Join(err, guardErr, unlockErr)
	}
	if transitionAttempted && lease.pending != nil {
		lease.closed = true
		return Receipt{}, errors.Join(err, guardErr)
	}
	if err != nil || guardErr != nil {
		return Receipt{}, errors.Join(err, guardErr)
	}
	return cloneReceipt(receipt), lease.releaseWriterLocked()
}

func (lease *Lease) updateOwned(update func(*Record) error) (bool, error) {
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return false, err
	}
	record, exists, err := readRecord(lease.control)
	owner, ownerErr := currentOwner()
	if err == nil && (!exists || ownerErr != nil) {
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("maintenance record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireOwned(
			record, lease.control, lease.record.Intent.OperationID,
			lease.record.Intent.TargetGeneration, owner,
		)
	}
	if err == nil {
		next := cloneRecord(record)
		err = update(&next)
		if err == nil {
			advanceRecord(&next)
			if bindingErr := refreshCanaryActiveBinding(&next); bindingErr != nil {
				return false, errors.Join(bindingErr, joinUnlock(nil, guard))
			}
			var committed bool
			committed, err = persistTransition(lease.control, record, next)
			if committed {
				lease.record = next
			}
			result := errors.Join(err, joinUnlock(nil, guard))
			if !committed && IsCode(result, CodeDurabilityUnknown) {
				lease.closed = true
				result = errors.Join(result, lease.releaseWriterLocked())
			}
			return committed, result
		}
	}
	return false, errors.Join(err, joinUnlock(nil, guard))
}

func (lease *Lease) releaseWriterLocked() error {
	err := joinUnlock(nil, lease.lock)
	lease.lock = heldLock{}
	return err
}

func validateCompletion(record Record, outcome Outcome) error {
	if record.Intent == nil || record.Intent.TransferPending {
		return fail(CodeRecoveryRequired, nil)
	}
	switch record.State {
	case StateDraining:
		if record.Intent.MutationStarted == nil && outcome == OutcomeAborted {
			return nil
		}
	case StateMaintenance:
		if record.Intent.MutationStarted == nil && outcome == OutcomeAborted {
			return nil
		}
		if record.Intent.MutationStarted != nil &&
			(outcome == OutcomeSucceeded || outcome == OutcomeRolledBack || outcome == OutcomeFailedSafe) {
			return nil
		}
	case StateRestoring:
		if outcome == OutcomeRolledBack || outcome == OutcomeFailedSafe {
			return nil
		}
	}
	return fail(CodeInvalid, errors.New("maintenance outcome does not match state"))
}

func advanceRecord(record *Record) {
	now := clockNow()
	record.Revision++
	record.UpdatedAt = now
	if record.Intent != nil {
		record.Intent.UpdatedAt = now
	}
}

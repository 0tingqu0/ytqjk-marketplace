package maintenance

import (
	"context"
	"errors"
)

// Complete persists OPEN and its canary receipt before releasing the exclusive
// writer lock. expectedOutcome must match the authenticated binding.
func (lease *CanaryLease) Complete(expectedOutcome Outcome, finalStateSHA256 string) (Receipt, error) {
	if lease == nil || !validCanaryOutcome(expectedOutcome) || !validSHA256(finalStateSHA256) {
		return Receipt{}, fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.requireUsableLocked(); err != nil {
		return Receipt{}, err
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
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("reopening record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireCanaryRecord(
			record, lease.control, lease.record.Intent.OperationID,
			lease.record.Intent.TargetGeneration, owner, false, true,
		)
	}
	if err == nil && record.Revision != lease.record.Revision {
		err = fail(CodeRecoveryRequired, errors.New("canary lease revision changed"))
	}
	if err == nil && !clockNow().Before(record.Intent.Canary.Deadline) {
		return lease.completeExpiredCanary(record, guard)
	}
	if err == nil && (record.Intent.Canary.ReadyAt == nil ||
		record.Intent.Canary.ReadyReceiptSHA256 == "") {
		err = fail(CodeInvalid, errors.New("canary is not ready"))
	}
	if err == nil && !canaryOutcomeAllowed(
		record.Intent.Canary.ExpectedOutcome, record.Intent.Canary.FallbackOutcome, expectedOutcome,
	) {
		err = fail(CodeInvalid, errors.New("canary completion outcome does not match binding"))
	}
	var receipt Receipt
	var expected Record
	committed := false
	transitionAttempted := false
	if err == nil {
		receipt, expected, err = lease.buildCanaryCompletion(record, expectedOutcome, finalStateSHA256)
		if err == nil {
			transitionAttempted = true
			committed, err = persistTransition(lease.control, record, expected)
			if committed {
				lease.record = expected
			}
		}
	}
	resolution := completionUnresolved
	resolvedRecord := Record{}
	if transitionAttempted && !committed && IsCode(err, CodeDurabilityUnknown) {
		pending := newCompletionPending(record, expected, "canary OPEN durability could not be confirmed")
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
				return cloneReceipt(receipt), fail(
					CodeCommitResultUnknown,
					errors.Join(errors.New("canary OPEN committed before cleanup failed"), cleanupErr),
				)
			}
			return cloneReceipt(receipt), nil
		}
		return Receipt{}, errors.Join(err, guardErr, unlockErr)
	}
	if transitionAttempted && lease.pending != nil {
		lease.closed = true
	}
	return Receipt{}, errors.Join(err, guardErr)
}

func (lease *CanaryLease) completeExpiredCanary(record Record, guard heldLock) (Receipt, error) {
	expected := expiredCanaryRecord(record)
	committed, err := persistTransition(lease.control, record, expected)
	resolution := completionUnresolved
	resolvedRecord := Record{}
	if committed {
		lease.record = expected
		resolution = completionRecoveryRequired
	} else if IsCode(err, CodeDurabilityUnknown) {
		pending := newCompletionPending(record, expected, "canary deadline expired before completion")
		resolution, resolvedRecord, err = reconcileOpenCompletion(lease.control, pending, err)
		if resolution == completionUnresolved {
			lease.pending = &pending
			lease.closed = true
		}
	}
	guardErr := joinUnlock(nil, guard)
	if resolution == completionUnresolved {
		return Receipt{}, errors.Join(err, guardErr)
	}
	if !committed {
		lease.record = resolvedRecord
	}
	lease.closed = true
	return Receipt{}, errors.Join(
		err, guardErr,
		fail(CodeRecoveryRequired, errors.New("canary deadline expired before completion")),
		lease.releaseWriterLocked(),
	)
}

func (lease *CanaryLease) buildCanaryCompletion(
	record Record,
	expectedOutcome Outcome,
	finalStateSHA256 string,
) (Receipt, Record, error) {
	canary := record.Intent.Canary
	receipt := Receipt{
		OperationID: record.Intent.OperationID, Generation: record.Generation,
		Outcome: expectedOutcome, Resources: cloneStrings(record.Intent.Resources), FinishedAt: clockNow(),
		Canary: &CanaryReceipt{
			Owner: canary.Owner, Purpose: record.Intent.Purpose,
			BaseGeneration: record.Intent.BaseGeneration, StartedAt: record.Intent.StartedAt,
			ExpiresAt: record.Intent.ExpiresAt, DrainDeadline: record.Intent.DrainDeadline,
			MutationStarted:   *record.Intent.MutationStarted,
			CapabilitySHA256:  canary.CapabilitySHA256,
			BaseBindingSHA256: canary.BaseBindingSHA256, ReadyBindingSHA256: canary.ReadyBindingSHA256,
			ActiveBindingSHA256: canary.ActiveBindingSHA256,
			PlanSHA256:          canary.PlanSHA256, SnapshotManifestSHA256: canary.SnapshotManifestSHA256,
			TargetBinarySHA256: canary.TargetBinarySHA256, TargetVersion: canary.TargetVersion,
			Port: canary.Port, Attempt: canary.Attempt, ExpectedOutcome: canary.ExpectedOutcome,
			FallbackOutcome:    canary.FallbackOutcome,
			Deadline:           canary.Deadline,
			ReadyReceiptSHA256: canary.ReadyReceiptSHA256, ReadyAt: *canary.ReadyAt,
			FinalStateSHA256: finalStateSHA256,
		},
	}
	next := Record{
		Schema: recordSchema, State: StateOpen, Generation: record.Generation,
		Revision: record.Revision + 1, UpdatedAt: receipt.FinishedAt, Receipt: &receipt,
	}
	binding, err := canaryReceiptBindingSHA256(lease.control, &next)
	if err != nil {
		return Receipt{}, Record{}, err
	}
	next.Receipt.Canary.ReceiptBindingSHA256 = binding
	return receipt, next, nil
}

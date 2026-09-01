package maintenance

import (
	"context"
	"errors"
)

func enterMaintenance(control controlPlane, writer heldLock, expected Record) (*Lease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLockWait)
	defer cancel()
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return nil, joinUnlock(err, writer)
	}
	record, exists, err := readRecord(control)
	if err == nil && !exists {
		err = fail(CodeRecoveryRequired, errors.New("draining record is unavailable"))
	}
	if err == nil {
		err = requireDrainer(record, control, expected, expected.Intent.Owner)
	}
	if err == nil && (!clockNow().Before(record.Intent.DrainDeadline) ||
		!clockNow().Before(record.Intent.ExpiresAt)) {
		err = fail(CodeRecoveryRequired, errors.New("draining operation cannot enter maintenance"))
	}
	if err == nil {
		next := cloneRecord(record)
		next.State = StateMaintenance
		next.Generation = next.Intent.TargetGeneration
		advanceRecord(&next)
		committed, transitionErr := persistTransition(control, record, next)
		err = transitionErr
		if committed {
			record = next
		}
	}
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return nil, joinUnlock(errors.Join(err, guardErr), writer)
	}
	lease := &Lease{control: control, lock: writer, record: record}
	lease.self = lease
	return lease, nil
}

func abortDrainingBound(control controlPlane, expected Record, expectedOwner Owner) (Receipt, error) {
	ctx, cancel := context.WithTimeout(context.Background(), defaultLockWait)
	defer cancel()
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return Receipt{}, err
	}
	record, exists, err := readRecord(control)
	if err == nil && !exists {
		err = fail(CodeRecoveryRequired, errors.New("draining record is unavailable"))
	}
	if err == nil && matchingAbortReceipt(record, expected) {
		receipt := cloneReceipt(*record.Receipt)
		return receipt, joinUnlock(nil, guard)
	}
	if err == nil {
		err = requireDrainer(record, control, expected, expectedOwner)
	}
	var receipt Receipt
	if err == nil {
		now := clockNow()
		receipt = Receipt{
			OperationID: record.Intent.OperationID, Generation: record.Generation,
			Outcome: OutcomeAborted, Resources: cloneStrings(record.Intent.Resources), FinishedAt: now,
		}
		next := Record{
			Schema: recordSchema, State: StateOpen, Generation: record.Generation,
			Revision: record.Revision + 1, UpdatedAt: now, Receipt: &receipt,
		}
		_, err = persistTransition(control, record, next)
	}
	return cloneReceipt(receipt), errors.Join(err, joinUnlock(nil, guard))
}

func matchingAbortReceipt(record Record, expected Record) bool {
	return record.State == StateOpen && record.Receipt != nil &&
		record.Receipt.OperationID == expected.Intent.OperationID &&
		record.Receipt.Generation == expected.Intent.BaseGeneration &&
		record.Receipt.Outcome == OutcomeAborted &&
		sameStrings(record.Receipt.Resources, expected.Intent.Resources)
}

// RecoverExclusive takes over only after the recorded owner is confirmed dead.
// It preserves the original ExpiresAt. A post-mutation operation past that
// deadline remains RECOVERY_REQUIRED and requires manual restoration.
func RecoverExclusive(
	ctx context.Context,
	scope Scope,
	operationID string,
	generation uint64,
) (*Lease, error) {
	if ctx == nil || !validOperationID(operationID) || generation == 0 {
		return nil, fail(CodeInvalid, nil)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	record, err := preflightRecovery(ctx, control, operationID, generation)
	if err != nil {
		return nil, err
	}
	deadline := clockNow().Add(MaxDrainTimeout)
	if record.Intent != nil && clockNow().Before(record.Intent.ExpiresAt) && record.Intent.ExpiresAt.Before(deadline) {
		deadline = record.Intent.ExpiresAt
	}
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	writer, err := acquirePlaneLock(ctx, control, control.writersPath, true, deadline)
	if err != nil {
		if IsCode(err, CodeActive) {
			err = fail(CodeWriterDrainTimeout, err)
		}
		return nil, err
	}
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return nil, joinUnlock(err, writer)
	}
	var exists bool
	record, exists, err = readRecord(control)
	if err == nil && !exists {
		err = fail(CodeStateCorrupt, errors.New("maintenance record disappeared"))
	}
	if err == nil {
		record, err = assessRecovery(control, record, operationID, generation)
	}
	owner, ownerErr := currentOwner()
	if err == nil && ownerErr != nil {
		err = fail(CodeLockFailed, ownerErr)
	}
	if err == nil {
		next := cloneRecord(record)
		next.Intent.Owner = owner
		next.Intent.TransferPending = false
		if next.Intent.MutationStarted != nil {
			next.State = StateRestoring
			next.Intent.Canary = nil
		} else if next.State == StateRecoveryRequired {
			next.State = StateMaintenance
		}
		advanceRecord(&next)
		committed, transitionErr := persistTransition(control, record, next)
		err = transitionErr
		if committed {
			record = next
		}
	}
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return nil, joinUnlock(errors.Join(err, guardErr), writer)
	}
	lease := &Lease{control: control, lock: writer, record: record, recovery: true}
	lease.self = lease
	return lease, nil
}

func preflightRecovery(
	ctx context.Context,
	control controlPlane,
	operationID string,
	generation uint64,
) (Record, error) {
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return Record{}, err
	}
	record, exists, err := readRecord(control)
	if err == nil && !exists {
		err = fail(CodeStateCorrupt, errors.New("maintenance record is missing"))
	}
	if err == nil {
		record, err = assessRecovery(control, record, operationID, generation)
	}
	return record, errors.Join(err, joinUnlock(nil, guard))
}

func assessRecovery(
	control controlPlane,
	record Record,
	operationID string,
	generation uint64,
) (Record, error) {
	manual, err := requireRecoverable(record, control, operationID, generation, clockNow())
	if err != nil {
		return Record{}, err
	}
	if !manual {
		return record, nil
	}
	if record.State != StateRecoveryRequired {
		next := cloneRecord(record)
		next.State = StateRecoveryRequired
		next.Generation = next.Intent.TargetGeneration
		now := clockNow()
		next.Intent.Recovery = &Recovery{
			Code: CodeRecoveryRequired, Cause: "original maintenance deadline expired", MarkedAt: now,
		}
		advanceRecord(&next)
		committed, err := persistTransition(control, record, next)
		if err != nil {
			return Record{}, err
		}
		if committed {
			record = next
		}
	}
	return record, fail(CodeRecoveryRequired, errors.New("original maintenance deadline expired"))
}

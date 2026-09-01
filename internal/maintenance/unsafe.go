package maintenance

import (
	"context"
	"errors"
)

// MarkRecoveryRequired is the only safe terminal for an UNKNOWN result after
// exclusive entry. It durably keeps admission closed, records a bounded cause,
// and then releases the process writer lock. The caller must stop the owner
// process before another process can call RecoverExclusive.
func (lease *Lease) MarkRecoveryRequired(code, cause string) error {
	if lease == nil || !validRecoveryCode(code) || !validRecoveryCause(cause) {
		return fail(CodeInvalid, errors.New("recovery metadata is invalid"))
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.closed {
		return fail(CodeRecoveryRequired, errors.New("maintenance lease is closed"))
	}
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return err
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
	if err == nil && record.State != StateMaintenance && record.State != StateRestoring {
		err = fail(CodeRecoveryRequired, errors.New("maintenance state cannot become recovery-required"))
	}
	committed := false
	if err == nil {
		next := cloneRecord(record)
		next.State = StateRecoveryRequired
		next.Generation = next.Intent.TargetGeneration
		next.Intent.TransferPending = false
		next.Intent.Recovery = &Recovery{Code: code, Cause: cause, MarkedAt: clockNow()}
		advanceRecord(&next)
		committed, err = persistTransition(lease.control, record, next)
		if committed {
			lease.record = next
			lease.closed = true
		}
	}
	guardErr := joinUnlock(nil, guard)
	if !committed && !IsCode(err, CodeDurabilityUnknown) {
		return errors.Join(err, guardErr)
	}
	lease.closed = true
	return errors.Join(err, guardErr, lease.releaseWriterLocked())
}

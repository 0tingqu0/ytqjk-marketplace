package maintenance

import (
	"context"
	"crypto/subtle"
	"errors"
	"os"
	"sync"
)

// CanaryLease is a process-local, one-use REOPENING lease. It must not be
// copied. The exclusive writer lock remains held until Complete or an
// ambiguous durability result closes the lease fail-closed.
type CanaryLease struct {
	mu      sync.Mutex
	self    *CanaryLease
	control controlPlane
	lock    heldLock
	record  Record
	token   *canaryToken
	pending *completionPending
	closed  bool
}

// BeginReopening transfers ownership to one already-running target process.
// The out-of-band capability is hashed before the REOPENING record is written.
func (lease *Lease) BeginReopening(childPID int, options CanaryOptions) error {
	_, err := lease.BeginReopeningResult(childPID, options)
	return err
}

// BeginReopeningResult reports whether the ownership transfer was durably
// committed. A committed transfer remains authoritative even when releasing
// the parent process lock returns an error; callers must not kill the child in
// that case.
func (lease *Lease) BeginReopeningResult(childPID int, options CanaryOptions) (bool, error) {
	if lease == nil || childPID <= 0 {
		return false, fail(CodeInvalid, nil)
	}
	capability := append([]byte(nil), options.Capability...)
	options.Capability = capability
	defer clear(capability)
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.self != lease {
		return false, fail(CodeInvalid, errors.New("maintenance lease was copied"))
	}
	if lease.closed {
		return false, fail(CodeRecoveryRequired, errors.New("maintenance lease is closed"))
	}
	alive, err := processAlive(childPID)
	if err != nil || !alive {
		return false, fail(CodeLockFailed, errors.Join(err, os.ErrProcessDone))
	}
	identity, err := processIdentity(childPID)
	if err != nil {
		return false, fail(CodeLockFailed, err)
	}
	child := Owner{PID: childPID, Identity: identity}
	committed, err := lease.updateOwned(func(next *Record) error {
		now := clockNow()
		if next.Intent == nil || next.Intent.MutationStarted == nil || next.Intent.TransferPending ||
			next.Intent.Canary != nil || (next.State != StateMaintenance && next.State != StateRestoring) {
			return fail(CodeRecoveryRequired, errors.New("maintenance operation cannot begin reopening"))
		}
		if err := validateCanaryOptions(options, next.Intent, now); err != nil {
			return err
		}
		if (next.State == StateMaintenance && options.ExpectedOutcome != OutcomeSucceeded) ||
			(next.State == StateRestoring && options.ExpectedOutcome != OutcomeRolledBack) {
			return fail(CodeInvalid, errors.New("canary outcome does not match maintenance state"))
		}
		canary := &Canary{
			Owner: child, CapabilitySHA256: capabilitySHA256(options.Capability),
			PlanSHA256: options.PlanSHA256, SnapshotManifestSHA256: options.SnapshotManifestSHA256,
			TargetBinarySHA256: options.TargetBinarySHA256, TargetVersion: options.TargetVersion,
			Port: options.Port, Attempt: options.Attempt, ExpectedOutcome: options.ExpectedOutcome,
			FallbackOutcome: options.FallbackOutcome,
			Deadline:        options.Deadline.UTC(),
		}
		next.State = StateReopening
		next.Intent.Owner = child
		next.Intent.TransferPending = true
		next.Intent.Recovery = nil
		next.Intent.Canary = canary
		binding, bindingErr := canaryBaseBindingSHA256(lease.control, next.Intent, canary)
		if bindingErr != nil {
			return bindingErr
		}
		canary.BaseBindingSHA256 = binding
		return nil
	})
	if !committed {
		return false, err
	}
	lease.closed = true
	unlockErr := lease.releaseWriterLocked()
	if unlockErr != nil {
		err = errors.Join(err, fail(CodeRecoveryRequired, errors.New("reopening transfer unlock failed")), unlockErr)
	}
	return true, err
}

// ClaimCanary authenticates the current target process and consumes the
// persisted transfer exactly once.
func ClaimCanary(
	ctx context.Context,
	scope Scope,
	operationID string,
	generation uint64,
	capability []byte,
) (*CanaryLease, error) {
	if ctx == nil || !validOperationID(operationID) || generation == 0 ||
		len(capability) < 32 || len(capability) > 256 {
		return nil, fail(CodeInvalid, nil)
	}
	capabilityCopy := append([]byte(nil), capability...)
	capabilityDigest := capabilitySHA256(capabilityCopy)
	defer clear(capabilityCopy)
	control, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	writer, err := acquirePlaneLock(ctx, control, control.writersPath, true, lockDeadline(ctx))
	if err != nil {
		return nil, err
	}
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return nil, joinUnlock(err, writer)
	}
	record, exists, err := readRecord(control)
	owner, ownerErr := currentOwner()
	if err == nil && (!exists || ownerErr != nil) {
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("reopening record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireCanaryRecord(record, control, operationID, generation, owner, true, false)
	}
	if err == nil {
		observed := []byte(capabilityDigest)
		expected := []byte(record.Intent.Canary.CapabilitySHA256)
		if subtle.ConstantTimeCompare(observed, expected) != 1 {
			err = fail(CodeRecoveryRequired, errors.New("canary capability does not match"))
		}
	}
	if err == nil {
		next := cloneRecord(record)
		next.Intent.TransferPending = false
		advanceRecord(&next)
		err = refreshCanaryActiveBinding(&next)
		if err == nil {
			committed, transitionErr := persistTransition(control, record, next)
			err = transitionErr
			if committed {
				record = next
			}
		}
	}
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return nil, joinUnlock(errors.Join(err, guardErr), writer)
	}
	lease := &CanaryLease{control: control, lock: writer, record: record}
	lease.self = lease
	lease.token = &canaryToken{lease: lease}
	return lease, nil
}

// MarkReady durably binds the target's health receipt before OPEN is allowed.
func (lease *CanaryLease) MarkReady(readyReceiptSHA256 string) error {
	if lease == nil || !validSHA256(readyReceiptSHA256) {
		return fail(CodeInvalid, nil)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.requireUsableLocked(); err != nil {
		return err
	}
	_, err := lease.updateOwned(func(next *Record) error {
		canary := next.Intent.Canary
		now := clockNow()
		if canary.ReadyAt != nil || canary.ReadyReceiptSHA256 != "" {
			return fail(CodeInvalid, errors.New("canary ready receipt was already recorded"))
		}
		if !now.Before(canary.Deadline) {
			return fail(CodeRecoveryRequired, errors.New("canary deadline expired before readiness"))
		}
		canary.ReadyReceiptSHA256 = readyReceiptSHA256
		canary.ReadyAt = &now
		readyBinding, bindingErr := canaryReadyBindingSHA256(canary)
		if bindingErr != nil {
			return bindingErr
		}
		canary.ReadyBindingSHA256 = readyBinding
		return nil
	})
	return err
}

func (lease *CanaryLease) updateOwned(update func(*Record) error) (bool, error) {
	guard, err := acquirePlaneLock(
		context.Background(), lease.control, lease.control.guardPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		return false, err
	}
	record, exists, err := readRecord(lease.control)
	owner, ownerErr := currentOwner()
	if err == nil && (!exists || ownerErr != nil) {
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("reopening record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireCanaryRecord(
			record, lease.control, lease.record.Intent.OperationID,
			lease.record.Intent.TargetGeneration, owner, false, false,
		)
	}
	if err == nil && record.Revision != lease.record.Revision {
		err = fail(CodeRecoveryRequired, errors.New("canary lease revision changed"))
	}
	if err == nil {
		next := cloneRecord(record)
		err = update(&next)
		if err == nil {
			advanceRecord(&next)
			err = refreshCanaryActiveBinding(&next)
			if err == nil {
				committed, transitionErr := persistTransition(lease.control, record, next)
				err = transitionErr
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
	}
	return false, errors.Join(err, joinUnlock(nil, guard))
}

func expiredCanaryRecord(record Record) Record {
	next := cloneRecord(record)
	next.State = StateRecoveryRequired
	next.Intent.TransferPending = false
	next.Intent.Recovery = &Recovery{
		Code: CodeRecoveryRequired, Cause: "canary deadline expired before completion", MarkedAt: clockNow(),
	}
	advanceRecord(&next)
	return next
}

func (lease *CanaryLease) requireUsableLocked() error {
	if lease.self != lease {
		return fail(CodeInvalid, errors.New("canary lease was copied"))
	}
	if lease.closed {
		return fail(CodeRecoveryRequired, errors.New("canary lease is closed"))
	}
	return nil
}

func (lease *CanaryLease) releaseWriterLocked() error {
	err := joinUnlock(nil, lease.lock)
	lease.lock = heldLock{}
	return err
}

func requireCanaryRecord(
	record Record,
	control controlPlane,
	operationID string,
	generation uint64,
	owner Owner,
	transferPending bool,
	allowExpired bool,
) error {
	if record.State != StateReopening || record.Intent == nil || record.Intent.Canary == nil ||
		record.Intent.OperationID != operationID || record.Intent.TargetGeneration != generation ||
		record.Intent.TransferPending != transferPending || !ownerEqual(record.Intent.Owner, owner) ||
		!ownerEqual(record.Intent.Canary.Owner, owner) || !sameStrings(record.Intent.Resources, control.resources) {
		return fail(CodeRecoveryRequired, errors.New("canary ownership does not match"))
	}
	if !allowExpired && !clockNow().Before(record.Intent.Canary.Deadline) {
		return fail(CodeRecoveryRequired, errors.New("canary deadline expired"))
	}
	binding, err := canaryBaseBindingSHA256(control, record.Intent, record.Intent.Canary)
	if err != nil {
		return err
	}
	if binding != record.Intent.Canary.BaseBindingSHA256 {
		return fail(CodeStateCorrupt, errors.New("canary binding does not match"))
	}
	if record.Intent.Canary.ReadyAt != nil {
		readyBinding, readyErr := canaryReadyBindingSHA256(record.Intent.Canary)
		if readyErr != nil || readyBinding != record.Intent.Canary.ReadyBindingSHA256 {
			return fail(CodeStateCorrupt, errors.Join(errors.New("canary ready binding does not match"), readyErr))
		}
	}
	return nil
}

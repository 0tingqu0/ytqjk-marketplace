package maintenance

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Drainer is a process-local, owner-bound handle for one persisted DRAINING
// transition. It must not be copied or serialized.
type Drainer struct {
	mu      sync.Mutex
	self    *Drainer
	control controlPlane
	record  Record
	owner   Owner
	receipt *Receipt
	closed  bool
}

// BeginDrain closes admission by durably recording DRAINING. It deliberately
// does not wait for existing shared writers, so the controller can cancel and
// join them before AwaitExclusive.
func BeginDrain(ctx context.Context, scope Scope, options ExclusiveOptions) (*Drainer, error) {
	duration, drain, err := validateExclusiveOptions(ctx, options)
	if err != nil {
		return nil, err
	}
	if sharedTokenFromContext(ctx) != nil {
		return nil, fail(CodeInvalid, errors.New("exclusive drain cannot be nested in a shared workflow"))
	}
	control, err := normalizeScope(scope)
	if err != nil {
		return nil, err
	}
	guard, err := acquirePlaneLock(ctx, control, control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return nil, err
	}
	record, err := loadOrInitialize(control)
	if err == nil {
		err = activeStateError(record, clockNow())
	}
	owner, ownerErr := currentOwner()
	if err == nil && ownerErr != nil {
		err = fail(CodeLockFailed, ownerErr)
	}
	if err == nil {
		now := clockNow()
		expiresAt := now.Add(duration)
		drainDeadline := now.Add(drain)
		if drainDeadline.After(expiresAt.Add(-RecoveryReserve)) {
			drainDeadline = expiresAt.Add(-RecoveryReserve)
		}
		next := Record{
			Schema: recordSchema, State: StateDraining, Generation: record.Generation,
			Revision: record.Revision + 1, UpdatedAt: now,
			Intent: &Intent{
				OperationID: options.OperationID, Purpose: options.Purpose,
				Resources: cloneStrings(control.resources), Owner: owner,
				BaseGeneration: record.Generation, TargetGeneration: record.Generation + 1,
				StartedAt: now, UpdatedAt: now, ExpiresAt: expiresAt, DrainDeadline: drainDeadline,
			},
		}
		committed, transitionErr := persistTransition(control, record, next)
		err = transitionErr
		if committed {
			record = next
		}
	}
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return nil, errors.Join(err, guardErr)
	}
	drainer := &Drainer{control: control, record: record, owner: owner}
	drainer.self = drainer
	return drainer, nil
}

func (drainer *Drainer) OperationID() string {
	if drainer == nil {
		return ""
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return ""
	}
	return drainer.record.Intent.OperationID
}

func (drainer *Drainer) Generation() uint64 {
	if drainer == nil {
		return 0
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return 0
	}
	return drainer.record.Intent.TargetGeneration
}

func (drainer *Drainer) DrainDeadline() time.Time {
	if drainer == nil {
		return time.Time{}
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return time.Time{}
	}
	return drainer.record.Intent.DrainDeadline
}

func (drainer *Drainer) ExpiresAt() time.Time {
	if drainer == nil {
		return time.Time{}
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return time.Time{}
	}
	return drainer.record.Intent.ExpiresAt
}

// AwaitExclusive waits only until the immutable DrainDeadline, then enters
// MAINTENANCE. A wait failure aborts DRAINING and reopens the base generation.
func AwaitExclusive(ctx context.Context, drainer *Drainer) (*Lease, error) {
	if ctx == nil || drainer == nil {
		return nil, fail(CodeInvalid, nil)
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return nil, fail(CodeInvalid, errors.New("drainer was copied"))
	}
	if drainer.closed {
		return nil, fail(CodeRecoveryRequired, errors.New("drainer is closed"))
	}
	if bindingErr := drainer.checkBinding(ctx); bindingErr != nil {
		receipt, abortErr := abortDrainingBound(drainer.control, drainer.record, drainer.owner)
		if abortErr == nil {
			cached := cloneReceipt(receipt)
			drainer.receipt = &cached
			drainer.closed = true
		}
		return nil, errors.Join(bindingErr, abortErr)
	}
	writer, err := acquirePlaneLock(
		ctx, drainer.control, drainer.control.writersPath, true, drainer.record.Intent.DrainDeadline,
	)
	if err != nil {
		receipt, abortErr := abortDrainingBound(drainer.control, drainer.record, drainer.owner)
		if abortErr == nil {
			cached := cloneReceipt(receipt)
			drainer.receipt = &cached
			drainer.closed = true
		}
		if IsCode(err, CodeActive) || errors.Is(ctx.Err(), context.Canceled) ||
			errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fail(CodeWriterDrainTimeout, err)
		}
		return nil, errors.Join(err, abortErr)
	}
	lease, err := enterMaintenance(drainer.control, writer, drainer.record)
	if err != nil {
		if !IsCode(err, CodeDurabilityUnknown) {
			receipt, abortErr := abortDrainingBound(drainer.control, drainer.record, drainer.owner)
			if abortErr == nil {
				cached := cloneReceipt(receipt)
				drainer.receipt = &cached
				drainer.closed = true
			}
			err = errors.Join(err, abortErr)
		} else {
			drainer.closed = true
		}
		return nil, err
	}
	drainer.closed = true
	return lease, nil
}

// Abort idempotently reopens admission at the base generation while the
// operation is still DRAINING and owned by this exact process instance.
func (drainer *Drainer) Abort() (Receipt, error) {
	if drainer == nil {
		return Receipt{}, fail(CodeInvalid, nil)
	}
	drainer.mu.Lock()
	defer drainer.mu.Unlock()
	if drainer.self != drainer {
		return Receipt{}, fail(CodeInvalid, errors.New("drainer was copied"))
	}
	if drainer.receipt != nil {
		return cloneReceipt(*drainer.receipt), nil
	}
	if drainer.closed {
		return Receipt{}, fail(CodeRecoveryRequired, errors.New("drainer is closed"))
	}
	receipt, err := abortDrainingBound(drainer.control, drainer.record, drainer.owner)
	if err == nil {
		cached := cloneReceipt(receipt)
		drainer.receipt = &cached
		drainer.closed = true
	}
	return cloneReceipt(receipt), err
}

func (drainer *Drainer) checkBinding(ctx context.Context) error {
	guard, err := acquirePlaneLock(ctx, drainer.control, drainer.control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return err
	}
	record, exists, err := readRecord(drainer.control)
	if err == nil && !exists {
		err = fail(CodeStateCorrupt, errors.New("draining record disappeared"))
	}
	if err == nil {
		err = requireDrainer(record, drainer.control, drainer.record, drainer.owner)
	}
	return errors.Join(err, joinUnlock(nil, guard))
}

func requireDrainer(record Record, control controlPlane, expected Record, owner Owner) error {
	if !ownerEqual(owner, expected.Intent.Owner) || record.State != StateDraining ||
		record.Revision != expected.Revision || record.Generation != expected.Generation ||
		record.Intent == nil || record.Intent.OperationID != expected.Intent.OperationID ||
		record.Intent.Purpose != expected.Intent.Purpose || !ownerEqual(record.Intent.Owner, owner) ||
		record.Intent.BaseGeneration != expected.Intent.BaseGeneration ||
		record.Intent.TargetGeneration != expected.Intent.TargetGeneration ||
		!record.Intent.StartedAt.Equal(expected.Intent.StartedAt) ||
		!record.Intent.UpdatedAt.Equal(expected.Intent.UpdatedAt) ||
		!record.Intent.ExpiresAt.Equal(expected.Intent.ExpiresAt) ||
		!record.Intent.DrainDeadline.Equal(expected.Intent.DrainDeadline) ||
		!sameStrings(record.Intent.Resources, expected.Intent.Resources) ||
		!sameStrings(record.Intent.Resources, control.resources) {
		return fail(CodeRecoveryRequired, errors.New("drainer binding does not match persisted operation"))
	}
	current, err := currentOwner()
	if err != nil {
		return fail(CodeLockFailed, err)
	}
	if !ownerEqual(current, owner) {
		return fail(CodeRecoveryRequired, errors.New("drainer owner process identity changed"))
	}
	return nil
}

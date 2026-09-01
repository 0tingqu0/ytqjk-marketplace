package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"time"
)

// Permit is a process-local shared writer permit. It must not be copied. One
// outer workflow owns it from admission through its final side effect; lower
// layers receive its context token instead of acquiring nested permits.
type Permit struct {
	mu         sync.Mutex
	self       *Permit
	control    controlPlane
	lock       heldLock
	fence      Fence
	token      *sharedToken
	committing bool
	closed     bool
}

// Lease is a process-local exclusive maintenance lease. It must not be copied.
type Lease struct {
	mu       sync.Mutex
	self     *Lease
	control  controlPlane
	lock     heldLock
	record   Record
	recovery bool
	pending  *completionPending
	closed   bool
}

func AcquireShared(ctx context.Context, scope Scope) (*Permit, error) {
	if ctx == nil {
		return nil, fail(CodeInvalid, errors.New("context is required"))
	}
	if sharedTokenFromContext(ctx) != nil {
		return nil, fail(CodeInvalid, errors.New("nested shared acquisition is forbidden"))
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
	var writer heldLock
	if err == nil {
		writer, err = acquirePlaneLock(ctx, control, control.writersPath, false, lockDeadline(ctx))
	}
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return nil, joinUnlock(errors.Join(err, guardErr), writer)
	}
	permit := &Permit{
		control: control,
		lock:    writer,
		fence: Fence{
			Generation: record.Generation, Revision: record.Revision,
			Resources: cloneStrings(control.resources), AcquiredAt: clockNow(),
		},
	}
	permit.self = permit
	permit.token = &sharedToken{permit: permit}
	return permit, nil
}

func (permit *Permit) Fence() Fence {
	if permit == nil {
		return Fence{}
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.self != permit {
		return Fence{}
	}
	return cloneFence(permit.fence)
}

func (permit *Permit) CheckFence(ctx context.Context) error {
	if permit == nil || ctx == nil {
		return fail(CodeInvalid, nil)
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.self != permit {
		return fail(CodeInvalid, errors.New("shared permit was copied"))
	}
	if permit.closed {
		return fail(CodeRecoveryRequired, errors.New("shared permit is closed"))
	}
	return permit.checkFenceLocked(ctx)
}

func (permit *Permit) Commit(action func(Fence) error) (result error) {
	if permit == nil || action == nil {
		return fail(CodeInvalid, nil)
	}
	permit.mu.Lock()
	if permit.self != permit {
		permit.mu.Unlock()
		return fail(CodeInvalid, errors.New("shared permit was copied"))
	}
	if permit.closed {
		permit.mu.Unlock()
		return fail(CodeRecoveryRequired, errors.New("shared permit is closed"))
	}
	if permit.committing {
		permit.mu.Unlock()
		return fail(CodeActive, errors.New("shared permit commit is in progress"))
	}
	fenceErr := permit.checkFenceLocked(context.Background())
	if fenceErr != nil {
		permit.closed = true
		unlockErr := joinUnlock(nil, permit.lock)
		permit.lock = heldLock{}
		permit.mu.Unlock()
		return errors.Join(fenceErr, unlockErr)
	}
	permit.committing = true
	fence := cloneFence(permit.fence)
	permit.mu.Unlock()
	defer func() {
		panicValue := recover()
		permit.mu.Lock()
		finalFenceErr := permit.checkFenceLocked(context.Background())
		permit.committing = false
		permit.closed = true
		unlockErr := joinUnlock(nil, permit.lock)
		permit.lock = heldLock{}
		permit.mu.Unlock()
		if panicValue != nil {
			panic(panicValue)
		}
		if cleanupErr := errors.Join(finalFenceErr, unlockErr); cleanupErr != nil {
			result = fail(
				CodeCommitResultUnknown,
				errors.Join(errors.New("shared action may have executed before final fence failed"), result, cleanupErr),
			)
		}
	}()
	result = action(fence)
	return result
}

func (permit *Permit) Release() error {
	if permit == nil {
		return nil
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.self != permit {
		return fail(CodeInvalid, errors.New("shared permit was copied"))
	}
	if permit.closed {
		return nil
	}
	if permit.committing {
		return fail(CodeActive, errors.New("shared permit commit is in progress"))
	}
	permit.closed = true
	err := joinUnlock(nil, permit.lock)
	permit.lock = heldLock{}
	return err
}

func (permit *Permit) checkFenceLocked(ctx context.Context) error {
	guard, err := acquirePlaneLock(ctx, permit.control, permit.control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return err
	}
	record, exists, err := readRecord(permit.control)
	if err == nil && !exists {
		err = fail(CodeStateCorrupt, errors.New("maintenance record disappeared"))
	}
	if err == nil {
		switch {
		case record.State == StateOpen && record.Generation == permit.fence.Generation:
			permit.fence.OperationID = ""
		case record.State == StateDraining && record.Generation == permit.fence.Generation &&
			record.Revision > permit.fence.Revision:
			permit.fence.OperationID = record.Intent.OperationID
		default:
			err = fail(CodeGenerationConflict, errors.New("shared permit generation is no longer admissible"))
		}
	}
	return errors.Join(err, joinUnlock(nil, guard))
}

// BeginExclusive is the compatibility convenience for BeginDrain followed by
// AwaitExclusive. Controllers that must cancel and join workers between those
// phases should call them separately.
func BeginExclusive(ctx context.Context, scope Scope, options ExclusiveOptions) (*Lease, error) {
	drainer, err := BeginDrain(ctx, scope, options)
	if err != nil {
		return nil, err
	}
	return AwaitExclusive(ctx, drainer)
}

func ClaimExclusive(ctx context.Context, scope Scope, operationID string, generation uint64) (*Lease, error) {
	if ctx == nil || !validOperationID(operationID) || generation == 0 {
		return nil, fail(CodeInvalid, nil)
	}
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
		err = errors.Join(fail(CodeRecoveryRequired, errors.New("pending transfer record is unavailable")), ownerErr)
	}
	if err == nil {
		err = requireOwned(record, control, operationID, generation, owner)
	}
	if err == nil && (record.State != StateMaintenance || !record.Intent.TransferPending ||
		!clockNow().Before(record.Intent.ExpiresAt)) {
		err = fail(CodeRecoveryRequired, errors.New("maintenance transfer is not claimable"))
	}
	if err == nil {
		next := cloneRecord(record)
		next.Revision++
		next.UpdatedAt = clockNow()
		next.Intent.UpdatedAt = next.UpdatedAt
		next.Intent.TransferPending = false
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

func acquireStandaloneLock(ctx context.Context, path string, exclusive bool, deadline time.Time) (heldLock, error) {
	file, err := openLockFileNoFollow(path)
	if err != nil {
		return heldLock{}, fail(CodeStateCorrupt, errors.Join(errors.New("maintenance lock is missing or unsafe"), err))
	}
	unlock, err := lockResource(ctx, file, exclusive, deadline)
	if err != nil {
		if errors.Is(err, errLockContended) {
			return heldLock{}, fail(CodeActive, err)
		}
		return heldLock{}, fail(CodeLockFailed, err)
	}
	return heldLock{path: path, unlock: unlock}, nil
}

func acquirePlaneLock(
	ctx context.Context,
	control controlPlane,
	path string,
	exclusive bool,
	deadline time.Time,
) (heldLock, error) {
	name := filepath.Base(path)
	if canonicalKey(filepath.Dir(path)) != canonicalKey(control.directory) ||
		(canonicalKey(path) != canonicalKey(control.guardPath) &&
			canonicalKey(path) != canonicalKey(control.writersPath)) {
		return heldLock{}, fail(CodeStateCorrupt, errors.New("maintenance lock is outside the bound control root"))
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return heldLock{}, err
	}
	file, err := openRootRegularFileNoFollow(bound.directory, name, true)
	if err != nil {
		return heldLock{}, errors.Join(
			fail(CodeStateCorrupt, errors.Join(errors.New("maintenance lock is missing or unsafe"), err)),
			bound.close(),
		)
	}
	expectedID := control.guardID
	if canonicalKey(path) == canonicalKey(control.writersPath) {
		expectedID = control.writersID
	}
	identity, identityErr := fileHandleIdentity(file)
	if identityErr != nil || identity != expectedID {
		return heldLock{}, errors.Join(
			fail(CodeStateCorrupt, errors.Join(errors.New("maintenance lock identity changed"), identityErr)),
			file.Close(), bound.close(),
		)
	}
	if err := bound.close(); err != nil {
		return heldLock{}, errors.Join(fail(CodeLockFailed, err), file.Close())
	}
	unlock, err := lockResource(ctx, file, exclusive, deadline)
	if err != nil {
		if errors.Is(err, errLockContended) {
			return heldLock{}, fail(CodeActive, err)
		}
		return heldLock{}, fail(CodeLockFailed, err)
	}
	lock := heldLock{path: path, unlock: unlock}
	if err := verifyControlDirectory(control); err != nil {
		return heldLock{}, joinUnlock(err, lock)
	}
	return lock, nil
}

func validateExclusiveOptions(ctx context.Context, options ExclusiveOptions) (time.Duration, time.Duration, error) {
	if ctx == nil || !validOperationID(options.OperationID) || !validPurpose(options.Purpose) {
		return 0, 0, fail(CodeInvalid, nil)
	}
	duration := options.Duration
	if duration == 0 {
		duration = MaxExclusiveDuration
	}
	drain := options.DrainTimeout
	if drain == 0 {
		drain = MaxDrainTimeout
	}
	if duration <= RecoveryReserve || duration > MaxExclusiveDuration || drain <= 0 || drain > MaxDrainTimeout ||
		drain > duration-RecoveryReserve {
		return 0, 0, fail(CodeInvalid, errors.New("maintenance duration or drain timeout is invalid"))
	}
	return duration, drain, nil
}

func lockDeadline(ctx context.Context) time.Time {
	deadline := time.Now().Add(defaultLockWait)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func cloneFence(fence Fence) Fence {
	fence.Resources = cloneStrings(fence.Resources)
	return fence
}

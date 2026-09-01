package maintenance

import (
	"context"
	"errors"
)

type canaryContextKey struct{}

type canaryToken struct {
	lease *CanaryLease
}

// WithCanary binds an unforgeable process-local token to the target health
// workflow. The token is invalid as soon as the one-use lease closes.
func WithCanary(ctx context.Context, lease *CanaryLease) (context.Context, error) {
	if ctx == nil || lease == nil {
		return nil, fail(CodeInvalid, nil)
	}
	if sharedTokenFromContext(ctx) != nil {
		return nil, fail(CodeInvalid, errors.New("canary cannot be nested in a shared workflow"))
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.requireUsableLocked(); err != nil {
		return nil, err
	}
	if lease.token == nil || lease.token.lease != lease {
		return nil, fail(CodeRecoveryRequired, errors.New("canary token is unavailable"))
	}
	if current := canaryTokenFromContext(ctx); current != nil {
		if current == lease.token {
			return ctx, nil
		}
		return nil, fail(CodeInvalid, errors.New("context already belongs to another canary"))
	}
	if _, err := lease.checkFenceLocked(ctx); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, canaryContextKey{}, lease.token), nil
}

// CanaryFenceFromContext verifies the current process, persisted binding and a
// nested resource subset without acquiring another writer lock.
func CanaryFenceFromContext(ctx context.Context, requested Scope) (CanaryFence, error) {
	if ctx == nil {
		return CanaryFence{}, fail(CodeInvalid, nil)
	}
	token := canaryTokenFromContext(ctx)
	if token == nil || token.lease == nil {
		return CanaryFence{}, fail(CodeInvalid, errors.New("canary workflow token is missing"))
	}
	control, err := normalizeScope(requested)
	if err != nil {
		return CanaryFence{}, err
	}
	lease := token.lease
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if err := lease.requireUsableLocked(); err != nil {
		return CanaryFence{}, err
	}
	if lease.token != token {
		return CanaryFence{}, fail(CodeRecoveryRequired, errors.New("canary workflow token is no longer active"))
	}
	if canonicalKey(control.root) != canonicalKey(lease.control.root) ||
		!sortedSubset(control.resources, lease.control.resources) {
		return CanaryFence{}, fail(CodeInvalid, errors.New("nested canary scope exceeds bound resources"))
	}
	fence, err := lease.checkFenceLocked(ctx)
	if err != nil {
		return CanaryFence{}, err
	}
	fence.Resources = cloneStrings(control.resources)
	return fence, nil
}

func (lease *CanaryLease) checkFenceLocked(ctx context.Context) (CanaryFence, error) {
	guard, err := acquirePlaneLock(ctx, lease.control, lease.control.guardPath, true, lockDeadline(ctx))
	if err != nil {
		return CanaryFence{}, err
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
	guardErr := joinUnlock(nil, guard)
	if err != nil || guardErr != nil {
		return CanaryFence{}, errors.Join(err, guardErr)
	}
	canary := record.Intent.Canary
	return CanaryFence{
		OperationID: record.Intent.OperationID, Generation: record.Intent.TargetGeneration,
		Revision: record.Revision, Resources: cloneStrings(record.Intent.Resources),
		BaseBindingSHA256: canary.BaseBindingSHA256, ReadyBindingSHA256: canary.ReadyBindingSHA256,
		ActiveBindingSHA256:    canary.ActiveBindingSHA256,
		PlanSHA256:             canary.PlanSHA256,
		SnapshotManifestSHA256: canary.SnapshotManifestSHA256,
		TargetBinarySHA256:     canary.TargetBinarySHA256, TargetVersion: canary.TargetVersion,
		Port: canary.Port, Owner: canary.Owner, Attempt: canary.Attempt,
		ExpectedOutcome: canary.ExpectedOutcome, FallbackOutcome: canary.FallbackOutcome,
		Deadline: canary.Deadline,
	}, nil
}

func canaryTokenFromContext(ctx context.Context) *canaryToken {
	if ctx == nil {
		return nil
	}
	token, _ := ctx.Value(canaryContextKey{}).(*canaryToken)
	return token
}

package maintenance

import (
	"context"
	"errors"
)

type sharedContextKey struct{}

type sharedToken struct {
	permit *Permit
}

// WithShared binds an unforgeable process-local token to ctx. The outer caller
// must keep permit alive for the entire workflow and release or commit it only
// after all nested side effects finish.
func WithShared(ctx context.Context, permit *Permit) (context.Context, error) {
	if ctx == nil || permit == nil {
		return nil, fail(CodeInvalid, nil)
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.self != permit {
		return nil, fail(CodeInvalid, errors.New("shared permit was copied"))
	}
	if permit.committing {
		return nil, fail(CodeActive, errors.New("shared permit commit is in progress"))
	}
	if permit.closed || permit.token == nil || permit.token.permit != permit {
		return nil, fail(CodeRecoveryRequired, errors.New("shared permit is unavailable"))
	}
	if current := sharedTokenFromContext(ctx); current != nil {
		if current == permit.token {
			return ctx, nil
		}
		return nil, fail(CodeInvalid, errors.New("context already belongs to another shared workflow"))
	}
	if err := permit.checkFenceLocked(ctx); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, sharedContextKey{}, permit.token), nil
}

// SharedFenceFromContext validates a nested resource scope without acquiring a
// second OS lock. requested must use the same ControlRoot and may only narrow
// the outer permit's deterministically sorted resource set.
func SharedFenceFromContext(ctx context.Context, requested Scope) (Fence, error) {
	if ctx == nil {
		return Fence{}, fail(CodeInvalid, nil)
	}
	token := sharedTokenFromContext(ctx)
	if token == nil || token.permit == nil {
		return Fence{}, fail(CodeInvalid, errors.New("shared workflow token is missing"))
	}
	control, err := normalizeScope(requested)
	if err != nil {
		return Fence{}, err
	}
	permit := token.permit
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.self != permit {
		return Fence{}, fail(CodeInvalid, errors.New("shared permit was copied"))
	}
	if permit.closed || permit.token != token {
		return Fence{}, fail(CodeRecoveryRequired, errors.New("shared workflow token is no longer active"))
	}
	if canonicalKey(control.root) != canonicalKey(permit.control.root) ||
		!sortedSubset(control.resources, permit.fence.Resources) {
		return Fence{}, fail(CodeInvalid, errors.New("nested shared scope exceeds outer resources"))
	}
	if err := permit.checkFenceLocked(ctx); err != nil {
		return Fence{}, err
	}
	fence := cloneFence(permit.fence)
	fence.Resources = cloneStrings(control.resources)
	return fence, nil
}

func sharedTokenFromContext(ctx context.Context) *sharedToken {
	if ctx == nil {
		return nil
	}
	token, _ := ctx.Value(sharedContextKey{}).(*sharedToken)
	return token
}

func sortedSubset(values, superset []string) bool {
	valueIndex := 0
	for _, candidate := range superset {
		if valueIndex < len(values) && values[valueIndex] == candidate {
			valueIndex++
		}
	}
	return valueIndex == len(values)
}

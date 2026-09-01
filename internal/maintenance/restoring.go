package maintenance

import "errors"

// BeginRestoring marks an intentional rollback after mutation has started.
// It preserves the original maintenance deadline and writer ownership.
func (lease *Lease) BeginRestoring() error {
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
		if next.State != StateMaintenance || next.Intent == nil ||
			next.Intent.MutationStarted == nil || next.Intent.TransferPending ||
			next.Intent.Canary != nil {
			return fail(CodeRecoveryRequired, errors.New("maintenance restore cannot start in the current state"))
		}
		next.State = StateRestoring
		return nil
	})
	return err
}

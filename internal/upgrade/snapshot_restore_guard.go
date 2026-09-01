package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const restoreGuardWait = 500 * time.Millisecond

var restoreGuards = struct {
	sync.Mutex
	held map[string]struct{}
}{held: map[string]struct{}{}}

type restoreGuard struct {
	root      string
	key       string
	bootstrap restoreBootstrapRecord
	runtime   *restoreBoundRoot
	upgrade   *restoreBoundRoot
	lock      *os.File
	unlock    func() error
	released  bool
}

func acquireRestoreGuard(plan Plan) (*restoreGuard, error) {
	roots, err := restorePlanRoots(plan)
	if err != nil {
		return nil, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return acquireRestoreGuardRoot(roots[0])
}

func acquireRestoreGuardRoot(runtimeRoot string) (*restoreGuard, error) {
	runtime, err := openRestoreBoundRoot(runtimeRoot, "")
	if err != nil {
		return nil, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	bootstrap, err := readRestoreBootstrap(runtime)
	if err != nil {
		return nil, errors.Join(failure("UPGRADE_RECOVERY_REQUIRED", err), runtime.close())
	}
	upgrade, err := openRestoreBoundChild(runtime, "upgrade", bootstrap.DirectoryIdentity)
	if err != nil {
		return nil, errors.Join(failure("UPGRADE_RECOVERY_REQUIRED", err), runtime.close())
	}
	lock, err := openRestoreRegularAtNoFollow(upgrade.directory, restoreGuardName, true)
	if err != nil {
		return nil, errors.Join(failure("UPGRADE_RECOVERY_REQUIRED", err), upgrade.close(), runtime.close())
	}
	lockIdentity, err := restoreHandleIdentity(lock)
	if err != nil || lockIdentity != bootstrap.GuardIdentity {
		return nil, errors.Join(
			failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore guard identity changed"), err)),
			lock.Close(), upgrade.close(), runtime.close(),
		)
	}
	key, err := restorePathKey(filepath.Join(runtimeRoot, "upgrade", restoreGuardName))
	if err != nil {
		return nil, errors.Join(failure("UPGRADE_RECOVERY_REQUIRED", err), lock.Close(), upgrade.close(), runtime.close())
	}
	restoreGuards.Lock()
	if _, exists := restoreGuards.held[key]; exists {
		restoreGuards.Unlock()
		return nil, errors.Join(
			failure("UPGRADE_RESTORE_IN_PROGRESS", errors.New("restore guard is already held in this process")),
			lock.Close(), upgrade.close(), runtime.close(),
		)
	}
	restoreGuards.held[key] = struct{}{}
	restoreGuards.Unlock()
	unlock, err := lockRestoreGuard(lock, restoreGuardWait)
	if err != nil {
		releaseRestoreGuardKey(key)
		return nil, errors.Join(failure("UPGRADE_RESTORE_IN_PROGRESS", err), upgrade.close(), runtime.close())
	}
	guard := &restoreGuard{
		root: runtimeRoot, key: key, bootstrap: bootstrap,
		runtime: runtime, upgrade: upgrade, lock: lock, unlock: unlock,
	}
	if err := guard.verifyPhysicalIdentity(); err != nil {
		return nil, errors.Join(err, guard.release())
	}
	return guard, nil
}

func (guard *restoreGuard) require(plan Plan) error {
	if guard == nil || guard.released || guard.unlock == nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore guard is not active"))
	}
	roots, err := restorePlanRoots(plan)
	if err != nil || !sameRestorePath(guard.root, roots[0]) {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore guard root mismatch"), err))
	}
	return guard.verifyPhysicalIdentity()
}

func (guard *restoreGuard) requireRuntime(runtimeRoot string) error {
	if guard == nil || guard.released || guard.unlock == nil || !sameRestorePath(guard.root, runtimeRoot) {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore guard is not active for this runtime"))
	}
	return guard.verifyPhysicalIdentity()
}

func (guard *restoreGuard) verifyPhysicalIdentity() error {
	if guard.runtime == nil || guard.upgrade == nil || guard.lock == nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore guard handles are unavailable"))
	}
	if err := guard.runtime.verify(); err != nil || guard.runtime.identity != guard.bootstrap.RuntimeIdentity {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore runtime identity changed"), err))
	}
	if err := guard.upgrade.verify(); err != nil || guard.upgrade.identity != guard.bootstrap.DirectoryIdentity {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore upgrade identity changed"), err))
	}
	identity, err := restoreHandleIdentity(guard.lock)
	if err != nil || identity != guard.bootstrap.GuardIdentity {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore guard handle identity changed"), err))
	}
	current, err := openRestoreRegularAtNoFollow(guard.upgrade.directory, restoreGuardName, true)
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	currentIdentity, identityErr := restoreHandleIdentity(current)
	closeErr := current.Close()
	if identityErr != nil || closeErr != nil || currentIdentity != identity {
		return failure(
			"UPGRADE_RECOVERY_REQUIRED",
			errors.Join(errors.New("restore guard entry identity changed"), identityErr, closeErr),
		)
	}
	return nil
}

func (guard *restoreGuard) release() error {
	if guard == nil || guard.released || guard.unlock == nil {
		return failure("UPGRADE_RESTORE_GUARD_FAILED", errors.New("restore guard release is invalid"))
	}
	guard.released = true
	err := guard.unlock()
	guard.unlock = nil
	guard.lock = nil
	err = errors.Join(err, guard.upgrade.close(), guard.runtime.close())
	guard.upgrade, guard.runtime = nil, nil
	releaseRestoreGuardKey(guard.key)
	if err != nil {
		return failure("UPGRADE_RESTORE_GUARD_FAILED", err)
	}
	return nil
}

func releaseRestoreGuardKey(key string) {
	restoreGuards.Lock()
	delete(restoreGuards.held, key)
	restoreGuards.Unlock()
}

//go:build windows

package upgrade

import (
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lockRestoreGuard(file *os.File, wait time.Duration) (func() error, error) {
	if file == nil {
		return nil, errors.New("restore guard handle is nil")
	}
	var overlapped windows.Overlapped
	deadline := time.Now().Add(wait)
	var err error
	for {
		err = windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() error {
		return errors.Join(windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped), file.Close())
	}, nil
}

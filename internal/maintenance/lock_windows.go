//go:build windows

package maintenance

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

func lockResource(ctx context.Context, file *os.File, exclusive bool, deadline time.Time) (func() error, error) {
	if file == nil {
		return nil, errors.New("maintenance lock handle is nil")
	}
	var err error
	// Lock outside the bootstrap receipt bytes so initialization proof remains
	// readable while this file is used as a coordination lock.
	overlapped := windows.Overlapped{OffsetHigh: 1}
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	for {
		err = windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &overlapped)
		if err == nil {
			break
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			file.Close()
			return nil, err
		}
		if !time.Now().Before(deadline) {
			file.Close()
			return nil, errLockContended
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, errLockContended
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() error {
		return errors.Join(
			windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &overlapped),
			file.Close(),
		)
	}, nil
}

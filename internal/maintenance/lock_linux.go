//go:build linux

package maintenance

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func lockResource(ctx context.Context, file *os.File, exclusive bool, deadline time.Time) (func() error, error) {
	flags := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		flags = unix.LOCK_EX | unix.LOCK_NB
	}
	if file == nil {
		return nil, errors.New("maintenance lock handle is nil")
	}
	descriptor := int(file.Fd())
	var err error
	for {
		err = unix.Flock(descriptor, flags)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
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
		return errors.Join(unix.Flock(descriptor, unix.LOCK_UN), file.Close())
	}, nil
}

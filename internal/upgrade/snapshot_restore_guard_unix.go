//go:build !windows

package upgrade

import (
	"errors"
	"os"
	"syscall"
	"time"
)

func lockRestoreGuard(file *os.File, wait time.Duration) (func() error, error) {
	if file == nil {
		return nil, errors.New("restore guard handle is nil")
	}
	deadline := time.Now().Add(wait)
	var err error
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() error {
		return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}, nil
}

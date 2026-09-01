//go:build !windows

package upgrade

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"
)

func lockOperationGuard(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(operationGuardWait)
	for {
		err = syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(deadline) {
			file.Close()
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return func() error {
		return errors.Join(syscall.Flock(int(file.Fd()), syscall.LOCK_UN), file.Close())
	}, nil
}

func operationProcessAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return true, err
}

func operationProcessIdentity(pid int) (string, error) {
	if pid <= 0 {
		return "", os.ErrProcessDone
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	closing := bytes.LastIndexByte(stat, ')')
	if closing < 0 {
		return "", errors.New("process stat is invalid")
	}
	fields := strings.Fields(string(stat[closing+1:]))
	if len(fields) <= 19 {
		return "", errors.New("process stat is incomplete")
	}
	bootID, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return "", err
	}
	boot := strings.TrimSpace(string(bootID))
	if boot == "" {
		return "", errors.New("boot identity is empty")
	}
	return "linux:" + boot + ":" + fields[19], nil
}

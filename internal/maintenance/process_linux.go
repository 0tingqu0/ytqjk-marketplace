//go:build linux

package maintenance

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func processAlive(pid int) (bool, error) {
	if pid <= 0 {
		return false, nil
	}
	err := unix.Kill(pid, 0)
	if err == nil || errors.Is(err, unix.EPERM) {
		return true, nil
	}
	if errors.Is(err, unix.ESRCH) {
		return false, nil
	}
	return true, err
}

func processIdentity(pid int) (string, error) {
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

//go:build !windows

package upgrade

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openRestoreDirectoryNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func openRestoreDirectoryAtNoFollow(parent *os.File, name string) (*os.File, error) {
	if parent == nil {
		return nil, errors.New("restore parent directory handle is nil")
	}
	descriptor, err := unix.Openat(
		int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

func openRestoreRegularAtNoFollow(directory *os.File, name string, writable bool) (*os.File, error) {
	if directory == nil {
		return nil, errors.New("restore directory handle is nil")
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if writable {
		flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	}
	descriptor, err := unix.Openat(int(directory.Fd()), name, flags, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), name)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(err, file.Close(), errors.New("restore control entry is not regular"))
	}
	return file, nil
}

func restoreHandleIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("restore control handle is nil")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x:%016x", uint64(stat.Dev), stat.Ino), nil
}

func syncRestoreDirectory(directory *os.File) error {
	if directory == nil {
		return errors.New("restore directory handle is nil")
	}
	return directory.Sync()
}

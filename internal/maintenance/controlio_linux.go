//go:build linux

package maintenance

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func directoryIdentity(path string) (string, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(descriptor)
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x:%016x", uint64(stat.Dev), stat.Ino), nil
}

func fileHandleIdentity(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("maintenance file handle is nil")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x:%016x", uint64(stat.Dev), stat.Ino), nil
}

func openRootRegularFileNoFollow(directory *os.File, name string, writable bool) (*os.File, error) {
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
		return nil, errors.Join(err, file.Close(), errors.New("maintenance control file is not regular"))
	}
	return file, nil
}

func openRegularFileNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(err, file.Close(), errors.New("maintenance control file is not regular"))
	}
	return file, nil
}

func openLockFileNoFollow(path string) (*os.File, error) {
	descriptor, err := unix.Open(path, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.Join(err, file.Close(), errors.New("maintenance lock is not regular"))
	}
	return file, nil
}

func syncBoundDirectory(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

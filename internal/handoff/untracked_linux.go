//go:build linux

package handoff

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func writeUntrackedFile(source, root, relative string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("untracked payload source is not a regular file")
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	directoryFDs := []int{rootFD}
	type createdDirectory struct {
		parentFD int
		name     string
	}
	var createdDirectories []createdDirectory
	committed := false
	defer func() {
		if !committed {
			for index := len(createdDirectories) - 1; index >= 0; index-- {
				created := createdDirectories[index]
				_ = unix.Unlinkat(created.parentFD, created.name, unix.AT_REMOVEDIR)
			}
		}
		for _, descriptor := range directoryFDs {
			_ = unix.Close(descriptor)
		}
	}()
	parts := strings.Split(relative, "/")
	parentFD := rootFD
	for _, part := range parts[:len(parts)-1] {
		nextFD, created, err := openOrCreateDirectoryAt(parentFD, part)
		if err != nil {
			return fmt.Errorf("open parent %s: %w", part, err)
		}
		if created {
			createdDirectories = append(createdDirectories, createdDirectory{parentFD: parentFD, name: part})
		}
		directoryFDs = append(directoryFDs, nextFD)
		parentFD = nextFD
	}
	name := parts[len(parts)-1]
	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_EXCL | unix.O_CLOEXEC | unix.O_NOFOLLOW
	outputFD, err := unix.Openat(parentFD, name, flags, uint32(mode.Perm()))
	if err != nil {
		return err
	}
	output := os.NewFile(uintptr(outputFD), name)
	defer func() {
		_ = output.Close()
		if !committed {
			_ = unix.Unlinkat(parentFD, name, 0)
		}
	}()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	if err := unix.Fsync(parentFD); err != nil {
		return err
	}
	committed = true
	return nil
}

func openOrCreateDirectoryAt(parentFD int, name string) (int, bool, error) {
	flags := unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	directoryFD, err := unix.Openat(parentFD, name, flags, 0)
	if err == nil {
		return directoryFD, false, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return -1, false, err
	}
	created := false
	if err := unix.Mkdirat(parentFD, name, 0o755); err == nil {
		created = true
	} else if !errors.Is(err, unix.EEXIST) {
		return -1, false, err
	}
	if err := unix.Fsync(parentFD); err != nil {
		if created {
			_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
		}
		return -1, false, err
	}
	directoryFD, err = unix.Openat(parentFD, name, flags, 0)
	if err != nil && created {
		_ = unix.Unlinkat(parentFD, name, unix.AT_REMOVEDIR)
	}
	return directoryFD, created, err
}

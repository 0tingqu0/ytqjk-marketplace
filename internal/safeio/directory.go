package safeio

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

// SyncTree makes every regular file and directory entry below root durable.
// Symbolic links and non-regular files are rejected.
func SyncTree(root string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sync root must be a real directory")
	}
	var directories []string
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to sync symbolic link: %s", path)
		}
		if entry.IsDir() {
			directories = append(directories, path)
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to sync non-regular file: %s", path)
		}
		return syncRegularFile(path)
	})
	if err != nil {
		return err
	}
	sort.Slice(directories, func(left, right int) bool {
		return len(directories[left]) > len(directories[right])
	})
	for _, directory := range directories {
		if err := syncDirectory(directory); err != nil {
			return err
		}
	}
	return nil
}

// PublishDirectory durably publishes a complete directory without replacing
// an existing target. A committed error means the rename happened but syncing
// the target's parent failed.
func PublishDirectory(source, target string) error {
	if err := SyncTree(source); err != nil {
		return err
	}
	return publishSyncedDirectory(source, target, renameDirectory, syncDirectory)
}

func publishSyncedDirectory(
	source, target string,
	rename func(string, string) error,
	sync func(string) error,
) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("publish source must be a real directory")
	}
	if _, err := os.Lstat(target); err == nil {
		return fmt.Errorf("publish target already exists: %s", target)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rename(source, target); err != nil {
		return err
	}
	if err := sync(filepath.Dir(target)); err != nil {
		return &PostCommitError{Operation: "directory publish", Err: err}
	}
	return nil
}

func syncRegularFile(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return errors.Join(file.Sync(), file.Close())
}

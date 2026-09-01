package upgrade

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	snapshotTreeDirectory byte = 'd'
	snapshotTreeFile      byte = 'f'
)

type snapshotTreeEntry struct {
	path string
	kind byte
	mode os.FileMode
	size int64
}

func snapshotCopyTree(source, destination string) (resultErr error) {
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		return errors.New("snapshot tree destination already exists")
	}
	entries, err := snapshotTreeEntries(source)
	if err != nil {
		return err
	}
	created := false
	defer func() {
		if !created {
			resultErr = errors.Join(resultErr, os.RemoveAll(destination))
		}
	}()
	for _, entry := range entries {
		target := destination
		if entry.path != "." {
			target = filepath.Join(destination, filepath.FromSlash(entry.path))
		}
		sourcePath := source
		if entry.path != "." {
			sourcePath = filepath.Join(source, filepath.FromSlash(entry.path))
		}
		switch entry.kind {
		case snapshotTreeDirectory:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case snapshotTreeFile:
			if err := safeio.CopyFile(sourcePath, target, entry.mode.Perm()); err != nil {
				return err
			}
			if err := os.Chmod(target, entry.mode.Perm()); err != nil {
				return err
			}
		default:
			return errors.New("unsupported snapshot tree entry")
		}
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.kind != snapshotTreeDirectory {
			continue
		}
		target := destination
		if entry.path != "." {
			target = filepath.Join(destination, filepath.FromSlash(entry.path))
		}
		if err := os.Chmod(target, entry.mode.Perm()); err != nil {
			return err
		}
	}
	created = true
	return nil
}

func snapshotTreeHash(root string) (string, error) {
	entries, err := snapshotTreeEntries(root)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	for _, entry := range entries {
		if err := writeSnapshotTreeEntry(digest, root, entry); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func snapshotTreeSize(root string) (int64, error) {
	entries, err := snapshotTreeEntries(root)
	if err != nil {
		return 0, err
	}
	var size int64
	for _, entry := range entries {
		if entry.kind == snapshotTreeFile {
			size += entry.size
		}
	}
	return size, nil
}

func snapshotTreeEntries(root string) ([]snapshotTreeEntry, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("snapshot tree root must be a real directory")
	}
	var entries []snapshotTreeEntry
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		current, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if current.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("snapshot tree contains symbolic link: %s", path)
		}
		kind := snapshotTreeFile
		if current.IsDir() {
			kind = snapshotTreeDirectory
		} else if !current.Mode().IsRegular() {
			return fmt.Errorf("snapshot tree contains non-regular entry: %s", path)
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		entries = append(entries, snapshotTreeEntry{
			path: filepath.ToSlash(relative), kind: kind, mode: current.Mode().Perm(), size: current.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].path < entries[right].path })
	return entries, nil
}

func writeSnapshotTreeEntry(digest hash.Hash, root string, entry snapshotTreeEntry) error {
	path := []byte(entry.path)
	if err := binary.Write(digest, binary.BigEndian, uint64(len(path))); err != nil {
		return err
	}
	if _, err := digest.Write(path); err != nil {
		return err
	}
	if _, err := digest.Write([]byte{entry.kind}); err != nil {
		return err
	}
	if err := binary.Write(digest, binary.BigEndian, uint32(entry.mode.Perm())); err != nil {
		return err
	}
	if entry.kind == snapshotTreeDirectory {
		return nil
	}
	if err := binary.Write(digest, binary.BigEndian, uint64(entry.size)); err != nil {
		return err
	}
	pathOnDisk := filepath.Join(root, filepath.FromSlash(entry.path))
	file, err := os.Open(pathOnDisk)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(digest, file)
	closeErr := file.Close()
	return errors.Join(copyErr, closeErr)
}

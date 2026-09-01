package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPublishDirectoryPublishesSyncedTree(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "staging")
	target := filepath.Join(root, "bundle")
	if err := os.MkdirAll(filepath.Join(source, "untracked", "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tracked.patch"), []byte("patch"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(source, "untracked", "nested", "payload.bin")
	if err := os.WriteFile(payload, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PublishDirectory(source, target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging directory remains: %v", err)
	}
	assertFileContent(t, filepath.Join(target, "untracked", "nested", "payload.bin"), "payload")
}

func TestPublishSyncedDirectoryReportsPostCommitFailure(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "staging")
	target := filepath.Join(root, "bundle")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("bundle parent sync failed")
	var syncedParent string
	err := publishSyncedDirectory(
		source,
		target,
		os.Rename,
		func(path string) error {
			syncedParent = path
			return syncErr
		},
	)
	if !errors.Is(err, syncErr) || !WasCommitted(err) {
		t.Fatalf("publish error = %v, want committed error wrapping %v", err, syncErr)
	}
	if syncedParent != root {
		t.Fatalf("synced parent = %q, want %q", syncedParent, root)
	}
	if info, statErr := os.Lstat(target); statErr != nil || !info.IsDir() {
		t.Fatalf("published target = %#v, %v", info, statErr)
	}
}

func TestPublishSyncedDirectoryRenameFailureIsNotCommitted(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "staging")
	target := filepath.Join(root, "bundle")
	if err := os.Mkdir(source, 0o755); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename failed")
	err := publishSyncedDirectory(
		source,
		target,
		func(string, string) error { return renameErr },
		func(string) error { return nil },
	)
	if !errors.Is(err, renameErr) || WasCommitted(err) {
		t.Fatalf("publish error = %v, want pre-commit error %v", err, renameErr)
	}
}

func TestSyncTreeRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if err := SyncTree(root); err == nil {
		t.Fatal("SyncTree accepted a symbolic link")
	}
}

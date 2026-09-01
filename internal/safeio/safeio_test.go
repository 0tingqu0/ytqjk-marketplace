package safeio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAtomicWriteReplacesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWrite(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "new")
	assertNoTemporaryFiles(t, root)
}

func TestAtomicWriteRenameFailurePreservesExistingFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename failed")

	err := atomicWrite(path, []byte("new"), 0o600, func(_, _ string) error {
		return renameErr
	})
	if !errors.Is(err, renameErr) {
		t.Fatalf("AtomicWrite error = %v, want %v", err, renameErr)
	}
	if WasCommitted(err) {
		t.Fatal("rename failure was marked committed")
	}
	assertFileContent(t, path, "old")
	assertNoTemporaryFiles(t, root)
}

func TestAtomicWriteReportsPostCommitSyncFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state.json")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	syncErr := errors.New("directory sync failed")

	err := atomicWriteWithSync(
		path,
		[]byte("new"),
		0o600,
		replaceFile,
		func(string) error { return syncErr },
	)
	if !errors.Is(err, syncErr) || !WasCommitted(err) {
		t.Fatalf("AtomicWrite error = %v, want committed error wrapping %v", err, syncErr)
	}
	if WasCommitted(errors.New("outer: " + err.Error())) {
		t.Fatal("plain error text was marked committed")
	}
	if !WasCommitted(errors.Join(errors.New("context"), err)) {
		t.Fatal("wrapped post-commit error lost committed classification")
	}
	assertFileContent(t, path, "new")
	assertNoTemporaryFiles(t, root)
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("file content = %q, want %q", got, want)
	}
}

func assertNoTemporaryFiles(t *testing.T, directory string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(directory, ".ytqjk-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary files remain: %v", matches)
	}
}

func TestAtomicJSONAndContainment(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "state", "value.json")
	want := map[string]any{"status": "READY", "count": 2}
	if err := WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := ReadJSON(path, &got); err != nil || got["status"] != "READY" {
		t.Fatalf("read = %#v, %v", got, err)
	}
	if _, err := Contained(root, filepath.Join(root, "child")); err != nil {
		t.Fatal(err)
	}
	if _, err := Contained(root, filepath.Dir(root)); err == nil {
		t.Fatal("parent path was accepted")
	}
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("state file = %#v, %v", info, err)
	}
}

func TestContainedRejectsSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := Contained(root, filepath.Join(link, "value.json")); err == nil {
		t.Fatal("symbolic-link escape was accepted")
	}
}

func TestTreeHashFramesPathAndContent(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	if err := os.WriteFile(filepath.Join(first, "a"), []byte("bc"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "ab"), []byte("c"), 0o600); err != nil {
		t.Fatal(err)
	}
	left, err := TreeHash(first)
	if err != nil {
		t.Fatal(err)
	}
	right, err := TreeHash(second)
	if err != nil {
		t.Fatal(err)
	}
	if left == right {
		t.Fatal("tree hash did not frame paths and contents")
	}
}

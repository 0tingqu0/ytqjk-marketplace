package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

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

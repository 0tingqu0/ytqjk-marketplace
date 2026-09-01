package handoff

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestDisjointPathDomainsRejectsParentChildOverlap(t *testing.T) {
	tests := []struct {
		name      string
		tracked   string
		untracked string
	}{
		{name: "tracked parent", tracked: "link", untracked: "link/payload.txt"},
		{name: "untracked parent", tracked: "tree/value.txt", untracked: "tree"},
		{name: "case insensitive alias", tracked: "Tree", untracked: "tree/value.txt"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := disjointPathDomains([]string{test.tracked}, []string{test.untracked}); err == nil {
				t.Fatal("overlapping tracked and untracked paths were accepted")
			}
		})
	}
	if err := disjointPathDomains([]string{"tree/left.txt"}, []string{"tree/right.txt"}); err != nil {
		t.Fatalf("disjoint siblings rejected: %v", err)
	}
}

func TestApplyRejectsTrackedParentOfUntrackedPayload(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "integration")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, repo, "init")
	git(t, repo, "config", "user.name", "YTQJK Test")
	git(t, repo, "config", "user.email", "ytqjk@example.invalid")
	writeTestFile(t, filepath.Join(repo, "base.txt"), "base\n")
	git(t, repo, "add", "base.txt")
	git(t, repo, "commit", "-m", "base")
	patchRepo := filepath.Join(root, "patch-source")
	clone(t, root, repo, patchRepo)
	writeTestFile(t, filepath.Join(patchRepo, "link"), "tracked parent\n")
	git(t, patchRepo, "add", "link")
	patch, err := gitOutput(
		patchRepo,
		"diff", "--cached", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-renames", "--no-textconv", "--",
	)
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "bundle")
	payloadPath := filepath.Join(bundle, "untracked", "link", "escape.txt")
	if err := os.MkdirAll(filepath.Dir(payloadPath), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("must stay inside\n")
	if err := os.WriteFile(filepath.Join(bundle, "tracked.patch"), patch, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(payloadPath, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	head, err := gitText(repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		Format:    Format,
		BaseHead:  head,
		Allowlist: []string{"link", "link/escape.txt"},
		Tracked: TrackedPayload{
			Paths: []string{"link"}, Patch: "tracked.patch",
			Bytes: int64(len(patch)), SHA256: safeio.SHA256(patch),
		},
		Untracked: []FilePayload{{
			Path: "link/escape.txt", Bytes: int64(len(payload)), SHA256: safeio.SHA256(payload),
		}},
	}
	manifest.BundleSHA256, err = manifestDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(filepath.Join(bundle, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(repo, bundle); err == nil || !strings.Contains(err.Error(), "paths overlap") {
		t.Fatalf("Apply error = %v, want path overlap rejection", err)
	}
	if status := gitOutputForTest(t, repo, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("rejected apply changed integration repo: %q", status)
	}
}

func TestWriteUntrackedFileDoesNotFollowSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	source := filepath.Join(t.TempDir(), "payload")
	writeTestFile(t, source, "payload\n")
	if err := os.Symlink(outside, filepath.Join(root, "linked")); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	err := writeUntrackedFile(source, root, "linked/escape.txt", 0o600)
	if err == nil {
		t.Fatal("symbolic-link ancestor was followed")
	}
	if _, statErr := os.Lstat(filepath.Join(outside, "escape.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("outside target was written: %v", statErr)
	}
}

func TestWriteUntrackedFileCreatesNestedPayload(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(t.TempDir(), "payload")
	writeTestFile(t, source, "payload\n")
	if err := writeUntrackedFile(source, root, "one/two/value.txt", 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := os.ReadFile(filepath.Join(root, "one", "two", "value.txt"))
	if err != nil || string(value) != "payload\n" {
		t.Fatalf("nested payload = %q, %v", value, err)
	}
}

func TestOutsideRepositoryRequiresPreprovisionedRealParent(t *testing.T) {
	repo := t.TempDir()
	missingParent := filepath.Join(t.TempDir(), "missing", "bundle")
	if _, err := outsideRepository(repo, missingParent); err == nil {
		t.Fatal("missing bundle parent was accepted")
	}

	realParent := t.TempDir()
	symlinkParent := filepath.Join(t.TempDir(), "linked")
	if err := os.Symlink(realParent, symlinkParent); err != nil {
		t.Skipf("symbolic links are unavailable: %v", err)
	}
	if _, err := outsideRepository(repo, filepath.Join(symlinkParent, "bundle")); err == nil {
		t.Fatal("symbolic-link bundle parent was accepted")
	}
}

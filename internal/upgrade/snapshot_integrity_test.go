package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestSnapshotTreeHashDetectsEmptyDirectoryPollution(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	empty := filepath.Join(plan.KnowledgeRoot, "sessions", "session-a", "empty")
	if err := os.MkdirAll(empty, 0o700); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(snapshotRoot(plan.RuntimeRoot, snapshot.ID), snapshotRootKnowledge, "sessions", "pollution")
	if err := os.MkdirAll(stored, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(plan.RuntimeRoot, snapshot.ID); errorCodeOf(err) != "UPGRADE_SNAPSHOT_CORRUPT" {
		t.Fatalf("empty-directory pollution error = %v", err)
	}
}

func TestSnapshotTreeHashDetectsModeTampering(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	writeFixture(t, filepath.Join(plan.KnowledgeRoot, "sessions", "session-a", "anchor.json"), "anchor")
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	stored := filepath.Join(snapshotRoot(plan.RuntimeRoot, snapshot.ID), snapshotRootKnowledge, "sessions", "session-a", "anchor.json")
	info, err := os.Lstat(stored)
	if err != nil {
		t.Fatal(err)
	}
	originalMode := info.Mode().Perm()
	defer func() { _ = os.Chmod(stored, originalMode) }()
	changed := false
	for _, mode := range []os.FileMode{0o400, 0o444, 0o600, 0o700} {
		if mode == originalMode || os.Chmod(stored, mode) != nil {
			continue
		}
		current, statErr := os.Lstat(stored)
		if statErr == nil && current.Mode().Perm() != originalMode {
			changed = true
			break
		}
	}
	if !changed {
		t.Skip("filesystem does not expose permission-mode changes")
	}
	if _, err := readSnapshot(plan.RuntimeRoot, snapshot.ID); errorCodeOf(err) != "UPGRADE_SNAPSHOT_CORRUPT" {
		t.Fatalf("mode-tampered snapshot error = %v", err)
	}
}

func TestSnapshotReadUsesStrictJSONAndReturnsManifestDigest(t *testing.T) {
	t.Run("manifest digest", func(t *testing.T) {
		plan := newSnapshotTestPlan(t)
		snapshot, err := captureSnapshot(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		if !hexDigestPattern.MatchString(snapshot.ManifestSHA256) {
			t.Fatalf("capture manifest digest = %q", snapshot.ManifestSHA256)
		}
		loaded, err := readSnapshot(plan.RuntimeRoot, snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.ManifestSHA256 != snapshot.ManifestSHA256 {
			t.Fatalf("loaded manifest digest = %q, want %q", loaded.ManifestSHA256, snapshot.ManifestSHA256)
		}
		manifest, err := os.ReadFile(filepath.Join(snapshotRoot(plan.RuntimeRoot, snapshot.ID), snapshotManifestName))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(manifest), "manifest_sha256") {
			t.Fatal("manifest digest leaked into self-hashed manifest")
		}
	})
	for _, test := range []struct {
		name    string
		rewrite func(string) string
	}{
		{name: "unknown field", rewrite: func(value string) string {
			return strings.Replace(value, "{", "{\n  \"unknown\": true,", 1)
		}},
		{name: "duplicate field", rewrite: func(value string) string {
			return strings.Replace(value, "{", "{\n  \"schema\": \"duplicate\",", 1)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := newSnapshotTestPlan(t)
			snapshot, err := captureSnapshot(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			root := snapshotRoot(plan.RuntimeRoot, snapshot.ID)
			manifest := filepath.Join(root, snapshotManifestName)
			data, err := os.ReadFile(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := safeio.AtomicWrite(manifest, []byte(test.rewrite(string(data))), 0o600); err != nil {
				t.Fatal(err)
			}
			digest, err := safeio.FileSHA256(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := safeio.AtomicWrite(filepath.Join(root, snapshotDigestName), []byte(digest+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readSnapshot(plan.RuntimeRoot, snapshot.ID); errorCodeOf(err) != "UPGRADE_SNAPSHOT_INVALID" {
				t.Fatalf("strict JSON error = %v", err)
			}
		})
	}
}

func TestSnapshotTreeCopyPreservesEmptyDirectoriesAndModes(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	destination := filepath.Join(root, "destination")
	if err := os.MkdirAll(filepath.Join(source, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, "file.txt"), "content")
	if err := snapshotCopyTree(source, destination); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(filepath.Join(destination, "empty")); err != nil || !info.IsDir() {
		t.Fatalf("copied empty directory = %v, %v", info, err)
	}
	sourceHash, err := snapshotTreeHash(source)
	if err != nil {
		t.Fatal(err)
	}
	destinationHash, err := snapshotTreeHash(destination)
	if err != nil {
		t.Fatal(err)
	}
	if sourceHash != destinationHash {
		t.Fatalf("copied tree digest = %s, want %s", destinationHash, sourceHash)
	}
}

func TestSnapshotTreeRejectsSymlinkDuringHash(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(target, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := snapshotTreeHash(target); err == nil {
		t.Fatal("snapshot tree hash accepted a symlink")
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
}

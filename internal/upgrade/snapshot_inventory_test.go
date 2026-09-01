package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotRestoresPersistentInventory(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	databasePaths := []string{
		filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3"),
		filepath.Join(plan.KnowledgeRoot, "service", "library-v1.sqlite3"),
		filepath.Join(plan.KnowledgeRoot, "service", "orchestration.sqlite3"),
	}
	for _, path := range databasePaths {
		writeSnapshotDatabaseValue(t, path, "old")
	}
	files := []string{
		"service/orchestration.key",
		"catalog.json",
		"sessions/session-a/anchor.json",
		"global/source.md",
		"verified/source.md",
		"personal-experience/approved/source.md",
		"personal-experience/candidates/source.md",
		"error-experience/approved/source.md",
		"error-experience/candidates/source.md",
		"service/intake/uploads/source.pdf",
		"libraries/group-a/manifest.json",
		"handoffs/sqlite-projections/operation-a/receipt.json",
		"global-cache/manifest.json",
		"projects/project-a/handoffs/receipt.json",
		"projects/project-a/errors/error.json",
		"projects/project-a/manifest.json",
		"projects/project-a/index.json",
		"projects/project-a/vectors.json",
		"projects/project-a/cache/prefetch.json",
		"projects/project-a/vectors/model.json",
	}
	for _, relative := range files {
		writeFixture(t, filepath.Join(plan.KnowledgeRoot, filepath.FromSlash(relative)), "old:"+relative)
	}
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotInventoryItem(t, snapshot, "service/library-v1.sqlite3", snapshotClassData, snapshotKindSQLite, true)
	assertSnapshotInventoryItem(t, snapshot, "service/orchestration.key", snapshotClassData, snapshotKindFile, true)
	assertSnapshotInventoryItem(t, snapshot, "libraries", snapshotClassData, snapshotKindTree, true)
	assertSnapshotInventoryItem(t, snapshot, "global-cache", snapshotClassCache, snapshotKindTree, true)
	assertSnapshotInventoryItem(t, snapshot, "projects/project-a/cache", snapshotClassCache, snapshotKindTree, true)
	for _, path := range databasePaths {
		updateSnapshotDatabaseValue(t, path, "new")
	}
	for _, relative := range files {
		writeFixture(t, filepath.Join(plan.KnowledgeRoot, filepath.FromSlash(relative)), "new:"+relative)
	}
	if err := restoreSnapshot(plan, snapshot, true); err != nil {
		t.Fatal(err)
	}
	for _, path := range databasePaths {
		if value := readSnapshotDatabaseValue(t, path); value != "old" {
			t.Fatalf("%s value = %q", path, value)
		}
	}
	for _, relative := range files {
		assertFixture(t, filepath.Join(plan.KnowledgeRoot, filepath.FromSlash(relative)), "old:"+relative)
	}
}

func TestSnapshotRestoresAbsentDataAsAbsent(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	created := []string{
		"service/library-v1.sqlite3",
		"service/orchestration.sqlite3",
		"service/orchestration.key",
		"catalog.json",
		"sessions/new-session/anchor.json",
		"libraries/new-group/manifest.json",
		"service/intake/uploads/new.pdf",
	}
	for _, relative := range created {
		writeFixture(t, filepath.Join(plan.KnowledgeRoot, filepath.FromSlash(relative)), "new")
	}
	if err := restoreSnapshot(plan, snapshot, true); err != nil {
		t.Fatal(err)
	}
	for _, relative := range created {
		path := filepath.Join(plan.KnowledgeRoot, filepath.FromSlash(relative))
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("absent snapshot target remains at %s: %v", path, err)
		}
	}
}

func TestSnapshotRejectsUnpairedOrchestrationIdentity(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	writeSnapshotDatabaseValue(t, filepath.Join(plan.KnowledgeRoot, "service", "orchestration.sqlite3"), "value")
	if _, err := captureSnapshot(context.Background(), plan); err == nil {
		t.Fatal("snapshot accepted orchestration database without its identity key")
	}
}

func TestSnapshotRejectsManifestTamperingAndPollution(t *testing.T) {
	t.Run("manifest digest", func(t *testing.T) {
		plan := newSnapshotTestPlan(t)
		snapshot, err := captureSnapshot(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		manifest := filepath.Join(snapshotRoot(plan.RuntimeRoot, snapshot.ID), snapshotManifestName)
		file, err := os.OpenFile(manifest, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(" \n"); err != nil {
			file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := readSnapshot(plan.RuntimeRoot, snapshot.ID); errorCodeOf(err) != "UPGRADE_SNAPSHOT_CORRUPT" {
			t.Fatalf("tampered manifest error = %v", err)
		}
	})
	t.Run("undeclared file", func(t *testing.T) {
		plan := newSnapshotTestPlan(t)
		snapshot, err := captureSnapshot(context.Background(), plan)
		if err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(snapshotRoot(plan.RuntimeRoot, snapshot.ID), "undeclared.txt"), "pollution")
		if _, err := readSnapshot(plan.RuntimeRoot, snapshot.ID); errorCodeOf(err) != "UPGRADE_SNAPSHOT_CORRUPT" {
			t.Fatalf("polluted snapshot error = %v", err)
		}
	})
}

func TestSnapshotRejectsSymlinkedData(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(plan.KnowledgeRoot, "sessions")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := captureSnapshot(context.Background(), plan); err == nil {
		t.Fatal("snapshot accepted symlinked session data")
	}
}

func newSnapshotTestPlan(t *testing.T) Plan {
	t.Helper()
	root := t.TempDir()
	plan := Plan{
		FromVersion: "0.6.10", ToVersion: "0.7.0", PreviousMaxSchema: 4, DatabaseSchema: 4,
		RuntimeRoot: filepath.Join(root, "runtime"), CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"),
	}
	for _, path := range []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return plan
}

func writeSnapshotDatabaseValue(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE value(text TEXT); INSERT INTO value VALUES (?)", value); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func updateSnapshotDatabaseValue(t *testing.T, path, value string) {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE value SET text=?", value); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func readSnapshotDatabaseValue(t *testing.T, path string) string {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow("SELECT text FROM value").Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertSnapshotInventoryItem(t *testing.T, snapshot Snapshot, relative, class, kind string, present bool) {
	t.Helper()
	for _, item := range snapshot.Items {
		if item.Root == snapshotRootKnowledge && item.RelativePath == relative {
			if item.Class != class || item.Kind != kind || item.Present != present {
				t.Fatalf("snapshot item %s = %#v", relative, item)
			}
			return
		}
	}
	t.Fatalf("snapshot item %s is missing", relative)
}

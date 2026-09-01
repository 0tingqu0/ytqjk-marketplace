package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestManualSnapshotRestorePreservesLiveData(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	database := filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3")
	catalog := filepath.Join(plan.KnowledgeRoot, "catalog.json")
	writeFixture(t, binary, "old-binary")
	writeSnapshotDatabaseValue(t, database, "old-database")
	writeFixture(t, catalog, "old-catalog")
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "new-binary")
	updateSnapshotDatabaseValue(t, database, "new-database")
	writeFixture(t, catalog, "new-catalog")
	if err := restoreSnapshot(plan, snapshot, false); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, binary, "old-binary")
	if value := readSnapshotDatabaseValue(t, database); value != "new-database" {
		t.Fatalf("manual rollback database value = %q", value)
	}
	assertFixture(t, catalog, "new-catalog")
}

func TestSnapshotRestoreFailureReversesEveryChangedTarget(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	catalog := filepath.Join(plan.KnowledgeRoot, "catalog.json")
	writeFixture(t, binary, "old-binary")
	writeFixture(t, catalog, "old-catalog")
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "new-binary")
	writeFixture(t, catalog, "new-catalog")
	originalRename := renameRestorePath
	defer func() { renameRestorePath = originalRename }()
	calls := 0
	renameRestorePath = func(source, destination string) error {
		calls++
		if calls == 3 {
			return errors.New("injected restore rename failure")
		}
		return os.Rename(source, destination)
	}
	if err := restoreSnapshot(plan, snapshot, true); err == nil {
		t.Fatal("snapshot restore succeeded after injected rename failure")
	}
	assertFixture(t, binary, "new-binary")
	assertFixture(t, catalog, "new-catalog")
	journal := filepath.Join(plan.RuntimeRoot, "upgrade", "restore-"+snapshot.ID+".json")
	if _, err := os.Lstat(journal); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back restore journal remains: %v", err)
	}
	for _, directory := range []string{filepath.Dir(binary), filepath.Dir(catalog)} {
		for _, pattern := range []string{
			".ytqjk-restore-stage-*", ".ytqjk-restore-backup-*", ".ytqjk-restore-discard-*",
		} {
			matches, err := filepath.Glob(filepath.Join(directory, pattern))
			if err != nil || len(matches) != 0 {
				t.Fatalf("restore artifacts for %s = %v, %v", directory, matches, err)
			}
		}
	}
}

func TestSnapshotRestoreDoesNotRemoveConflictingStage(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "old-binary")
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "new-binary")
	token := safeio.SHA256([]byte(binary))[:12]
	conflict := filepath.Join(filepath.Dir(binary), ".ytqjk-restore-stage-"+snapshot.ID+"-"+token)
	writeFixture(t, conflict, "not-owned-by-restore")
	if err := restoreSnapshot(plan, snapshot, false); err == nil {
		t.Fatal("snapshot restore accepted a conflicting stage path")
	}
	assertFixture(t, conflict, "not-owned-by-restore")
	assertFixture(t, binary, "new-binary")
}

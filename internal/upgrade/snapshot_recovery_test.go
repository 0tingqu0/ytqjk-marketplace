package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type persistedRestoreFixture struct {
	plan        Plan
	snapshot    Snapshot
	items       []restoreItem
	journal     restoreJournal
	journalPath string
	binary      string
	catalog     string
	session     string
}

func TestSnapshotRecoveryRollsBackInterruptedPhases(t *testing.T) {
	tests := []struct {
		name  string
		phase string
		crash func(t *testing.T, fixture *persistedRestoreFixture)
	}{
		{name: "prepared", phase: "PREPARED"},
		{name: "after backup rename", phase: "SWAPPING", crash: crashAfterBackupRename},
		{name: "after target install", phase: "SWAPPING", crash: crashAfterTargetInstall},
		{name: "rolling back", phase: "ROLLING_BACK", crash: crashAfterTargetInstall},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPersistedRestoreFixture(t)
			if test.crash != nil {
				test.crash(t, fixture)
			}
			if err := writeRestoreJournal(fixture.journalPath, &fixture.journal, test.phase, restoreDecisionRollback, 0, 1); err != nil {
				t.Fatal(err)
			}
			if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
				t.Fatal(err)
			}
			assertFixture(t, fixture.binary, "live-binary")
			assertFixture(t, fixture.catalog, "live-catalog")
			assertFixture(t, fixture.session, "live-session")
			assertRestoreRecoveryClean(t, fixture)
		})
	}
}

func TestSnapshotRecoveryCompletesCommittedGeneration(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	swapEveryRestoreItem(t, fixture)
	if err := writeRestoreJournal(fixture.journalPath, &fixture.journal, "COMMITTED", restoreDecisionCommit, len(fixture.items)-1, len(fixture.items)); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, fixture.binary, "snapshot-binary")
	assertFixture(t, fixture.catalog, "snapshot-catalog")
	assertFixture(t, fixture.session, "snapshot-session")
	assertRestoreRecoveryClean(t, fixture)
}

func TestSnapshotRecoveryConsumesOldJournalBeforeNewRestore(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	if err := restoreSnapshot(fixture.plan, fixture.snapshot, true); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, fixture.binary, "snapshot-binary")
	assertFixture(t, fixture.catalog, "snapshot-catalog")
	assertRestoreRecoveryClean(t, fixture)
}

func TestSnapshotRecoveryRetainsJournalAfterCleanupFailure(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	swapEveryRestoreItem(t, fixture)
	if err := writeRestoreJournal(fixture.journalPath, &fixture.journal, "COMMITTED", restoreDecisionCommit, len(fixture.items)-1, len(fixture.items)); err != nil {
		t.Fatal(err)
	}
	backup := firstOriginalRestoreItem(t, fixture).Backup
	originalRemove := removeRestorePath
	defer func() { removeRestorePath = originalRemove }()
	removeRestorePath = func(path string) error {
		if sameRestorePath(path, backup) {
			return errors.New("injected cleanup failure")
		}
		return os.RemoveAll(path)
	}
	if err := recoverPendingRestoreJournals(fixture.plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if _, err := os.Lstat(fixture.journalPath); err != nil {
		t.Fatalf("recovery journal was removed after cleanup failure: %v", err)
	}
	if _, err := os.Lstat(backup); err != nil {
		t.Fatalf("failed cleanup artifact was removed: %v", err)
	}
	removeRestorePath = originalRemove
	if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
		t.Fatal(err)
	}
	assertRestoreRecoveryClean(t, fixture)
}

func TestSnapshotRollbackRetainsJournalAfterStageCleanupFailure(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	stage := firstOriginalRestoreItem(t, fixture).Staged
	originalRemove := removeRestorePath
	defer func() { removeRestorePath = originalRemove }()
	removeRestorePath = func(path string) error {
		if sameRestorePath(path, stage) {
			return errors.New("injected stage cleanup failure")
		}
		return os.RemoveAll(path)
	}
	if err := recoverPendingRestoreJournals(fixture.plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("rollback cleanup failure error = %v", err)
	}
	if _, err := os.Lstat(fixture.journalPath); err != nil {
		t.Fatalf("rollback journal was removed after cleanup failure: %v", err)
	}
	if _, err := os.Lstat(stage); err != nil {
		t.Fatalf("failed rollback cleanup artifact was removed: %v", err)
	}
	removeRestorePath = originalRemove
	if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, fixture.binary, "live-binary")
	assertFixture(t, fixture.catalog, "live-catalog")
	assertRestoreRecoveryClean(t, fixture)
}

func TestSnapshotRecoveryMarksAmbiguousState(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	item := firstOriginalRestoreItem(t, fixture)
	if err := os.Rename(item.Target, item.Backup); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, item.Target, "unknown-generation")
	if err := os.Remove(item.Backup); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreJournal(fixture.journalPath, &fixture.journal, "SWAPPING", restoreDecisionRollback, 0, 1); err != nil {
		t.Fatal(err)
	}
	if err := recoverPendingRestoreJournals(fixture.plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("ambiguous recovery error = %v", err)
	}
	journal, err := readRestoreJournal(fixture.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if journal.Phase != "RECOVERY_REQUIRED" {
		t.Fatalf("ambiguous recovery phase = %s", journal.Phase)
	}
	assertFixture(t, item.Target, "unknown-generation")
}

func newPersistedRestoreFixture(t *testing.T) *persistedRestoreFixture {
	t.Helper()
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	catalog := filepath.Join(plan.KnowledgeRoot, "catalog.json")
	session := filepath.Join(plan.KnowledgeRoot, "sessions", "session-a", "anchor.json")
	writeFixture(t, binary, "snapshot-binary")
	writeFixture(t, catalog, "snapshot-catalog")
	writeFixture(t, session, "snapshot-session")
	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "live-binary")
	writeFixture(t, catalog, "live-catalog")
	writeFixture(t, session, "live-session")
	loaded, err := readSnapshot(plan.RuntimeRoot, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	items, err := snapshotRestoreItems(plan, loaded, true)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := initializeRestoreJournal(plan, snapshot.ID, snapshot.ManifestSHA256, items, nil)
	if err != nil {
		t.Fatal(err)
	}
	journalPath := restoreJournalPath(plan.RuntimeRoot, snapshot.ID)
	if err := writeRestoreJournal(journalPath, &journal, "PREPARING", restoreDecisionRollback, -1, 0); err != nil {
		t.Fatal(err)
	}
	if err := prepareSnapshotRestoreItems(items, journal.Items); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreJournal(journalPath, &journal, "PREPARED", restoreDecisionRollback, -1, 0); err != nil {
		t.Fatal(err)
	}
	return &persistedRestoreFixture{
		plan: plan, snapshot: snapshot, items: items, journal: journal, journalPath: journalPath,
		binary: binary, catalog: catalog, session: session,
	}
}

func crashAfterBackupRename(t *testing.T, fixture *persistedRestoreFixture) {
	t.Helper()
	item := firstOriginalRestoreItem(t, fixture)
	if err := os.Rename(item.Target, item.Backup); err != nil {
		t.Fatal(err)
	}
}

func crashAfterTargetInstall(t *testing.T, fixture *persistedRestoreFixture) {
	t.Helper()
	item := firstOriginalRestoreItem(t, fixture)
	if err := os.Rename(item.Target, item.Backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(item.Staged, item.Target); err != nil {
		t.Fatal(err)
	}
}

func swapEveryRestoreItem(t *testing.T, fixture *persistedRestoreFixture) {
	t.Helper()
	for _, item := range fixture.journal.Items {
		if item.Original.Present {
			if err := os.Rename(item.Target, item.Backup); err != nil {
				t.Fatal(err)
			}
		}
		if item.Desired.Present {
			if err := os.Rename(item.Staged, item.Target); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func firstOriginalRestoreItem(t *testing.T, fixture *persistedRestoreFixture) restoreJournalItem {
	t.Helper()
	for _, item := range fixture.journal.Items {
		if item.Original.Present && item.Desired.Present {
			return item
		}
	}
	t.Fatal("restore fixture has no present item")
	return restoreJournalItem{}
}

func assertRestoreRecoveryClean(t *testing.T, fixture *persistedRestoreFixture) {
	t.Helper()
	if _, err := os.Lstat(fixture.journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal remains: %v", err)
	}
	for _, item := range fixture.journal.Items {
		for _, path := range []string{item.Staged, item.Backup, item.Discard} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore artifact remains at %s: %v", path, err)
			}
		}
	}
}

package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestSnapshotRollbackRecoversEveryDirectoryRenamePoint(t *testing.T) {
	for _, afterBackupRestore := range []bool{false, true} {
		name := "after-target-to-discard"
		if afterBackupRestore {
			name = "after-backup-to-target"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newPersistedRestoreFixture(t)
			item := firstDirectoryRestoreItem(t, fixture)
			installDesiredRestoreItem(t, item)
			if err := writeRestoreJournal(
				fixture.journalPath, &fixture.journal, "ROLLING_BACK", restoreDecisionRollback, 0, 1,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(item.Target, item.Discard); err != nil {
				t.Fatal(err)
			}
			if afterBackupRestore {
				if err := os.Rename(item.Backup, item.Target); err != nil {
					t.Fatal(err)
				}
			}
			if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
				t.Fatal(err)
			}
			assertFixture(t, fixture.session, "live-session")
			assertRestoreRecoveryClean(t, fixture)
		})
	}
}

func TestSnapshotRollbackResumesPartialArtifactCleanup(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	item := firstDirectoryRestoreItem(t, fixture)
	installDesiredRestoreItem(t, item)
	if err := os.Rename(item.Target, item.Discard); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(item.Backup, item.Target); err != nil {
		t.Fatal(err)
	}
	if err := writeRestoreJournal(
		fixture.journalPath, &fixture.journal, "ROLLED_BACK_CLEANING", restoreDecisionRollback, 0, 1,
	); err != nil {
		t.Fatal(err)
	}
	originalRemove := removeRestorePath
	t.Cleanup(func() { removeRestorePath = originalRemove })
	failed := false
	removeRestorePath = func(path string) error {
		if sameRestorePath(path, item.Discard) && !failed {
			failed = true
			if err := os.Remove(filepath.Join(path, "session-a", "anchor.json")); err != nil {
				return err
			}
			return errors.New("injected partial cleanup failure")
		}
		return os.RemoveAll(path)
	}
	if err := recoverPendingRestoreJournals(fixture.plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("partial cleanup error = %v", err)
	}
	assertFixture(t, fixture.session, "live-session")
	if _, err := os.Lstat(fixture.journalPath); err != nil {
		t.Fatalf("partial cleanup removed recovery journal: %v", err)
	}
	removeRestorePath = originalRemove
	if err := recoverPendingRestoreJournals(fixture.plan); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, fixture.session, "live-session")
	assertRestoreRecoveryClean(t, fixture)
}

func TestSnapshotCommittedPostCommitNeverRollsBack(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "snapshot-binary")
	snapshot, err := captureSnapshot(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "live-binary")
	originalWriter := writeRestoreJournalFile
	t.Cleanup(func() { writeRestoreJournalFile = originalWriter })
	commitObserved := false
	writeRestoreJournalFile = func(guard *restoreGuard, path string, value any) error {
		if err := originalWriter(guard, path, value); err != nil {
			return err
		}
		journal, ok := value.(*restoreJournal)
		if ok && journal.Phase == "COMMITTED" {
			commitObserved = journal.Decision == restoreDecisionCommit
			return &safeio.PostCommitError{Operation: "injected journal commit", Err: errors.New("sync failed")}
		}
		return nil
	}
	if err = restoreSnapshot(plan, snapshot, false); err != nil {
		t.Fatal(err)
	}
	if !commitObserved {
		t.Fatal("restore did not persist a monotonic COMMIT decision")
	}
	assertFixture(t, binary, "snapshot-binary")
	journalPath := restoreJournalPath(plan.RuntimeRoot, snapshot.ID)
	if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("committed journal remains: %v", err)
	}
}

func TestSnapshotPrecommitPostCommitReportsRollback(t *testing.T) {
	for _, phase := range []string{"PREPARING", "PREPARED", "SWAPPING"} {
		t.Run(phase, func(t *testing.T) {
			plan := newSnapshotTestPlan(t)
			binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
			writeFixture(t, binary, "snapshot-binary")
			snapshot, err := captureSnapshot(t.Context(), plan)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, binary, "live-binary")
			originalWriter := writeRestoreJournalFile
			t.Cleanup(func() { writeRestoreJournalFile = originalWriter })
			injected := false
			writeRestoreJournalFile = func(guard *restoreGuard, path string, value any) error {
				if err := originalWriter(guard, path, value); err != nil {
					return err
				}
				journal, ok := value.(*restoreJournal)
				atInjectionPoint := ok && phase != "SWAPPING"
				if ok && phase == "SWAPPING" && journal.Changed >= 0 && journal.Changed < len(journal.Items) {
					item := journal.Items[journal.Changed]
					atInjectionPoint = item.Original.Present || item.Desired.Present
				}
				if ok && journal.Phase == phase && atInjectionPoint && !injected {
					injected = true
					return &safeio.PostCommitError{Operation: "injected precommit journal", Err: errors.New("sync failed")}
				}
				return nil
			}
			err = restoreSnapshot(plan, snapshot, false)
			if errorCodeOf(err) != "UPGRADE_SNAPSHOT_RESTORE_ROLLED_BACK" {
				t.Fatalf("precommit PostCommit error = %v", err)
			}
			if !injected {
				t.Fatalf("phase %s was not injected", phase)
			}
			assertFixture(t, binary, "live-binary")
			journalPath := restoreJournalPath(plan.RuntimeRoot, snapshot.ID)
			if _, err := os.Lstat(journalPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("rolled-back journal remains: %v", err)
			}
		})
	}
}

func TestBoundSnapshotRecoveryFailsOnMissingOrReplacedJournal(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		fixture := newPersistedRestoreFixture(t)
		if err := os.Remove(fixture.journalPath); err != nil {
			t.Fatal(err)
		}
		outcome, err := recoverBoundRestoreJournal(
			fixture.plan, fixture.snapshot.ID, fixture.snapshot.ManifestSHA256,
		)
		if outcome != restoreRecoveryUnknown || errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
			t.Fatalf("missing journal outcome = %v, %v", outcome, err)
		}
	})
	t.Run("replaced during transition", func(t *testing.T) {
		fixture := newPersistedRestoreFixture(t)
		originalWriter := writeRestoreJournalFile
		t.Cleanup(func() { writeRestoreJournalFile = originalWriter })
		writeRestoreJournalFile = func(guard *restoreGuard, path string, _ any) error {
			replacement := fixture.journal
			replacement.ManifestSHA256 = testOperationB
			replacement.guard = guard
			return originalWriter(guard, path, &replacement)
		}
		outcome, err := recoverBoundRestoreJournal(
			fixture.plan, fixture.snapshot.ID, fixture.snapshot.ManifestSHA256,
		)
		if outcome != restoreRecoveryUnknown || errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
			t.Fatalf("replaced journal outcome = %v, %v", outcome, err)
		}
	})
}

func TestSnapshotCommittedRecoveryFailurePreservesDecision(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "snapshot-binary")
	snapshot, err := captureSnapshot(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "live-binary")
	originalWriter := writeRestoreJournalFile
	t.Cleanup(func() { writeRestoreJournalFile = originalWriter })
	writeRestoreJournalFile = func(guard *restoreGuard, path string, value any) error {
		journal, ok := value.(*restoreJournal)
		if ok && journal.Phase == "COMMITTED_CLEANING" {
			return errors.New("injected recovery write failure")
		}
		if err := originalWriter(guard, path, value); err != nil {
			return err
		}
		if ok && journal.Phase == "COMMITTED" {
			return &safeio.PostCommitError{Operation: "injected journal commit", Err: errors.New("sync failed")}
		}
		return nil
	}
	err = restoreSnapshot(plan, snapshot, false)
	if errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("recovery failure = %v", err)
	}
	assertFixture(t, binary, "snapshot-binary")
	journalPath := restoreJournalPath(plan.RuntimeRoot, snapshot.ID)
	journal, readErr := readRestoreJournal(journalPath)
	if readErr != nil || journal.Decision != restoreDecisionCommit || journal.Phase != "COMMITTED" {
		t.Fatalf("committed journal = %#v, %v", journal, readErr)
	}
	writeRestoreJournalFile = originalWriter
	if err := recoverPendingRestoreJournals(plan); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, binary, "snapshot-binary")
}

func TestSnapshotRestoreRejectsCallerManifestDigestTamper(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "snapshot-binary")
	snapshot, err := captureSnapshot(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "live-binary")
	snapshot.ManifestSHA256 = testOperationB
	if err := restoreSnapshot(plan, snapshot, false); errorCodeOf(err) != "UPGRADE_SNAPSHOT_INVALID" {
		t.Fatalf("manifest mismatch error = %v", err)
	}
	assertFixture(t, binary, "live-binary")
}

func TestSnapshotRecoveryRejectsJournalIdentityTampering(t *testing.T) {
	tests := []struct {
		name   string
		tamper func(*restoreJournal)
	}{
		{name: "manifest digest", tamper: func(journal *restoreJournal) {
			journal.ManifestSHA256 = testOperationB
		}},
		{name: "desired size", tamper: func(journal *restoreJournal) {
			for index := range journal.Items {
				if journal.Items[index].Desired.Present {
					journal.Items[index].Desired.Size++
					return
				}
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPersistedRestoreFixture(t)
			test.tamper(&fixture.journal)
			if err := safeio.WriteJSON(fixture.journalPath, fixture.journal); err != nil {
				t.Fatal(err)
			}
			if err := recoverPendingRestoreJournals(fixture.plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
				t.Fatalf("tampered journal error = %v", err)
			}
		})
	}
}

func TestSnapshotRecoveryRejectsUnknownJournalName(t *testing.T) {
	plan := newSnapshotTestPlan(t)
	path := filepath.Join(plan.RuntimeRoot, "upgrade", "restore-not-a-digest.json")
	writeFixture(t, path, "{}\n")
	if err := recoverPendingRestoreJournals(plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("unknown journal error = %v", err)
	}
}

func firstDirectoryRestoreItem(t *testing.T, fixture *persistedRestoreFixture) restoreJournalItem {
	t.Helper()
	for _, item := range fixture.journal.Items {
		if item.Original.Present && item.Desired.Present && item.Desired.Directory {
			return item
		}
	}
	t.Fatal("restore fixture has no present directory item")
	return restoreJournalItem{}
}

func installDesiredRestoreItem(t *testing.T, item restoreJournalItem) {
	t.Helper()
	if err := os.Rename(item.Target, item.Backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(item.Staged, item.Target); err != nil {
		t.Fatal(err)
	}
}

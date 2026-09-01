package upgrade

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func validateRestoreJournal(plan Plan, path string, journal restoreJournal) error {
	identifier := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), "restore-"), ".json")
	if journal.Schema != restoreJournalSchema || journal.SnapshotID != identifier ||
		!hexDigestPattern.MatchString(identifier) || !hexDigestPattern.MatchString(journal.ManifestSHA256) || len(journal.Items) == 0 {
		return errors.New("invalid restore journal identity")
	}
	roots, err := restorePlanRoots(plan)
	if err != nil {
		return err
	}
	storedRoots := []string{journal.RuntimeRoot, journal.CodexRoot, journal.KnowledgeRoot}
	for index := range roots {
		if !sameRestorePath(roots[index], storedRoots[index]) {
			return errors.New("restore journal root mismatch")
		}
	}
	if journal.Phase == "RECOVERY_REQUIRED" {
		return errors.New("restore journal requires manual recovery")
	}
	loaded, err := readSnapshot(plan.RuntimeRoot, identifier)
	if err != nil {
		return err
	}
	if loaded.ManifestSHA256 != journal.ManifestSHA256 {
		return errors.New("restore journal snapshot digest mismatch")
	}
	full, err := snapshotRestoreItems(plan, loaded, true)
	if err != nil {
		return err
	}
	active, err := snapshotRestoreItems(plan, loaded, false)
	if err != nil {
		return err
	}
	if !journalItemsEqual(journal.Items, full) && !journalItemsEqual(journal.Items, active) {
		return errors.New("restore journal target inventory mismatch")
	}
	seen := map[string]struct{}{}
	for _, item := range journal.Items {
		key, err := restorePathKey(item.Target)
		if err != nil {
			return err
		}
		seen[key] = struct{}{}
	}
	for index, item := range journal.Items {
		itemKey, err := restorePathKey(item.Target)
		if err != nil {
			return err
		}
		if index > 0 {
			previousKey, keyErr := restorePathKey(journal.Items[index-1].Target)
			if keyErr != nil {
				return keyErr
			}
			if previousKey >= itemKey {
				return errors.New("restore journal targets are not ordered")
			}
		}
		if err := validateRestoreTarget(plan, item.Target); err != nil {
			return err
		}
		staged, backup := restoreArtifactPaths(identifier, item.Target)
		discard := restoreDiscardPath(identifier, item.Target)
		if !sameRestorePath(staged, item.Staged) || !sameRestorePath(backup, item.Backup) || !sameRestorePath(discard, item.Discard) {
			return errors.New("restore journal artifact path mismatch")
		}
		for _, artifact := range []string{item.Staged, item.Backup, item.Discard} {
			artifactKey, keyErr := restorePathKey(artifact)
			if keyErr != nil {
				return keyErr
			}
			if _, duplicate := seen[artifactKey]; duplicate {
				return errors.New("restore journal artifact overlaps a reserved path")
			}
			seen[artifactKey] = struct{}{}
		}
		if err := validateRestoreIdentity(item.Original); err != nil {
			return err
		}
		if err := validateRestoreIdentity(item.Desired); err != nil {
			return err
		}
	}
	return nil
}

func validateRestoreIdentity(identity restorePathIdentity) error {
	if !identity.Present {
		if identity.Directory || identity.Mode != 0 || identity.Size != 0 || identity.SHA256 != "" {
			return errors.New("invalid absent restore identity")
		}
		return nil
	}
	if identity.Mode == 0 || identity.Size < 0 || !hexDigestPattern.MatchString(identity.SHA256) {
		return errors.New("invalid present restore identity")
	}
	return nil
}

func journalItemsEqual(journal []restoreJournalItem, items []restoreItem) bool {
	if len(journal) != len(items) {
		return false
	}
	for index := range items {
		if !sameRestorePath(journal[index].Target, items[index].Target) ||
			!restoreIdentityEqual(journal[index].Desired, desiredRestoreIdentity(items[index])) {
			return false
		}
	}
	return true
}

func desiredRestoreIdentity(item restoreItem) restorePathIdentity {
	if !item.Present {
		return restorePathIdentity{}
	}
	return restorePathIdentity{
		Present: true, Directory: item.Directory, Mode: uint32(item.Mode.Perm()),
		Size: item.Size, SHA256: item.ExpectedHash,
	}
}

func readRestoreJournal(path string) (journal restoreJournal, returned error) {
	runtimeRoot := filepath.Dir(filepath.Dir(path))
	guard, err := acquireRestoreGuardRoot(runtimeRoot)
	if err != nil {
		return restoreJournal{}, err
	}
	defer func() { returned = errors.Join(returned, guard.release()) }()
	return readRestoreJournalBound(guard, path)
}

func readRestoreJournalBound(guard *restoreGuard, path string) (restoreJournal, error) {
	file, err := guard.openJournal(path, false)
	if err != nil {
		return restoreJournal{}, err
	}
	opened, statErr := file.Stat()
	if statErr != nil || !opened.Mode().IsRegular() {
		_ = file.Close()
		return restoreJournal{}, errors.Join(errors.New("restore journal is not regular"), statErr)
	}
	data, readErr := io.ReadAll(file)
	closeErr := file.Close()
	if readErr != nil || closeErr != nil {
		return restoreJournal{}, errors.Join(readErr, closeErr)
	}
	if err := guard.verifyJournalHandle(path, opened); err != nil {
		return restoreJournal{}, err
	}
	var journal restoreJournal
	if err := decodeStrictJSON(data, &journal); err != nil {
		return restoreJournal{}, err
	}
	journal.bindingSHA256 = safeio.SHA256(data)
	journal.bindingFile = opened
	journal.guard = guard
	return journal, nil
}

func writeRestoreJournal(
	path string, journal *restoreJournal, phase, decision string, changed, next int,
) (returned error) {
	guard, owned, err := restoreJournalGuard(path, journal)
	if err != nil {
		return err
	}
	if owned {
		defer func() { returned = errors.Join(returned, guard.release()) }()
	}
	if journal.Decision == restoreDecisionCommit && decision != restoreDecisionCommit {
		return errors.New("committed restore decision cannot move backward")
	}
	if err := verifyRestoreJournalWritePrecondition(path, journal); err != nil {
		return err
	}
	nextJournal := *journal
	nextJournal.Phase, nextJournal.Decision = phase, decision
	nextJournal.Changed, nextJournal.Next, nextJournal.UpdatedAt = changed, next, time.Now().UTC()
	nextJournal.bindingSHA256, nextJournal.bindingFile = "", nil
	nextJournal.guard = guard
	err = writeRestoreJournalFile(guard, path, &nextJournal)
	if err != nil && !safeio.WasCommitted(err) {
		return err
	}
	persisted, readErr := readRestoreJournalBound(guard, path)
	if readErr != nil || !restoreJournalsEqual(nextJournal, persisted) {
		return &safeio.PostCommitError{
			Operation: "restore journal write verification",
			Err:       errors.Join(err, readErr, errors.New("restore journal write identity changed")),
		}
	}
	*journal = persisted
	return err
}

func verifyRestoreJournalWritePrecondition(path string, journal *restoreJournal) error {
	if journal.bindingSHA256 == "" || journal.bindingFile == nil {
		if exists, err := restoreJournalExists(journal.guard, path); err != nil {
			return err
		} else if !exists {
			return nil
		}
		return errors.New("restore journal unexpectedly exists")
	}
	return verifyRestoreJournalBinding(path, journal)
}

func verifyRestoreJournalBinding(path string, expected *restoreJournal) error {
	if expected == nil || expected.guard == nil || expected.guard.released {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal has no active guard"))
	}
	current, err := readRestoreJournalBound(expected.guard, path)
	if err != nil {
		return err
	}
	if expected.bindingSHA256 == "" || current.bindingSHA256 != expected.bindingSHA256 ||
		expected.bindingFile == nil || current.bindingFile == nil ||
		!os.SameFile(expected.bindingFile, current.bindingFile) || !restoreJournalsEqual(*expected, current) {
		return errors.New("restore journal binding changed")
	}
	return nil
}

func removeBoundRestoreJournal(path string, journal *restoreJournal) (returned error) {
	guard, owned, err := restoreJournalGuard(path, journal)
	if err != nil {
		return err
	}
	if owned {
		defer func() { returned = errors.Join(returned, guard.release()) }()
	}
	if err := verifyRestoreJournalBinding(path, journal); err != nil {
		return err
	}
	consumed := path + ".consumed-" + journal.bindingSHA256
	if err := consumeRestoreJournalBound(guard, path, consumed); err != nil {
		return err
	}
	if err := verifyRestoreJournalRemoved(guard, path); err != nil {
		return err
	}
	journal.bindingFile = nil
	consumedJournal, err := readRestoreJournalBound(guard, consumed)
	if err != nil || consumedJournal.bindingSHA256 != journal.bindingSHA256 ||
		!restoreJournalsEqual(consumedJournal, *journal) {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("consumed restore journal changed"), err))
	}
	return guard.requireRuntime(guard.root)
}

func verifyRestoreJournalRemoved(guard *restoreGuard, path string) error {
	if exists, err := restoreJournalExists(guard, path); err != nil {
		return err
	} else if !exists {
		return guard.requireRuntime(guard.root)
	}
	return errors.New("restore journal remains after removal")
}

func restoreJournalsEqual(left, right restoreJournal) bool {
	left.bindingSHA256, left.bindingFile = "", nil
	right.bindingSHA256, right.bindingFile = "", nil
	left.guard, right.guard = nil, nil
	return reflect.DeepEqual(left, right)
}

func restoreJournalGuard(path string, journal *restoreJournal) (*restoreGuard, bool, error) {
	if journal == nil {
		return nil, false, failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal is nil"))
	}
	if journal.guard != nil && !journal.guard.released {
		if err := journal.guard.requireRuntime(journal.RuntimeRoot); err != nil {
			return nil, false, err
		}
		return journal.guard, false, nil
	}
	if !sameRestorePath(filepath.Dir(path), filepath.Join(journal.RuntimeRoot, "upgrade")) {
		return nil, false, failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal root mismatch"))
	}
	guard, err := acquireRestoreGuardRoot(journal.RuntimeRoot)
	if err != nil {
		return nil, false, err
	}
	journal.guard = guard
	return guard, true, nil
}

func markRestoreRecoveryRequired(path string, journal *restoreJournal, cause error) error {
	writeErr := writeRestoreJournal(path, journal, "RECOVERY_REQUIRED", journal.Decision, journal.Changed, journal.Next)
	return failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(cause, writeErr))
}

func rollbackRestorePhase(phase string) bool {
	switch phase {
	case "PREPARING", "PREPARED", "SWAPPING", "ROLLING_BACK", "ROLLED_BACK", "ROLLED_BACK_CLEANING":
		return true
	default:
		return false
	}
}

func restoreIdentityEqual(left, right restorePathIdentity) bool {
	return left == right
}

func sameRestorePath(left, right string) bool {
	left, leftErr := filepath.Abs(left)
	right, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	left, right = filepath.Clean(left), filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

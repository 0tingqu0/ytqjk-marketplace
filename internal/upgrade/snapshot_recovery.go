package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func restoreJournalPath(runtimeRoot, identifier string) string {
	return filepath.Join(runtimeRoot, "upgrade", "restore-"+identifier+".json")
}

func initializeRestoreJournal(
	plan Plan, identifier, manifestSHA256 string, items []restoreItem, guard *restoreGuard,
) (restoreJournal, error) {
	if !hexDigestPattern.MatchString(identifier) || !hexDigestPattern.MatchString(manifestSHA256) || len(items) == 0 {
		return restoreJournal{}, errors.New("invalid snapshot restore identifier or inventory")
	}
	if err := validateRestoreTopology(plan, items); err != nil {
		return restoreJournal{}, err
	}
	roots, err := restorePlanRoots(plan)
	if err != nil {
		return restoreJournal{}, err
	}
	journal := restoreJournal{
		Schema: restoreJournalSchema, SnapshotID: identifier, Phase: "PREPARING", Decision: restoreDecisionRollback,
		ManifestSHA256: manifestSHA256, Changed: -1,
		RuntimeRoot: roots[0], CodexRoot: roots[1], KnowledgeRoot: roots[2],
		guard: guard,
	}
	seen := map[string]struct{}{}
	for _, item := range items {
		key, err := restorePathKey(item.Target)
		if err != nil {
			return restoreJournal{}, err
		}
		seen[key] = struct{}{}
	}
	for index := range items {
		item := &items[index]
		if err := validateRestoreTarget(plan, item.Target); err != nil {
			return restoreJournal{}, err
		}
		item.staged, item.backup = restoreArtifactPaths(identifier, item.Target)
		discard := restoreDiscardPath(identifier, item.Target)
		for _, path := range []string{item.staged, item.backup, discard} {
			key, err := restorePathKey(path)
			if err != nil {
				return restoreJournal{}, err
			}
			if _, duplicate := seen[key]; duplicate {
				return restoreJournal{}, errors.New("snapshot restore artifact overlaps a reserved path")
			}
			seen[key] = struct{}{}
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return restoreJournal{}, errors.New("snapshot restore artifact already exists")
			}
		}
		original, err := inspectRestorePath(item.Target)
		if err != nil {
			return restoreJournal{}, err
		}
		desired := desiredRestoreIdentity(*item)
		if err := validateRestoreIdentity(desired); err != nil {
			return restoreJournal{}, err
		}
		journal.Items = append(journal.Items, restoreJournalItem{
			Target: item.Target, Staged: item.staged, Backup: item.backup, Discard: discard,
			Original: original, Desired: desired,
		})
	}
	return journal, nil
}

func recoverPendingRestoreJournals(plan Plan) (returned error) {
	guard, err := acquireRestoreGuard(plan)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, guard.release()) }()
	return recoverPendingRestoreJournalsGuarded(plan, guard)
}

func recoverPendingRestoreJournalsGuarded(plan Plan, guard *restoreGuard) error {
	if err := guard.require(plan); err != nil {
		return err
	}
	directory := filepath.Join(plan.RuntimeRoot, "upgrade")
	entries, err := guard.readUpgradeDirectory()
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	var paths []string
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "restore-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		identifier := strings.TrimSuffix(strings.TrimPrefix(name, "restore-"), ".json")
		if name != "restore-"+identifier+".json" || !hexDigestPattern.MatchString(identifier) {
			return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal name is invalid"))
		}
		if !entry.Type().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal is not a regular file"))
		}
		paths = append(paths, filepath.Join(directory, name))
	}
	sort.Strings(paths)
	for _, path := range paths {
		if _, err := recoverRestoreJournalFile(plan, path, guard); err != nil {
			return err
		}
	}
	return nil
}

func recoverRollbackJournal(path string, journal *restoreJournal) error {
	if journal.Phase == "RECOVERY_REQUIRED" {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal requires manual recovery"))
	}
	if journal.Phase != "ROLLED_BACK_CLEANING" {
		for _, item := range journal.Items {
			if err := validateRollbackArtifacts(item); err != nil {
				return markRestoreRecoveryRequired(path, journal, err)
			}
		}
		if err := writeRestoreJournal(path, journal, "ROLLING_BACK", restoreDecisionRollback, journal.Changed, journal.Next); err != nil {
			return failure("UPGRADE_RECOVERY_REQUIRED", err)
		}
		for index := len(journal.Items) - 1; index >= 0; index-- {
			if err := rollbackRestoreItem(journal.Items[index]); err != nil {
				return failure("UPGRADE_RECOVERY_REQUIRED", err)
			}
		}
		if err := verifyJournalTargets(journal.Items, true); err != nil {
			return markRestoreRecoveryRequired(path, journal, err)
		}
		if err := writeRestoreJournal(path, journal, "ROLLED_BACK_CLEANING", restoreDecisionRollback, journal.Changed, journal.Next); err != nil {
			return failure("UPGRADE_RECOVERY_REQUIRED", err)
		}
	}
	if err := verifyJournalTargets(journal.Items, true); err != nil {
		return markRestoreRecoveryRequired(path, journal, err)
	}
	if err := cleanupRestoreArtifacts(journal.Items, true); err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if err := removeBoundRestoreJournal(path, journal); err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return nil
}

func recoverCommittedJournal(path string, journal *restoreJournal) error {
	if err := verifyJournalTargets(journal.Items, false); err != nil {
		return markRestoreRecoveryRequired(path, journal, err)
	}
	if journal.Phase == "COMMITTED" {
		for _, item := range journal.Items {
			if err := validateCommittedArtifacts(item); err != nil {
				return markRestoreRecoveryRequired(path, journal, err)
			}
		}
	}
	return finishCommittedRestore(path, journal)
}

func validateRollbackArtifacts(item restoreJournalItem) error {
	target, err := inspectRestorePath(item.Target)
	if err != nil {
		return err
	}
	staged, err := inspectRestorePath(item.Staged)
	if err != nil {
		return err
	}
	backup, err := inspectRestorePath(item.Backup)
	if err != nil {
		return err
	}
	discard, err := inspectRestorePath(item.Discard)
	if err != nil {
		return err
	}
	if staged.Present && !restoreIdentityEqual(staged, item.Desired) {
		return errors.New("restore stage identity is ambiguous")
	}
	if discard.Present && !restoreIdentityEqual(discard, item.Desired) {
		return errors.New("restore discard identity is ambiguous")
	}
	if backup.Present && (!item.Original.Present || !restoreIdentityEqual(backup, item.Original)) {
		return errors.New("restore backup identity is ambiguous")
	}
	if target.Present && !restoreIdentityEqual(target, item.Original) && !restoreIdentityEqual(target, item.Desired) {
		return errors.New("restore target identity is ambiguous")
	}
	if target.Present && restoreIdentityEqual(target, item.Desired) && discard.Present {
		return errors.New("restore desired generation exists twice")
	}
	if item.Original.Present && !target.Present && !backup.Present {
		return errors.New("original restore target is unavailable")
	}
	return nil
}

func validateCommittedArtifacts(item restoreJournalItem) error {
	staged, err := inspectRestorePath(item.Staged)
	if err != nil {
		return err
	}
	if staged.Present && !restoreIdentityEqual(staged, item.Desired) {
		return errors.New("committed restore stage identity is ambiguous")
	}
	backup, err := inspectRestorePath(item.Backup)
	if err != nil {
		return err
	}
	if backup.Present && !restoreIdentityEqual(backup, item.Original) {
		return errors.New("committed restore backup identity is ambiguous")
	}
	if !item.Original.Present && backup.Present {
		return errors.New("unexpected committed restore backup")
	}
	discard, err := inspectRestorePath(item.Discard)
	if err != nil {
		return err
	}
	if discard.Present {
		return errors.New("unexpected committed restore discard")
	}
	return nil
}

func rollbackRestoreItem(item restoreJournalItem) error {
	target, err := inspectRestorePath(item.Target)
	if err != nil {
		return err
	}
	backup, err := inspectRestorePath(item.Backup)
	if err != nil {
		return err
	}
	discard, err := inspectRestorePath(item.Discard)
	if err != nil {
		return err
	}
	if restoreIdentityEqual(target, item.Original) {
		return nil
	}
	if target.Present {
		if !restoreIdentityEqual(target, item.Desired) || discard.Present {
			return errors.New("restore target changed during rollback")
		}
		if err := renameRestorePath(item.Target, item.Discard); err != nil {
			return err
		}
		discard = item.Desired
		target = restorePathIdentity{}
	}
	if item.Original.Present {
		if target.Present || !restoreIdentityEqual(backup, item.Original) {
			return errors.New("restore backup changed during rollback")
		}
		return renameRestorePath(item.Backup, item.Target)
	}
	if backup.Present || target.Present || discard.Present && !restoreIdentityEqual(discard, item.Desired) {
		return errors.New("absent original restore state is ambiguous")
	}
	return nil
}

func cleanupRestoreArtifacts(items []restoreJournalItem, allowPartial bool) error {
	var failures []error
	for _, item := range items {
		if err := removeRestoreArtifact(item.Staged, item.Desired, allowPartial); err != nil {
			failures = append(failures, err)
		}
		if err := removeRestoreArtifact(item.Backup, item.Original, allowPartial); err != nil {
			failures = append(failures, err)
		}
		if err := removeRestoreArtifact(item.Discard, item.Desired, allowPartial); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func removeRestoreArtifact(path string, expected restorePathIdentity, allowPartial bool) error {
	if !allowPartial {
		return removeVerifiedRestorePath(path, expected)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return errors.New("restore cleanup artifact is unsafe")
	}
	if err := removeRestorePath(path); err != nil {
		return fmt.Errorf("remove restore artifact %s: %w", path, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore artifact remains after cleanup: %s", path)
	}
	return nil
}

func removeVerifiedRestorePath(path string, expected restorePathIdentity) error {
	actual, err := inspectRestorePath(path)
	if err != nil || !actual.Present {
		return err
	}
	if !restoreIdentityEqual(actual, expected) {
		return fmt.Errorf("restore artifact identity changed: %s", path)
	}
	if err := removeRestorePath(path); err != nil {
		return fmt.Errorf("remove restore artifact %s: %w", path, err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("restore artifact remains after cleanup: %s", path)
	}
	return nil
}

func verifyJournalTargets(items []restoreJournalItem, original bool) error {
	for _, item := range items {
		expected := item.Desired
		if original {
			expected = item.Original
		}
		if err := verifyRestorePath(item.Target, expected); err != nil {
			return err
		}
	}
	return nil
}

func verifyRestorePath(path string, expected restorePathIdentity) error {
	actual, err := inspectRestorePath(path)
	if err != nil {
		return err
	}
	if !restoreIdentityEqual(actual, expected) {
		return fmt.Errorf("restore path identity mismatch: %s", path)
	}
	return nil
}

func inspectRestorePath(path string) (restorePathIdentity, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return restorePathIdentity{}, nil
	}
	if err != nil {
		return restorePathIdentity{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() && !info.Mode().IsRegular() {
		return restorePathIdentity{}, errors.New("restore path is symbolic or non-regular")
	}
	identity := restorePathIdentity{Present: true, Directory: info.IsDir(), Mode: uint32(info.Mode().Perm())}
	if info.IsDir() {
		identity.SHA256, err = snapshotTreeHash(path)
		if err == nil {
			identity.Size, err = snapshotTreeSize(path)
		}
	} else {
		identity.Size = info.Size()
		identity.SHA256, err = safeio.FileSHA256(path)
	}
	return identity, err
}

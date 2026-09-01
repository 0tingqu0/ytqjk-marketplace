package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	restoreJournalSchema    = "ytqjk-snapshot-restore-journal/v2"
	restoreDecisionCommit   = "COMMIT"
	restoreDecisionRollback = "ROLLBACK"
)

var (
	renameRestorePath       = os.Rename
	removeRestorePath       = os.RemoveAll
	writeRestoreJournalFile = writeRestoreJournalBound
)

type restoreItem struct {
	Target       string
	Source       string
	Present      bool
	Directory    bool
	Mode         os.FileMode
	Size         int64
	ExpectedHash string
	staged       string
	backup       string
	stagedReady  bool
	backupReady  bool
	installed    bool
}

type restorePathIdentity struct {
	Present   bool   `json:"present"`
	Directory bool   `json:"directory,omitempty"`
	Mode      uint32 `json:"mode,omitempty"`
	Size      int64  `json:"size,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

type restoreJournalItem struct {
	Target   string              `json:"target"`
	Staged   string              `json:"staged"`
	Backup   string              `json:"backup"`
	Discard  string              `json:"discard"`
	Original restorePathIdentity `json:"original"`
	Desired  restorePathIdentity `json:"desired"`
}

type restoreJournal struct {
	Schema         string               `json:"schema"`
	SnapshotID     string               `json:"snapshot_id"`
	ManifestSHA256 string               `json:"snapshot_manifest_sha256"`
	Phase          string               `json:"phase"`
	Decision       string               `json:"decision"`
	Changed        int                  `json:"changed"`
	Next           int                  `json:"next"`
	RuntimeRoot    string               `json:"runtime_root"`
	CodexRoot      string               `json:"codex_root"`
	KnowledgeRoot  string               `json:"knowledge_root"`
	Items          []restoreJournalItem `json:"items"`
	UpdatedAt      time.Time            `json:"updated_at"`
	bindingSHA256  string
	bindingFile    os.FileInfo
	guard          *restoreGuard
}

func restoreSnapshot(plan Plan, snapshot Snapshot, restoreData bool) (returned error) {
	guard, err := acquireRestoreGuard(plan)
	if err != nil {
		return err
	}
	defer func() { returned = errors.Join(returned, guard.release()) }()
	return restoreSnapshotGuarded(plan, snapshot, restoreData, guard)
}

func restoreSnapshotGuarded(plan Plan, snapshot Snapshot, restoreData bool, guard *restoreGuard) error {
	if err := guard.require(plan); err != nil {
		return err
	}
	if !hexDigestPattern.MatchString(snapshot.ManifestSHA256) {
		return failure("UPGRADE_SNAPSHOT_INVALID", errors.New("snapshot manifest digest is required"))
	}
	if err := recoverPendingRestoreJournalsGuarded(plan, guard); err != nil {
		return err
	}
	loaded, err := readSnapshot(plan.RuntimeRoot, snapshot.ID)
	if err != nil {
		return err
	}
	if loaded.ManifestSHA256 != snapshot.ManifestSHA256 {
		return failure("UPGRADE_SNAPSHOT_INVALID", errors.New("snapshot manifest digest mismatch"))
	}
	items, err := snapshotRestoreItems(plan, loaded, restoreData)
	if err != nil {
		return err
	}
	err = transactionalSnapshotRestore(plan, loaded.ID, loaded.ManifestSHA256, items, guard)
	if err == nil || errorCodeOf(err) == "UPGRADE_SNAPSHOT_RESTORE_ROLLED_BACK" || !safeio.WasCommitted(err) {
		return err
	}
	return resolveCommittedRestoreError(plan, loaded.ID, loaded.ManifestSHA256, err, guard)
}

func snapshotRestoreItems(plan Plan, snapshot Snapshot, restoreData bool) ([]restoreItem, error) {
	root := snapshotRoot(plan.RuntimeRoot, snapshot.ID)
	var items []restoreItem
	for _, item := range snapshot.Items {
		if item.Class != snapshotClassActive && !restoreData {
			continue
		}
		target, err := snapshotTargetPath(plan, item)
		if err != nil {
			return nil, err
		}
		source, err := snapshotStoredPath(root, item)
		if err != nil {
			return nil, err
		}
		mode := os.FileMode(item.Mode)
		if mode == 0 {
			mode = fileModeFor(target)
		}
		items = append(items, restoreItem{
			Target: target, Source: source, Present: item.Present, Directory: item.Kind == snapshotKindTree,
			Mode: mode, Size: item.Size, ExpectedHash: item.SHA256,
		})
		if item.Kind == snapshotKindSQLite {
			for _, suffix := range []string{"-shm", "-wal"} {
				items = append(items, restoreItem{Target: target + suffix})
			}
		}
	}
	sort.Slice(items, func(left, right int) bool {
		leftKey, _ := restorePathKey(items[left].Target)
		rightKey, _ := restorePathKey(items[right].Target)
		return leftKey < rightKey
	})
	for index := 1; index < len(items); index++ {
		if sameRestorePath(items[index-1].Target, items[index].Target) {
			return nil, errors.New("duplicate snapshot restore target")
		}
	}
	if err := validateRestoreTopology(plan, items); err != nil {
		return nil, err
	}
	return items, nil
}

func transactionalSnapshotRestore(
	plan Plan, identifier, manifestSHA256 string, items []restoreItem, guard *restoreGuard,
) error {
	journalPath := restoreJournalPath(plan.RuntimeRoot, identifier)
	exists, err := restoreJournalExists(guard, journalPath)
	if err != nil {
		return err
	}
	if exists {
		return failure("UPGRADE_RECOVERY_REQUIRED", errors.New("snapshot restore journal already exists"))
	}
	journal, err := initializeRestoreJournal(plan, identifier, manifestSHA256, items, guard)
	if err != nil {
		return err
	}
	if err := writeRestoreJournal(journalPath, &journal, "PREPARING", restoreDecisionRollback, -1, 0); err != nil {
		return err
	}
	if err := prepareSnapshotRestoreItems(items, journal.Items); err != nil {
		return rollbackFailedRestore(journalPath, &journal, err)
	}
	if err := writeRestoreJournal(journalPath, &journal, "PREPARED", restoreDecisionRollback, -1, 0); err != nil {
		return rollbackFailedRestore(journalPath, &journal, err)
	}
	for index := range journal.Items {
		if err := writeRestoreJournal(journalPath, &journal, "SWAPPING", restoreDecisionRollback, index-1, index); err != nil {
			return rollbackFailedRestore(journalPath, &journal, err)
		}
		item := journal.Items[index]
		if item.Original.Present {
			if err := renameRestorePath(item.Target, item.Backup); err != nil {
				return rollbackFailedRestore(journalPath, &journal, err)
			}
		}
		if item.Desired.Present {
			if err := renameRestorePath(item.Staged, item.Target); err != nil {
				return rollbackFailedRestore(journalPath, &journal, err)
			}
		}
		if err := writeRestoreJournal(journalPath, &journal, "SWAPPING", restoreDecisionRollback, index, index+1); err != nil {
			return rollbackFailedRestore(journalPath, &journal, err)
		}
	}
	if err := verifyJournalTargets(journal.Items, false); err != nil {
		return rollbackFailedRestore(journalPath, &journal, err)
	}
	if err := writeRestoreJournal(journalPath, &journal, "COMMITTED", restoreDecisionCommit, len(items)-1, len(items)); err != nil {
		if safeio.WasCommitted(err) {
			return &safeio.PostCommitError{Operation: "snapshot restore commit journal", Err: err}
		}
		return rollbackFailedRestore(journalPath, &journal, err)
	}
	return finishCommittedRestore(journalPath, &journal)
}

func finishCommittedRestore(path string, journal *restoreJournal) error {
	if err := verifyJournalTargets(journal.Items, false); err != nil {
		return markRestoreRecoveryRequired(path, journal, err)
	}
	if journal.Phase != "COMMITTED_CLEANING" {
		if err := writeRestoreJournal(path, journal, "COMMITTED_CLEANING", restoreDecisionCommit, journal.Changed, journal.Next); err != nil {
			return &safeio.PostCommitError{Operation: "snapshot restore cleanup journal", Err: err}
		}
	}
	if err := cleanupRestoreArtifacts(journal.Items, true); err != nil {
		return &safeio.PostCommitError{Operation: "snapshot restore cleanup", Err: err}
	}
	if err := verifyJournalTargets(journal.Items, false); err != nil {
		return markRestoreRecoveryRequired(path, journal, err)
	}
	if err := removeBoundRestoreJournal(path, journal); err != nil {
		return &safeio.PostCommitError{Operation: "snapshot restore journal cleanup", Err: err}
	}
	if err := verifyJournalTargets(journal.Items, false); err != nil {
		return &safeio.PostCommitError{Operation: "snapshot restore final verification", Err: err}
	}
	return nil
}

func prepareSnapshotRestoreItems(items []restoreItem, journalItems []restoreJournalItem) error {
	if err := verifyJournalTargets(journalItems, true); err != nil {
		return err
	}
	for index := range items {
		item := &items[index]
		journalItem := journalItems[index]
		if !item.Present {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(item.Target), 0o700); err != nil {
			return err
		}
		if err := materializeRestoreStage(item); err != nil {
			return err
		}
		if err := verifyRestorePath(item.staged, journalItem.Desired); err != nil {
			cleanupErr := removeRestorePath(item.staged)
			return errors.Join(fmt.Errorf("verify snapshot restore stage: %w", err), cleanupErr)
		}
	}
	return verifyJournalTargets(journalItems, true)
}

func rollbackFailedRestore(path string, journal *restoreJournal, cause error) error {
	writeErr := writeRestoreJournal(path, journal, "ROLLING_BACK", restoreDecisionRollback, journal.Changed, journal.Next)
	if writeErr != nil {
		return errors.Join(cause, failure("UPGRADE_RECOVERY_REQUIRED", writeErr))
	}
	if err := recoverRollbackJournal(path, journal); err != nil {
		return errors.Join(cause, err)
	}
	return failure("UPGRADE_SNAPSHOT_RESTORE_ROLLED_BACK", cause)
}

// transactionalRestore remains the activation-only single-process replacement.
// Durable snapshot data uses transactionalSnapshotRestore and its v2 journal.
func transactionalRestore(identifier string, items []restoreItem) error {
	if err := validateStandaloneRestoreTopology(items); err != nil {
		return err
	}
	if err := prepareRestoreItems(identifier, items); err != nil {
		return errors.Join(err, cleanupPreparedRestore(items))
	}
	changed := -1
	for index := range items {
		item := &items[index]
		changed = index
		if _, err := os.Lstat(item.Target); err == nil {
			if err := renameRestorePath(item.Target, item.backup); err != nil {
				return reverseRestore(items, changed, err)
			}
			item.backupReady = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return reverseRestore(items, changed, err)
		}
		if item.Present {
			if err := renameRestorePath(item.staged, item.Target); err != nil {
				return reverseRestore(items, changed, err)
			}
			item.installed = true
		}
	}
	if err := cleanupPreparedRestore(items); err != nil {
		return &safeio.PostCommitError{Operation: "activation restore", Err: err}
	}
	return nil
}

func prepareRestoreItems(identifier string, items []restoreItem) error {
	for index := range items {
		item := &items[index]
		if err := os.MkdirAll(filepath.Dir(item.Target), 0o700); err != nil {
			return err
		}
		if info, err := os.Lstat(item.Target); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return errors.New("restore target is a symbolic link")
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		item.staged, item.backup = restoreArtifactPaths(identifier, item.Target)
		for _, path := range []string{item.staged, item.backup} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				return errors.New("restore staging path already exists")
			}
		}
		if !item.Present {
			continue
		}
		if item.Mode == 0 {
			item.Mode = fileModeFor(item.Target)
		}
		if err := materializeRestoreStage(item); err != nil {
			return err
		}
		item.stagedReady = true
	}
	return nil
}

func materializeRestoreStage(item *restoreItem) error {
	var err error
	if item.Directory {
		err = snapshotCopyTree(item.Source, item.staged)
	} else {
		err = safeio.CopyFile(item.Source, item.staged, item.Mode.Perm())
		if err == nil {
			err = os.Chmod(item.staged, item.Mode.Perm())
		}
	}
	if err == nil {
		return nil
	}
	cleanupErr := removeRestorePath(item.staged)
	if errors.Is(cleanupErr, os.ErrNotExist) {
		cleanupErr = nil
	}
	return errors.Join(err, cleanupErr)
}

func reverseRestore(items []restoreItem, changed int, cause error) error {
	var failures []error
	for index := changed; index >= 0; index-- {
		item := items[index]
		if item.installed {
			if err := removeRestorePath(item.Target); err != nil {
				failures = append(failures, err)
				continue
			}
		}
		if item.backupReady {
			if err := renameRestorePath(item.backup, item.Target); err != nil {
				failures = append(failures, err)
			}
		}
	}
	if len(failures) != 0 {
		return errors.Join(append([]error{cause}, failures...)...)
	}
	if err := cleanupPreparedRestore(items); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(append([]error{cause}, failures...)...)
}

func cleanupPreparedRestore(items []restoreItem) error {
	var failures []error
	for _, item := range items {
		paths := []string{}
		if item.stagedReady {
			paths = append(paths, item.staged)
		}
		if item.backupReady {
			paths = append(paths, item.backup)
		}
		for _, path := range paths {
			if path == "" {
				continue
			}
			if err := removeRestorePath(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				failures = append(failures, fmt.Errorf("remove restore artifact %s: %w", path, err))
			}
		}
	}
	return errors.Join(failures...)
}

func restoreArtifactPaths(identifier, target string) (string, string) {
	token := safeio.SHA256([]byte(target))[:12]
	directory := filepath.Dir(target)
	return filepath.Join(directory, ".ytqjk-restore-stage-"+identifier+"-"+token),
		filepath.Join(directory, ".ytqjk-restore-backup-"+identifier+"-"+token)
}

func restoreDiscardPath(identifier, target string) string {
	token := safeio.SHA256([]byte(target))[:12]
	return filepath.Join(filepath.Dir(target), ".ytqjk-restore-discard-"+identifier+"-"+token)
}

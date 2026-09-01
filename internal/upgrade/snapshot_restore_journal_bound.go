package upgrade

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func (guard *restoreGuard) journalName(path string) (string, error) {
	if err := guard.requireRuntime(guard.root); err != nil {
		return "", err
	}
	if !sameRestorePath(filepath.Dir(path), guard.upgrade.path) {
		return "", failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal escapes bound upgrade directory"))
	}
	name := filepath.Base(path)
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return "", failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore journal name is invalid"))
	}
	return name, nil
}

func (guard *restoreGuard) openJournal(path string, writable bool) (*os.File, error) {
	name, err := guard.journalName(path)
	if err != nil {
		return nil, err
	}
	file, err := openRestoreRegularAtNoFollow(guard.upgrade.directory, name, writable)
	if err != nil {
		return nil, err
	}
	if err := guard.requireRuntime(guard.root); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	return file, nil
}

func (guard *restoreGuard) verifyJournalHandle(path string, opened os.FileInfo) error {
	current, err := guard.openJournal(path, false)
	if err != nil {
		return err
	}
	currentInfo, statErr := current.Stat()
	closeErr := current.Close()
	if statErr != nil || closeErr != nil || opened == nil || !os.SameFile(opened, currentInfo) {
		return failure(
			"UPGRADE_RECOVERY_REQUIRED",
			errors.Join(errors.New("restore journal entry identity changed"), statErr, closeErr),
		)
	}
	return guard.requireRuntime(guard.root)
}

func (guard *restoreGuard) readUpgradeDirectory() ([]os.DirEntry, error) {
	if err := guard.requireRuntime(guard.root); err != nil {
		return nil, err
	}
	directory, err := guard.upgrade.root.Open(".")
	if err != nil {
		return nil, err
	}
	identity, identityErr := restoreHandleIdentity(directory)
	if identityErr != nil || identity != guard.bootstrap.DirectoryIdentity {
		return nil, errors.Join(
			failure("UPGRADE_RECOVERY_REQUIRED", errors.Join(errors.New("restore scan directory changed"), identityErr)),
			directory.Close(),
		)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	return entries, guard.requireRuntime(guard.root)
}

func restoreJournalExists(guard *restoreGuard, path string) (bool, error) {
	file, err := guard.openJournal(path, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, guard.requireRuntime(guard.root)
	}
	if err != nil {
		return false, err
	}
	return true, errors.Join(file.Close(), guard.requireRuntime(guard.root))
}

func writeRestoreJournalBound(guard *restoreGuard, path string, value any) error {
	name, err := guard.journalName(path)
	if err != nil {
		return err
	}
	if err := writeRestoreBoundJSON(guard.upgrade, name, value); err != nil {
		return err
	}
	return guard.requireRuntime(guard.root)
}

func consumeRestoreJournalBound(guard *restoreGuard, source, target string) error {
	sourceName, err := guard.journalName(source)
	if err != nil {
		return err
	}
	targetName, err := guard.journalName(target)
	if err != nil {
		return err
	}
	if exists, err := restoreJournalExists(guard, target); err != nil || exists {
		return errors.Join(errors.New("restore journal consumption target exists"), err)
	}
	if err := guard.upgrade.root.Rename(sourceName, targetName); err != nil {
		return err
	}
	consumed, openErr := guard.openJournal(target, true)
	if openErr != nil {
		return &safeio.PostCommitError{Operation: "restore journal consumption", Err: openErr}
	}
	syncErr := consumed.Sync()
	closeErr := consumed.Close()
	directoryErr := syncRestoreDirectory(guard.upgrade.directory)
	identityErr := guard.requireRuntime(guard.root)
	if err := errors.Join(syncErr, closeErr, directoryErr, identityErr); err != nil {
		return &safeio.PostCommitError{Operation: "restore journal consumption", Err: err}
	}
	return nil
}

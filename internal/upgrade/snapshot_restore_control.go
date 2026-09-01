package upgrade

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	restoreBootstrapSchema   = "ytqjk-restore-bootstrap/v1"
	restoreBootstrapPending  = "PENDING"
	restoreBootstrapComplete = "COMPLETE"
	restoreBootstrapName     = ".ytqjk-restore.bootstrap"
	restoreGuardName         = "restore.guard"
)

type restoreBootstrapRecord struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	RuntimeIdentity   string `json:"runtime_identity,omitempty"`
	DirectoryIdentity string `json:"directory_identity,omitempty"`
	GuardIdentity     string `json:"guard_identity,omitempty"`
}

type restoreBoundRoot struct {
	path      string
	name      string
	parent    *restoreBoundRoot
	root      *os.Root
	directory *os.File
	identity  string
}

func openRestoreBoundRoot(path, expected string) (*restoreBoundRoot, error) {
	directory, err := openRestoreDirectoryNoFollow(path)
	if err != nil {
		return nil, err
	}
	identity, err := restoreHandleIdentity(directory)
	if err != nil || expected != "" && identity != expected {
		return nil, errors.Join(errors.New("restore directory identity changed"), err, directory.Close())
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	bound := &restoreBoundRoot{path: path, root: root, directory: directory, identity: identity}
	if err := bound.verify(); err != nil {
		return nil, errors.Join(err, bound.close())
	}
	return bound, nil
}

func openRestoreBoundChild(parent *restoreBoundRoot, name, expected string) (*restoreBoundRoot, error) {
	if parent == nil || parent.directory == nil || parent.root == nil {
		return nil, errors.New("restore parent directory is unavailable")
	}
	directory, err := openRestoreDirectoryAtNoFollow(parent.directory, name)
	if err != nil {
		return nil, err
	}
	identity, err := restoreHandleIdentity(directory)
	if err != nil || expected != "" && identity != expected {
		return nil, errors.Join(errors.New("restore directory identity changed"), err, directory.Close())
	}
	root, err := parent.root.OpenRoot(name)
	if err != nil {
		return nil, errors.Join(err, directory.Close())
	}
	bound := &restoreBoundRoot{
		path: filepath.Join(parent.path, name), name: name, parent: parent,
		root: root, directory: directory, identity: identity,
	}
	if err := bound.verify(); err != nil {
		return nil, errors.Join(err, bound.close())
	}
	return bound, nil
}

func (bound *restoreBoundRoot) verify() error {
	if bound == nil || bound.root == nil || bound.directory == nil || bound.identity == "" {
		return errors.New("restore bound directory is unavailable")
	}
	identity, err := restoreHandleIdentity(bound.directory)
	if err != nil || identity != bound.identity {
		return errors.Join(errors.New("restore directory handle identity changed"), err)
	}
	opened, err := bound.root.Open(".")
	if err != nil {
		return err
	}
	openedIdentity, identityErr := restoreHandleIdentity(opened)
	closeErr := opened.Close()
	if identityErr != nil || closeErr != nil || openedIdentity != bound.identity {
		return errors.Join(errors.New("restore root handle identity changed"), identityErr, closeErr)
	}
	var current *os.File
	if bound.parent == nil {
		current, err = openRestoreDirectoryNoFollow(bound.path)
	} else {
		if err := bound.parent.verify(); err != nil {
			return err
		}
		current, err = openRestoreDirectoryAtNoFollow(bound.parent.directory, bound.name)
	}
	if err != nil {
		return err
	}
	currentIdentity, identityErr := restoreHandleIdentity(current)
	closeErr = current.Close()
	if identityErr != nil || closeErr != nil || currentIdentity != bound.identity {
		return errors.Join(errors.New("restore directory entry identity changed"), identityErr, closeErr)
	}
	return nil
}

func (bound *restoreBoundRoot) close() error {
	if bound == nil {
		return nil
	}
	var directoryErr, rootErr error
	if bound.directory != nil {
		directoryErr = bound.directory.Close()
		bound.directory = nil
	}
	if bound.root != nil {
		rootErr = bound.root.Close()
		bound.root = nil
	}
	return errors.Join(directoryErr, rootErr)
}

func ensureRestoreBootstrap(runtimeRoot *restoreBoundRoot) (restoreBootstrapRecord, error) {
	record, err := readRestoreBootstrap(runtimeRoot)
	if err == nil {
		return record, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return restoreBootstrapRecord{}, err
	}
	marker, err := runtimeRoot.root.OpenFile(restoreBootstrapName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if errors.Is(err, os.ErrExist) {
		return readRestoreBootstrap(runtimeRoot)
	}
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	defer marker.Close()
	pending := restoreBootstrapRecord{Schema: restoreBootstrapSchema, Status: restoreBootstrapPending}
	if err := replaceRestoreBootstrap(marker, pending); err != nil {
		return restoreBootstrapRecord{}, err
	}
	if err := syncRestoreDirectory(runtimeRoot.directory); err != nil {
		return restoreBootstrapRecord{}, err
	}
	record, err = bootstrapRestoreControl(runtimeRoot)
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	if err := replaceRestoreBootstrap(marker, record); err != nil {
		return restoreBootstrapRecord{}, err
	}
	observed, err := readRestoreBootstrap(runtimeRoot)
	if err != nil || observed != record {
		return restoreBootstrapRecord{}, errors.Join(errors.New("restore bootstrap exact readback failed"), err)
	}
	return record, nil
}

func bootstrapRestoreControlRoot(runtimePath string) (returned error) {
	runtimeRoot, err := openRestoreBoundRoot(runtimePath, "")
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	defer func() { returned = errors.Join(returned, runtimeRoot.close()) }()
	_, err = ensureRestoreBootstrap(runtimeRoot)
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return nil
}

func bootstrapRestoreControl(runtimeRoot *restoreBoundRoot) (restoreBootstrapRecord, error) {
	_, err := runtimeRoot.root.Lstat("upgrade")
	if errors.Is(err, os.ErrNotExist) {
		if err := runtimeRoot.root.Mkdir("upgrade", 0o700); err != nil {
			return restoreBootstrapRecord{}, err
		}
	} else if err != nil {
		return restoreBootstrapRecord{}, err
	}
	upgrade, err := openRestoreBoundChild(runtimeRoot, "upgrade", "")
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	defer upgrade.close()
	guard, err := openRestoreRegularAtNoFollow(upgrade.directory, restoreGuardName, true)
	if errors.Is(err, os.ErrNotExist) {
		guard, err = upgrade.root.OpenFile(restoreGuardName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	}
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	if err := guard.Sync(); err != nil {
		return restoreBootstrapRecord{}, errors.Join(err, guard.Close())
	}
	guardIdentity, identityErr := restoreHandleIdentity(guard)
	closeErr := guard.Close()
	if identityErr != nil || closeErr != nil {
		return restoreBootstrapRecord{}, errors.Join(identityErr, closeErr)
	}
	if err := errors.Join(syncRestoreDirectory(upgrade.directory), syncRestoreDirectory(runtimeRoot.directory)); err != nil {
		return restoreBootstrapRecord{}, err
	}
	return restoreBootstrapRecord{
		Schema: restoreBootstrapSchema, Status: restoreBootstrapComplete,
		RuntimeIdentity: runtimeRoot.identity, DirectoryIdentity: upgrade.identity, GuardIdentity: guardIdentity,
	}, nil
}

func readRestoreBootstrap(runtimeRoot *restoreBoundRoot) (restoreBootstrapRecord, error) {
	file, err := openRestoreRegularAtNoFollow(runtimeRoot.directory, restoreBootstrapName, false)
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	opened, statErr := file.Stat()
	data, readErr := io.ReadAll(io.LimitReader(file, 64<<10))
	closeErr := file.Close()
	if readErr == nil && len(data) == 64<<10 {
		readErr = errors.New("restore bootstrap record is too large")
	}
	if statErr != nil || readErr != nil || closeErr != nil {
		return restoreBootstrapRecord{}, errors.Join(statErr, readErr, closeErr)
	}
	current, err := openRestoreRegularAtNoFollow(runtimeRoot.directory, restoreBootstrapName, false)
	if err != nil {
		return restoreBootstrapRecord{}, err
	}
	currentInfo, currentStatErr := current.Stat()
	currentCloseErr := current.Close()
	if currentStatErr != nil || currentCloseErr != nil || !os.SameFile(opened, currentInfo) {
		return restoreBootstrapRecord{}, errors.Join(
			errors.New("restore bootstrap identity changed while reading"), currentStatErr, currentCloseErr,
		)
	}
	var record restoreBootstrapRecord
	if err := decodeStrictJSON(data, &record); err != nil {
		return restoreBootstrapRecord{}, err
	}
	if record.Schema != restoreBootstrapSchema || record.Status != restoreBootstrapComplete ||
		len(record.RuntimeIdentity) != 33 || len(record.DirectoryIdentity) != 33 || len(record.GuardIdentity) != 33 {
		return restoreBootstrapRecord{}, errors.New("restore bootstrap proof is incomplete or invalid")
	}
	if err := runtimeRoot.verify(); err != nil || runtimeRoot.identity != record.RuntimeIdentity {
		return restoreBootstrapRecord{}, errors.Join(errors.New("restore runtime identity changed"), err)
	}
	return record, nil
}

func replaceRestoreBootstrap(file *os.File, record restoreBootstrapRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func writeRestoreBoundJSON(bound *restoreBoundRoot, name string, value any) (result error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	token, err := safeio.RandomHex(12)
	if err != nil {
		return err
	}
	temporaryName := ".restore-" + token + ".tmp"
	temporary, err := bound.root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = bound.root.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := bound.root.Rename(temporaryName, name); err != nil {
		return err
	}
	committed = true
	if err := syncRestoreDirectory(bound.directory); err != nil {
		return &safeio.PostCommitError{Operation: "restore journal replacement", Err: err}
	}
	return bound.verify()
}

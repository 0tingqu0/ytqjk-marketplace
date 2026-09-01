package maintenance

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const recordFileName = "record.json"

var openControlRoot = os.OpenRoot

type boundRoot struct {
	root      *os.Root
	directory *os.File
	identity  string
}

func verifyControlDirectory(control controlPlane) error {
	if err := verifyDirectoryIdentity(control.directory, control.directoryID); err != nil {
		return err
	}
	return verifyResourceBindings(control)
}

func verifyDirectoryIdentity(path, expected string) error {
	identity, err := directoryIdentity(path)
	if err != nil || identity != expected {
		return fail(
			CodeStateCorrupt,
			errors.Join(errors.New("maintenance control directory identity changed"), err),
		)
	}
	return nil
}

func openBoundRoot(path, expected string) (*boundRoot, error) {
	if err := verifyDirectoryIdentity(path, expected); err != nil {
		return nil, err
	}
	root, err := openControlRoot(path)
	if err != nil {
		return nil, fail(CodeStateCorrupt, err)
	}
	directory, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, fail(CodeStateCorrupt, err)
	}
	identity, err := fileHandleIdentity(directory)
	if err != nil || identity != expected {
		closeErr := errors.Join(directory.Close(), root.Close())
		return nil, fail(
			CodeStateCorrupt,
			errors.Join(errors.New("opened maintenance root identity does not match"), err, closeErr),
		)
	}
	return &boundRoot{root: root, directory: directory, identity: identity}, nil
}

func openCurrentBoundRoot(path string) (*boundRoot, error) {
	identity, err := directoryIdentity(path)
	if err != nil {
		return nil, fail(CodeStateCorrupt, err)
	}
	return openBoundRoot(path, identity)
}

func (bound *boundRoot) close() error {
	if bound == nil {
		return nil
	}
	return errors.Join(bound.directory.Close(), bound.root.Close())
}

func validateBoundName(name string) error {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return errors.New("maintenance control entry name is invalid")
	}
	return nil
}

func readBoundRegularFile(control controlPlane, name string) ([]byte, error) {
	if err := validateBoundName(name); err != nil {
		return nil, err
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return nil, err
	}
	file, err := openRootRegularFileNoFollow(bound.directory, name, false)
	if err != nil {
		return nil, errors.Join(err, bound.close())
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4<<20))
	closeErr := file.Close()
	if readErr == nil && len(data) == 4<<20 {
		readErr = errors.New("maintenance control file is too large")
	}
	return data, errors.Join(readErr, closeErr, verifyControlDirectory(control), bound.close())
}

func readRootRegularFile(directory, name string) ([]byte, error) {
	if err := validateBoundName(name); err != nil {
		return nil, err
	}
	bound, err := openCurrentBoundRoot(directory)
	if err != nil {
		return nil, err
	}
	file, err := openRootRegularFileNoFollow(bound.directory, name, false)
	if err != nil {
		return nil, errors.Join(err, bound.close())
	}
	data, readErr := io.ReadAll(io.LimitReader(file, 4<<20))
	closeErr := file.Close()
	if readErr == nil && len(data) == 4<<20 {
		readErr = errors.New("maintenance control file is too large")
	}
	return data, errors.Join(readErr, closeErr, bound.close())
}

func writeBoundJSON(control controlPlane, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return atomicWriteBound(control, recordFileName, append(data, '\n'))
}

func atomicWriteBound(control controlPlane, name string, data []byte) (result error) {
	if err := validateBoundName(name); err != nil {
		return err
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		closeErr := bound.close()
		if closeErr == nil {
			return
		}
		if committed {
			result = errors.Join(result, &safeio.PostCommitError{
				Operation: "maintenance bound root close", Err: closeErr,
			})
			return
		}
		result = errors.Join(result, closeErr)
	}()
	temporaryName, err := randomTemporaryName()
	if err != nil {
		return err
	}
	temporary, err := bound.root.OpenFile(temporaryName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = bound.root.Remove(temporaryName)
		}
	}()
	if _, err := temporary.Write(data); err != nil {
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
	if err := syncBoundDirectory(bound.root); err != nil {
		return &safeio.PostCommitError{Operation: "maintenance record replacement", Err: err}
	}
	if err := verifyControlDirectory(control); err != nil {
		return &safeio.PostCommitError{Operation: "maintenance control identity verification", Err: err}
	}
	return nil
}

func createBoundRegular(control controlPlane, name string) error {
	if err := validateBoundName(name); err != nil {
		return err
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return err
	}
	file, err := bound.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return errors.Join(err, bound.close())
	}
	return errors.Join(
		file.Sync(), file.Close(), syncBoundDirectory(bound.root),
		verifyControlDirectory(control), bound.close(),
	)
}

func boundEntryExists(control controlPlane, name string) (bool, error) {
	if err := validateBoundName(name); err != nil {
		return false, err
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return false, err
	}
	file, err := openRootRegularFileNoFollow(bound.directory, name, false)
	if errors.Is(err, os.ErrNotExist) {
		return false, errors.Join(verifyControlDirectory(control), bound.close())
	}
	if err != nil {
		return false, errors.Join(err, bound.close())
	}
	return true, errors.Join(file.Close(), verifyControlDirectory(control), bound.close())
}

func boundEntryIdentity(control controlPlane, name string) (string, error) {
	if err := validateBoundName(name); err != nil {
		return "", err
	}
	bound, err := openBoundRoot(control.directory, control.directoryID)
	if err != nil {
		return "", err
	}
	file, err := openRootRegularFileNoFollow(bound.directory, name, false)
	if err != nil {
		return "", errors.Join(err, bound.close())
	}
	identity, identityErr := fileHandleIdentity(file)
	return identity, errors.Join(
		identityErr, file.Close(), verifyControlDirectory(control), bound.close(),
	)
}

func randomTemporaryName() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return ".maintenance-" + hex.EncodeToString(value) + ".tmp", nil
}

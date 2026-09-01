package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	bootstrapSchema   = "ytqjk-maintenance-bootstrap/v2"
	bootstrapPending  = "PENDING"
	bootstrapComplete = "COMPLETE"
	bootstrapFileName = ".maintenance.bootstrap.lock"
)

type bootstrapRecord struct {
	Schema            string `json:"schema"`
	Status            string `json:"status"`
	DirectoryIdentity string `json:"directory_identity"`
	GuardIdentity     string `json:"guard_identity"`
	WritersIdentity   string `json:"writers_identity"`
}

// BootstrapControlRoot is the only maintenance API allowed to create the
// canonical control root and its initial OPEN record. Repeated and concurrent
// calls are safe. The permanent .maintenance.bootstrap.lock is initialization
// proof only, never admission state. Once it exists, missing state is never
// recreated: an interrupted initialization permanently fails closed.
func BootstrapControlRoot(ctx context.Context, controlRoot string) error {
	if ctx == nil || strings.TrimSpace(controlRoot) == "" {
		return fail(CodeInvalid, errors.New("context and control root are required"))
	}
	absolute, err := secureMkdirAll(controlRoot)
	if err != nil {
		return fail(CodeStateCorrupt, err)
	}
	bound, err := openCurrentBoundRoot(absolute)
	if err != nil {
		return err
	}
	marker, openErr := bound.root.OpenFile(bootstrapFileName, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	err = errors.Join(openErr, bound.close())
	if errors.Is(err, os.ErrExist) {
		return waitForBootstrap(ctx, absolute)
	}
	if err != nil {
		return fail(CodeLockFailed, err)
	}
	defer marker.Close()
	if err := replaceBootstrapMarker(marker, bootstrapRecord{Schema: bootstrapSchema, Status: bootstrapPending}); err != nil {
		return fail(CodeLockFailed, err)
	}
	directoryID, guardID, writersID, err := bootstrapFresh(absolute)
	if err != nil {
		return err
	}
	complete := bootstrapRecord{
		Schema: bootstrapSchema, Status: bootstrapComplete,
		DirectoryIdentity: directoryID, GuardIdentity: guardID, WritersIdentity: writersID,
	}
	if err := replaceBootstrapMarker(marker, complete); err != nil {
		return fail(CodeLockFailed, err)
	}
	observed, err := readBootstrapRecord(absolute)
	if err != nil || observed != complete {
		return fail(
			CodeStateCorrupt,
			errors.Join(errors.New("maintenance bootstrap exact readback failed"), err),
		)
	}
	return validateBootstrappedControlRoot(absolute, observed)
}

func bootstrapFresh(controlRoot string) (string, string, string, error) {
	directory := filepath.Join(controlRoot, "maintenance")
	if _, err := os.Lstat(directory); err == nil {
		return "", "", "", fail(CodeStateCorrupt, errors.New("maintenance state predates bootstrap proof"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", "", fail(CodeStateCorrupt, err)
	}
	if err := os.Mkdir(directory, 0o700); err != nil {
		return "", "", "", fail(CodeLockFailed, err)
	}
	canonical, err := canonicalDirectory(directory)
	if err != nil || canonicalKey(canonical) != canonicalKey(directory) {
		return "", "", "", fail(
			CodeStateCorrupt, errors.Join(errors.New("maintenance control directory is unsafe"), err),
		)
	}
	directoryID, err := directoryIdentity(directory)
	if err != nil {
		return "", "", "", fail(CodeStateCorrupt, err)
	}
	control := controlPlane{
		root: controlRoot, directory: directory, directoryID: directoryID,
		guardPath:   filepath.Join(directory, "guard.lock"),
		writersPath: filepath.Join(directory, "writers.lock"),
		recordPath:  filepath.Join(directory, "record.json"),
	}
	if err := createBoundRegular(control, "guard.lock"); err != nil {
		return "", "", "", fail(CodeLockFailed, err)
	}
	if err := createBoundRegular(control, "writers.lock"); err != nil {
		return "", "", "", fail(CodeLockFailed, err)
	}
	guardID, err := boundEntryIdentity(control, "guard.lock")
	if err != nil {
		return "", "", "", fail(CodeStateCorrupt, err)
	}
	writersID, err := boundEntryIdentity(control, "writers.lock")
	if err != nil || guardID == writersID {
		return "", "", "", fail(
			CodeStateCorrupt, errors.Join(errors.New("maintenance lock identities are invalid"), err),
		)
	}
	control.guardID = guardID
	control.writersID = writersID
	now := clockNow()
	if err := writeInitialRecord(control, Record{
		Schema: recordSchema, State: StateOpen, Generation: 0,
		Revision: 1, UpdatedAt: now,
	}); err != nil {
		return "", "", "", err
	}
	return directoryID, guardID, writersID, nil
}

func waitForBootstrap(ctx context.Context, controlRoot string) error {
	deadline := time.Now().Add(defaultLockWait)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	for {
		marker, err := readBootstrapRecord(controlRoot)
		if err == nil && marker.Status == bootstrapComplete {
			return validateBootstrappedControlRoot(controlRoot, marker)
		}
		if !time.Now().Before(deadline) {
			return fail(CodeStateCorrupt, errors.Join(errors.New("maintenance bootstrap is incomplete"), err))
		}
		select {
		case <-ctx.Done():
			return fail(CodeStateCorrupt, errors.Join(errors.New("maintenance bootstrap is incomplete"), ctx.Err(), err))
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func validateBootstrappedControlRoot(controlRoot string, marker bootstrapRecord) error {
	directory, err := canonicalDirectory(filepath.Join(controlRoot, "maintenance"))
	if err != nil {
		return fail(CodeStateCorrupt, err)
	}
	directoryID, err := directoryIdentity(directory)
	if err != nil || directoryID != marker.DirectoryIdentity {
		return fail(CodeStateCorrupt, errors.Join(errors.New("maintenance directory identity mismatch"), err))
	}
	control := controlPlane{
		root: controlRoot, directory: directory, directoryID: directoryID,
		guardPath: filepath.Join(directory, "guard.lock"), guardID: marker.GuardIdentity,
		writersPath: filepath.Join(directory, "writers.lock"), writersID: marker.WritersIdentity,
		recordPath: filepath.Join(directory, recordFileName),
	}
	if err := validateControlLockIdentities(control); err != nil {
		return err
	}
	_, exists, err := readRecord(control)
	if err != nil || !exists {
		return fail(CodeStateCorrupt, errors.Join(errors.New("maintenance record is missing or unsafe"), err))
	}
	return nil
}

func readBootstrapRecord(controlRoot string) (bootstrapRecord, error) {
	var marker bootstrapRecord
	data, err := readRootRegularFile(controlRoot, bootstrapFileName)
	if err == nil {
		err = decodeStrictJSON(data, &marker)
	}
	if err != nil {
		return bootstrapRecord{}, err
	}
	if marker.Schema != bootstrapSchema || marker.Status != bootstrapComplete ||
		len(marker.DirectoryIdentity) != 33 || len(marker.GuardIdentity) != 33 ||
		len(marker.WritersIdentity) != 33 || marker.GuardIdentity == marker.WritersIdentity {
		return marker, errors.New("maintenance bootstrap proof is incomplete or invalid")
	}
	return marker, nil
}

func replaceBootstrapMarker(file *os.File, record bootstrapRecord) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := file.Truncate(0); err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	return file.Sync()
}

func secureMkdirAll(value string) (string, error) {
	absolute, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	current := absolute
	missing := make([]string, 0, 4)
	for {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return "", errors.New("control root path component is unsafe")
			}
			if _, err := canonicalDirectory(current); err != nil {
				return "", err
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("control root has no existing safe ancestor")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		current = filepath.Join(current, missing[index])
		if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, err := os.Lstat(current)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.Join(errors.New("created control root component is unsafe"), err)
		}
	}
	return canonicalDirectory(absolute)
}

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecordSymlinkCannotForgeOpenAdmission(t *testing.T) {
	scope := newTestScope(t)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	realPath := filepath.Join(control.directory, "record.real")
	if err := os.Rename(control.recordPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("record.real", control.recordPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeStateCorrupt)
}

func TestOpenedControlRootHandleMustMatchPersistedDirectoryIdentity(t *testing.T) {
	scope := newTestScope(t)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	fakeDirectory := filepath.Join(scope.ControlRoot, "maintenance.fake")
	if err := os.Mkdir(fakeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	original := openControlRoot
	openControlRoot = func(path string) (*os.Root, error) {
		if canonicalKey(path) == canonicalKey(control.directory) {
			return original(fakeDirectory)
		}
		return original(path)
	}
	t.Cleanup(func() { openControlRoot = original })
	_, _, err = readRecord(control)
	assertCode(t, err, CodeStateCorrupt)
}

func TestRenamedAndRecreatedControlLockIdentityIsRejected(t *testing.T) {
	for _, lockName := range []string{"guard.lock", "writers.lock"} {
		t.Run(lockName, func(t *testing.T) {
			scope := newTestScope(t)
			control, err := normalizeScope(scope)
			if err != nil {
				t.Fatal(err)
			}
			lockPath := filepath.Join(control.directory, lockName)
			if err := os.Rename(lockPath, lockPath+".retired"); err != nil {
				t.Fatal(err)
			}
			replacement, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := replacement.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = acquirePlaneLock(
				context.Background(), control, lockPath, true, lockDeadline(context.Background()),
			)
			assertCode(t, err, CodeStateCorrupt)
			_, err = normalizeScope(scope)
			assertCode(t, err, CodeStateCorrupt)
		})
	}
}

func TestBootstrapMarkerSymlinkIsRejected(t *testing.T) {
	scope := newTestScope(t)
	markerPath := filepath.Join(scope.ControlRoot, bootstrapFileName)
	realPath := markerPath + ".real"
	if err := os.Rename(markerPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(realPath), markerPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeStateCorrupt)
}

func TestBootstrapMarkerPreventsStateResurrection(t *testing.T) {
	scope := newTestScope(t)
	maintenancePath := filepath.Join(scope.ControlRoot, "maintenance")
	retiredPath := filepath.Join(scope.ControlRoot, "maintenance.retired")
	if err := os.Rename(maintenancePath, retiredPath); err != nil {
		t.Fatal(err)
	}
	err := BootstrapControlRoot(context.Background(), scope.ControlRoot)
	assertCode(t, err, CodeStateCorrupt)
	if _, err := os.Lstat(maintenancePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bootstrap recreated maintenance state: %v", err)
	}
}

func TestInterruptedBootstrapCannotRestart(t *testing.T) {
	controlRoot := filepath.Join(t.TempDir(), "control")
	if err := os.Mkdir(controlRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := bootstrapRecord{Schema: bootstrapSchema, Status: bootstrapPending}
	data, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(controlRoot, bootstrapFileName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	err = BootstrapControlRoot(ctx, controlRoot)
	assertCode(t, err, CodeStateCorrupt)
	if _, err := os.Lstat(filepath.Join(controlRoot, "maintenance")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted bootstrap recreated state: %v", err)
	}
}

func TestBoundRecordWriteDoesNotRecreateRenamedDirectory(t *testing.T) {
	scope := newTestScope(t)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record, exists, err := readRecord(control)
	if err != nil || !exists {
		t.Fatalf("read record: exists=%v err=%v", exists, err)
	}
	retiredPath := control.directory + ".retired"
	if err := os.Rename(control.directory, retiredPath); err != nil {
		t.Fatal(err)
	}
	if err := writeRecordFile(control, record); !IsCode(err, CodeLockFailed) && !IsCode(err, CodeStateCorrupt) {
		t.Fatalf("bound write error = %v", err)
	}
	if _, err := os.Lstat(control.directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("record write recreated directory: %v", err)
	}
}

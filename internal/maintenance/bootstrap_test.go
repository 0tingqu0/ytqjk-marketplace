package maintenance

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestOrdinaryAcquireDoesNotBootstrapControlRoot(t *testing.T) {
	root := t.TempDir()
	scope := Scope{
		ControlRoot: filepath.Join(root, "control"), RuntimeRoot: filepath.Join(root, "runtime"),
	}
	if err := os.Mkdir(scope.RuntimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeStateCorrupt)
	if _, statErr := os.Lstat(scope.ControlRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("ordinary acquire created control root: %v", statErr)
	}
}

func TestBootstrapControlRootIsConcurrentAndIdempotent(t *testing.T) {
	controlRoot := filepath.Join(t.TempDir(), "nested", "control")
	start := make(chan struct{})
	errorsByCall := make(chan error, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsByCall <- BootstrapControlRoot(context.Background(), controlRoot)
		}()
	}
	close(start)
	group.Wait()
	close(errorsByCall)
	for err := range errorsByCall {
		if err != nil {
			t.Fatal(err)
		}
	}
	var record Record
	if err := readStrictJSONFile(filepath.Join(controlRoot, "maintenance", "record.json"), &record); err != nil {
		t.Fatal(err)
	}
	if record.State != StateOpen || record.Generation != 0 || record.Revision != 1 {
		t.Fatalf("initial record = %#v", record)
	}
	marker, err := readBootstrapRecord(controlRoot)
	if err != nil {
		t.Fatal(err)
	}
	if marker.GuardIdentity == marker.WritersIdentity {
		t.Fatal("bootstrap marker did not freeze distinct lock identities")
	}
	if err := validateBootstrappedControlRoot(controlRoot, marker); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapRejectsExistingBadState(t *testing.T) {
	controlRoot := filepath.Join(t.TempDir(), "control")
	maintenanceRoot := filepath.Join(controlRoot, "maintenance")
	if err := os.MkdirAll(maintenanceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(maintenanceRoot, "record.json"), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := BootstrapControlRoot(context.Background(), controlRoot)
	assertCode(t, err, CodeStateCorrupt)
}

func TestBootstrapRejectsSymlinkControlRoot(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "control")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	err := BootstrapControlRoot(context.Background(), link)
	assertCode(t, err, CodeStateCorrupt)
}

func TestBootstrapLockDoesNotRepresentAdmissionState(t *testing.T) {
	scope := newTestScope(t)
	bootstrapPath := filepath.Join(scope.ControlRoot, ".maintenance.bootstrap.lock")
	bootstrapLock, err := acquireStandaloneLock(
		context.Background(), bootstrapPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		t.Fatal(err)
	}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatalf("bootstrap lock blocked admission: %v", err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	if err := joinUnlock(nil, bootstrapLock); err != nil {
		t.Fatal(err)
	}
}

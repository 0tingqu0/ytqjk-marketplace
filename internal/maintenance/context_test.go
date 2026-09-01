package maintenance

import (
	"context"
	"errors"
	"testing"
)

func TestSharedContextAllowsOnlyNestedResourceSubsets(t *testing.T) {
	scope := newTestScope(t)
	extra := t.TempDir()
	scope.ExtraRoots = []string{extra}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := WithShared(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	nested := Scope{ControlRoot: scope.ControlRoot, RuntimeRoot: extra}
	fence, err := SharedFenceFromContext(shared, nested)
	if err != nil {
		t.Fatal(err)
	}
	if len(fence.Resources) != 1 || fence.Resources[0] != canonicalKey(extra) {
		t.Fatalf("nested fence = %#v", fence)
	}
	if _, err := AcquireShared(shared, nested); !IsCode(err, CodeInvalid) {
		t.Fatalf("nested acquire error = %v", err)
	}
	superset := nested
	superset.ExtraRoots = []string{t.TempDir()}
	if _, err := SharedFenceFromContext(shared, superset); !IsCode(err, CodeInvalid) {
		t.Fatalf("superset error = %v", err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err := SharedFenceFromContext(shared, nested); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("released context error = %v", err)
	}
}

func TestCommitActionCanUseSharedContextWithoutNestedLock(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := WithShared(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Commit(func(Fence) error {
		_, nestedErr := SharedFenceFromContext(shared, scope)
		return nestedErr
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCommitPanicReleasesWriterLockAndRepanics(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		_ = permit.Commit(func(Fence) error {
			panic("commit panic")
		})
	}()
	if recovered != "commit panic" {
		t.Fatalf("recovered panic = %#v", recovered)
	}
	next, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatalf("writer lock remained held after panic: %v", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitBlocksConcurrentReleaseAndSecondCommit(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	releaseAction := make(chan struct{})
	completed := make(chan error, 1)
	go func() {
		completed <- permit.Commit(func(Fence) error {
			close(started)
			<-releaseAction
			return nil
		})
	}()
	<-started
	if err := permit.Release(); !IsCode(err, CodeActive) {
		t.Fatalf("concurrent release error = %v", err)
	}
	if err := permit.Commit(func(Fence) error { return nil }); !IsCode(err, CodeActive) {
		t.Fatalf("concurrent commit error = %v", err)
	}
	close(releaseAction)
	if err := <-completed; err != nil {
		t.Fatal(err)
	}
}

func TestCommitFinalFenceFailureReturnsPermanentUnknown(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	directoryID := permit.control.directoryID
	actionCalls := 0
	err = permit.Commit(func(Fence) error {
		actionCalls++
		permit.control.directoryID = "forged-directory-identity"
		return nil
	})
	permit.control.directoryID = directoryID
	assertCode(t, err, CodeCommitResultUnknown)
	if actionCalls != 1 {
		t.Fatalf("action calls = %d", actionCalls)
	}
	if err := permit.Commit(func(Fence) error { actionCalls++; return nil }); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("terminal permit error = %v", err)
	}
	if actionCalls != 1 {
		t.Fatalf("action was retried: calls=%d", actionCalls)
	}
	next, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatalf("writer lock leaked after final fence failure: %v", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestCommitUnlockFailureReturnsPermanentUnknown(t *testing.T) {
	scope := newTestScope(t)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	originalUnlock := permit.lock.unlock
	permit.lock.unlock = func() error {
		return errors.Join(originalUnlock(), errors.New("injected unlock report"))
	}
	actionCalls := 0
	err = permit.Commit(func(Fence) error { actionCalls++; return nil })
	assertCode(t, err, CodeCommitResultUnknown)
	if actionCalls != 1 {
		t.Fatalf("action calls = %d", actionCalls)
	}
	next, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatalf("underlying writer lock leaked: %v", err)
	}
	if err := next.Release(); err != nil {
		t.Fatal(err)
	}
}

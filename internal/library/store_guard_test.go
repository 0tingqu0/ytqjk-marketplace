package library

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSnapshotGuardBlocksCrossStoreWritesUntilClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	global := testNode("global", TypeGlobal, nil)
	first, err := OpenStore(path, []Node{global}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := OpenStore(path, []Node{global}, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	guard, err := first.BeginSnapshotGuard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })
	frozen := guard.Snapshot()

	type reconcileResult struct {
		snapshot Snapshot
		err      error
	}
	started := make(chan struct{})
	completed := make(chan reconcileResult, 1)
	go func() {
		close(started)
		snapshot, reconcileErr := second.ReconcileSeedNodes([]Node{
			testNode("project-a", TypeProject, stringPointer("global")),
		})
		completed <- reconcileResult{snapshot: snapshot, err: reconcileErr}
	}()
	<-started
	deadline := time.Now().Add(time.Second)
	for second.database.Stats().InUse == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if second.database.Stats().InUse == 0 {
		t.Fatal("writer did not reach the second Store connection")
	}

	select {
	case result := <-completed:
		t.Fatalf("write completed while guard was open: snapshot=%#v err=%v", result.snapshot, result.err)
	case <-time.After(200 * time.Millisecond):
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}

	var result reconcileResult
	select {
	case result = <-completed:
	case <-time.After(5 * time.Second):
		t.Fatal("write remained blocked after guard closed")
	}
	if result.err != nil {
		t.Fatalf("ReconcileSeedNodes() error = %v", result.err)
	}
	if result.snapshot.Revision != frozen.Revision+1 || !hasNode(result.snapshot, "project-a") {
		t.Fatalf("reconciled snapshot = %#v", result.snapshot)
	}
	persisted, err := first.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != result.snapshot.Revision || persisted.Digest != result.snapshot.Digest {
		t.Fatalf("persisted snapshot = %#v, reconcile result = %#v", persisted, result.snapshot)
	}
}

func TestSnapshotGuardSnapshotIsDetachedAndCloseIsIdempotent(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	t.Cleanup(func() { _ = store.Close() })
	guard, err := store.BeginSnapshotGuard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.Close() })

	expected := guard.Snapshot()
	mutated := guard.Snapshot()
	mutated.Nodes[0].ID = "poisoned"
	mutated.Nodes[0].Metadata["poisoned"] = "true"
	if mutated.Nodes[0].ParentID != nil {
		*mutated.Nodes[0].ParentID = "poisoned"
	}
	mutated.Edges[0].ChildID = "poisoned"
	mutated.Roots[0] = "poisoned"
	if actual := guard.Snapshot(); !reflect.DeepEqual(actual, expected) {
		t.Fatalf("snapshot mutation leaked: got=%#v want=%#v", actual, expected)
	}
	if err := guard.Close(); err != nil {
		t.Fatal(err)
	}
	if err := guard.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
}

func TestSnapshotGuardCloseMapsRollbackError(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	t.Cleanup(func() { _ = store.Close() })
	guard, err := store.BeginSnapshotGuard(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	first := guard.Close()
	assertServerCode(t, first, "LIBRARY_STORE_UNAVAILABLE")
	second := guard.Close()
	assertServerCode(t, second, "LIBRARY_STORE_UNAVAILABLE")
	if first != second {
		t.Fatal("idempotent Close() did not return the original error")
	}
}

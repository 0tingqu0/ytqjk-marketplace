package library

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestStoreRejectsInvalidBootstrapSeedAndRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	invalid := testNode("global", TypeGlobal, nil)
	invalid.CapacityBytes = MinCapacityBytes - 1
	_, err := OpenStore(path, []Node{invalid}, 0)
	assertServerCode(t, err, "LIBRARY_SEED_CONFLICT")

	database := openRawLibraryDatabase(t, path)
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN ('library_state', 'library_previews')`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 0 || tables != 0 {
		t.Fatalf("partial bootstrap survived: version=%d tables=%d", version, tables)
	}
}

func TestConcurrentStoreBootstrapCreatesOneCanonicalState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for range 2 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			store, err := OpenStore(path, testNodes(), 0)
			if err == nil {
				err = store.Close()
			}
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatalf("concurrent bootstrap: %v: %v", result, errors.Unwrap(result))
		}
	}
	store, err := OpenStore(path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 0 || len(snapshot.Nodes) != len(testNodes()) {
		t.Fatalf("bootstrapped snapshot = %#v", snapshot)
	}
	var stateRows int
	if err := store.database.QueryRow("SELECT COUNT(*) FROM library_state").Scan(&stateRows); err != nil {
		t.Fatal(err)
	}
	if stateRows != 1 {
		t.Fatalf("library_state rows = %d", stateRows)
	}
}

func TestStoreClaimsExistingVersionZeroSchemaWithoutDataLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	store := openTestStore(t, path)
	preview, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"alpha"}`))
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	database := openRawLibraryDatabase(t, path)
	if _, err := database.Exec("PRAGMA user_version = 0"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path, []Node{testNode("ignored", TypeGlobal, nil)}, 99)
	if err != nil {
		t.Fatalf("claim v0 store: %v", err)
	}
	defer store.Close()
	after, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != after.Revision || before.Digest != after.Digest || hasNode(after, "ignored") {
		t.Fatalf("v0 claim changed state: before=%#v after=%#v", before, after)
	}
	if _, err := store.Commit(ActionAttach, preview.Digest, preview.ExpectedRevision); err != nil {
		t.Fatalf("persisted preview was lost: %v", err)
	}
	var version int
	if err := store.database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != librarySchemaVersion {
		t.Fatalf("schema version = %d", version)
	}
}

func TestStoreRejectsFutureSchemaWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	database := openRawLibraryDatabase(t, path)
	if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := OpenStore(path, testNodes(), 0)
	assertServerCode(t, err, "LIBRARY_SCHEMA_TOO_NEW")

	database = openRawLibraryDatabase(t, path)
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	var tables int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name LIKE 'library_%'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if version != 2 || tables != 0 {
		t.Fatalf("future schema was mutated: version=%d tables=%d", version, tables)
	}
}

func TestStoreStrictlyRejectsCorruptedStateAndPreviewJSON(t *testing.T) {
	t.Run("state unknown field", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
		defer store.Close()
		if _, err := store.database.Exec(`UPDATE library_state SET nodes_json = '[{"id":"global","unexpected":true}]'`); err != nil {
			t.Fatal(err)
		}
		_, err := store.Snapshot(nil)
		assertServerCode(t, err, "LIBRARY_STORE_CORRUPT")
	})

	t.Run("state nested duplicate", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
		defer store.Close()
		corrupt := `[{
			"id":"mounted","title":"Mounted","type":"mounted","parent_id":null,
			"capacity_bytes":1073741824,
			"metadata":{"mount_id":"one","mount_id":"two","capability":"READ_ONLY"},
			"stats":{"used_bytes":0,"indexed_documents":0,"total_documents":0,"indexed_chunks":0,"total_chunks":0}
		}]`
		if _, err := store.database.Exec(`UPDATE library_state SET nodes_json = ?`, corrupt); err != nil {
			t.Fatal(err)
		}
		_, err := store.Snapshot(nil)
		assertServerCode(t, err, "LIBRARY_STORE_CORRUPT")
	})

	for name, corrupt := range map[string]string{
		"preview unknown field": `{"action":"attach","unexpected":true}`,
		"preview duplicate key": `{"action":"attach","action":"move"}`,
		"preview trailing data": `{} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
			defer store.Close()
			payload := []byte(`{"node_id":"orphan","parent_id":"alpha"}`)
			preview, err := store.Preview(ActionAttach, payload)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.database.Exec(
				`UPDATE library_previews SET preview_json = ? WHERE digest = ?`,
				corrupt, preview.Digest,
			); err != nil {
				t.Fatal(err)
			}
			_, err = store.Commit(ActionAttach, preview.Digest, preview.ExpectedRevision)
			assertServerCode(t, err, "LIBRARY_STORE_CORRUPT")
		})
	}
}

func TestStorePreviewTTLPruningAndCapacity(t *testing.T) {
	t.Run("expired preview", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
		defer store.Close()
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		store.now = func() time.Time { return now }
		expired, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"alpha"}`))
		if err != nil {
			t.Fatal(err)
		}
		now = now.Add(previewTTL)
		assertCode(t, commitError(store, ActionAttach, expired), "PREVIEW_EXPIRED")
		if _, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"other"}`)); err != nil {
			t.Fatal(err)
		}
		assertCode(t, commitError(store, ActionAttach, expired), "PREVIEW_NOT_FOUND")
	})

	t.Run("bounded preview table", func(t *testing.T) {
		store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
		t.Cleanup(func() { _ = store.Close() })
		now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
		store.now = func() time.Time { return now }
		var first, last MutationPreview
		for index := 0; index <= maxPersistedPreview; index++ {
			request := validGroupRequest()
			request.NodeID = fmt.Sprintf("group-%03d", index)
			request.Title = fmt.Sprintf("Group %03d", index)
			request.ParentID = stringPointer("global")
			preview, err := store.Preview(ActionCreate, createPayload(t, request))
			if err != nil {
				t.Fatalf("preview %d: %v", index, err)
			}
			if index == 0 {
				first = preview
			}
			last = preview
			now = now.Add(time.Second)
		}
		var count int
		if err := store.database.QueryRow("SELECT COUNT(*) FROM library_previews").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != maxPersistedPreview {
			t.Fatalf("preview count = %d", count)
		}
		assertCode(t, commitError(store, ActionCreate, first), "PREVIEW_NOT_FOUND")
		if _, err := store.Commit(ActionCreate, last.Digest, last.ExpectedRevision); err != nil {
			t.Fatalf("newest preview was evicted: %v", err)
		}
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestReconcileSeedNodesIsAdditiveAtomicAndConcurrent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	global := testNode("global", TypeGlobal, nil)
	store, err := OpenStore(path, []Node{global}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	seedGlobal := cloneNode(global)
	seedGlobal.Title = "Must not replace existing title"
	seedGlobal.CapacityBytes++
	projectA := testNode("project-a", TypeProject, stringPointer("global"))
	projectB := testNode("project-b", TypeProject, stringPointer("global"))
	merged, err := store.ReconcileSeedNodes([]Node{seedGlobal, projectA, projectB})
	if err != nil {
		t.Fatal(err)
	}
	if merged.Revision != 1 || len(merged.Nodes) != 3 {
		t.Fatalf("merged snapshot = %#v", merged)
	}
	for _, node := range merged.Nodes {
		if node.ID == "global" && (node.Title != global.Title || node.CapacityBytes != global.CapacityBytes) {
			t.Fatalf("existing global was rewritten: %#v", node)
		}
	}
	projectA.Title = "Must also be ignored"
	unchanged, err := store.ReconcileSeedNodes([]Node{projectA})
	if err != nil || unchanged.Revision != merged.Revision {
		t.Fatalf("idempotent reconcile = %#v, %v", unchanged, err)
	}

	conflictingGlobal := testNode("global", TypeProject, nil)
	newProject := testNode("not-added", TypeProject, stringPointer("global"))
	_, err = store.ReconcileSeedNodes([]Node{newProject, conflictingGlobal})
	assertServerCode(t, err, "LIBRARY_SEED_CONFLICT")
	afterConflict, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if hasNode(afterConflict, "not-added") || afterConflict.Revision != merged.Revision {
		t.Fatalf("failed batch leaked: %#v", afterConflict)
	}
}

func TestConcurrentSeedReconcileIncrementsRevisionOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	global := testNode("global", TypeGlobal, nil)
	first, err := OpenStore(path, []Node{global}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(path, []Node{global}, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	seed := []Node{testNode("project-a", TypeProject, stringPointer("global"))}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, candidate := range []*Store{first, second} {
		workers.Add(1)
		go func(store *Store) {
			defer workers.Done()
			<-start
			_, reconcileErr := store.ReconcileSeedNodes(seed)
			results <- reconcileErr
		}(candidate)
	}
	close(start)
	workers.Wait()
	close(results)
	for result := range results {
		if result != nil {
			t.Fatalf("concurrent reconcile: %v", result)
		}
	}
	snapshot, err := first.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 1 || !hasNode(snapshot, "project-a") {
		t.Fatalf("concurrent reconciliation = %#v", snapshot)
	}
}

func TestClosedStoreReturnsStableUnavailableError(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := store.Snapshot(nil)
	assertServerCode(t, err, "LIBRARY_STORE_UNAVAILABLE")
}

func openRawLibraryDatabase(t *testing.T, path string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

func assertServerCode(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected server error %s", expected)
	}
	var libraryErr *Error
	if !errors.As(err, &libraryErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if libraryErr.Code != expected || !libraryErr.IsServerFault() {
		t.Fatalf("error = %#v, want server fault %s", libraryErr, expected)
	}
}

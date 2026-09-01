package library

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorePreviewSurvivesRestartWithoutMutatingTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	store := openTestStore(t, path)
	before, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	request := validGroupRequest()
	request.NodeID = "team"
	request.Title = "Team"
	request.ParentID = stringPointer("global")
	payload := createPayload(t, request)
	preview, err := store.Preview(ActionCreate, payload)
	if err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	afterPreview, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if afterPreview.Revision != before.Revision || afterPreview.Digest != before.Digest || len(afterPreview.Nodes) != len(before.Nodes) {
		t.Fatalf("preview mutated tree: before=%#v after=%#v", before, afterPreview)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = OpenStore(path, []Node{testNode("ignored", TypeGlobal, nil)}, 99)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	committed, err := store.Commit(ActionCreate, preview.Digest, preview.ExpectedRevision)
	if err != nil {
		t.Fatalf("Commit() after restart error = %v", err)
	}
	if committed.Revision != before.Revision+1 || !hasNode(committed, "team") || hasNode(committed, "ignored") {
		t.Fatalf("unexpected committed snapshot: %#v", committed)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = OpenStore(path, nil, 0)
	if err != nil {
		t.Fatalf("second reopen: %v", err)
	}
	persisted, err := store.Snapshot(nil)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Revision != committed.Revision || persisted.Digest != committed.Digest || !hasNode(persisted, "team") {
		t.Fatalf("commit did not persist: %#v", persisted)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsReplayAndStalePreviewDeterministically(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	defer store.Close()
	first, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"alpha"}`))
	if err != nil {
		t.Fatal(err)
	}
	stale, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"other"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertCode(t, commitError(store, ActionMove, first), "PREVIEW_ACTION_MISMATCH")
	if _, err := store.Commit(ActionAttach, first.Digest, first.ExpectedRevision); err != nil {
		t.Fatalf("first Commit() error = %v", err)
	}
	assertCode(t, commitError(store, ActionAttach, first), "PREVIEW_REPLAYED")
	assertCode(t, commitError(store, ActionAttach, stale), "REVISION_CONFLICT")
	_, err = store.Commit(ActionAttach, stale.Digest, stale.ExpectedRevision+1)
	assertCode(t, err, "PREVIEW_MISMATCH")
}

func TestStoreConcurrentCASAllowsExactlyOneWinner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	first := openTestStore(t, path)
	defer first.Close()
	second, err := OpenStore(path, testNodes(), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	left, err := first.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"alpha"}`))
	if err != nil {
		t.Fatal(err)
	}
	right, err := second.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"other"}`))
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, candidate := range []struct {
		store   *Store
		preview MutationPreview
	}{{first, left}, {second, right}} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, commitErr := candidate.store.Commit(ActionAttach, candidate.preview.Digest, candidate.preview.ExpectedRevision)
			results <- commitErr
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	conflicted := 0
	for result := range results {
		if result == nil {
			succeeded++
			continue
		}
		var contractErr *Error
		if errors.As(result, &contractErr) && contractErr.Code == "REVISION_CONFLICT" {
			conflicted++
			continue
		}
		t.Fatalf("unexpected concurrent result: %v", result)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestStoreSupportsFiveTopologyActions(t *testing.T) {
	tests := []struct {
		name    string
		action  string
		payload func(*testing.T) []byte
		verify  func(*testing.T, Snapshot)
	}{
		{
			name: "create", action: ActionCreate,
			payload: func(t *testing.T) []byte {
				request := validGroupRequest()
				request.NodeID, request.Title = "team", "Team"
				request.ParentID = stringPointer("global")
				return createPayload(t, request)
			},
			verify: func(t *testing.T, snapshot Snapshot) {
				if !hasNode(snapshot, "team") {
					t.Fatal("created node is absent")
				}
			},
		},
		{
			name: "attach", action: ActionAttach,
			payload: func(*testing.T) []byte {
				return []byte(`{"node_id":"orphan","parent_id":"alpha"}`)
			},
			verify: verifyParent("orphan", "alpha"),
		},
		{
			name: "detach", action: ActionDetach,
			payload: func(*testing.T) []byte { return []byte(`{"node_id":"alpha"}`) },
			verify:  verifyParent("alpha", ""),
		},
		{
			name: "move", action: ActionMove,
			payload: func(*testing.T) []byte {
				return []byte(`{"node_id":"alpha","parent_id":"other"}`)
			},
			verify: verifyParent("alpha", "other"),
		},
		{
			name: "insert between", action: ActionInsertBetween,
			payload: func(*testing.T) []byte {
				return []byte(`{"parent_id":"global","node_id":"alpha","middle_id":"bridge"}`)
			},
			verify: func(t *testing.T, snapshot Snapshot) {
				verifyParent("bridge", "global")(t, snapshot)
				verifyParent("alpha", "bridge")(t, snapshot)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
			defer store.Close()
			preview, err := store.Preview(test.action, test.payload(t))
			if err != nil {
				t.Fatalf("Preview() error = %v", err)
			}
			snapshot, err := store.Commit(test.action, preview.Digest, preview.ExpectedRevision)
			if err != nil {
				t.Fatalf("Commit() error = %v", err)
			}
			if snapshot.Revision != 1 {
				t.Fatalf("revision = %d", snapshot.Revision)
			}
			test.verify(t, snapshot)
		})
	}
}

func TestStoreStatsDoNotChangeDigest(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	defer store.Close()
	first, err := store.Snapshot(map[string]Statistics{"global": {UsedBytes: 1}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Snapshot(map[string]Statistics{"global": {UsedBytes: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if first.Digest != second.Digest || first.Nodes[2].Stats.UsedBytes == second.Nodes[2].Stats.UsedBytes {
		t.Fatalf("stats/digest separation failed: first=%#v second=%#v", first, second)
	}
}

func TestStoreRequiresExactMutationFields(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "library.sqlite3"))
	defer store.Close()
	_, err := store.Preview(ActionAttach, []byte(`{"node_id":"orphan","parent_id":"alpha","extra":true}`))
	assertCode(t, err, "INVALID_REQUEST_FIELDS")
	_, err = store.Preview(ActionCreate, []byte(`{"node_id":"team","title":"Team","type":"group","parent_id":null,"metadata":{}}`))
	assertCode(t, err, "INVALID_REQUEST_FIELDS")
}

func TestMutationEnvelopesRequireExactCASFields(t *testing.T) {
	_, err := DecodePreviewRequest([]byte(`{"action":"attach","action":"move","payload":{}}`))
	assertCode(t, err, "DUPLICATE_JSON_KEY")
	_, err = DecodeCommitRequest([]byte(`{"digest":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}`))
	assertCode(t, err, "INVALID_REQUEST_FIELDS")
	_, err = DecodeCommitRequest([]byte(`{"digest":"short","expected_revision":0}`))
	assertCode(t, err, "INVALID_PREVIEW_DIGEST")
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path, testNodes(), 0)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	return store
}

func createPayload(t *testing.T, request CreateRequest) []byte {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"node_id": request.NodeID, "title": request.Title, "type": request.Type,
		"parent_id": request.ParentID, "capacity_bytes": request.CapacityBytes,
		"metadata": request.Metadata,
	})
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func commitError(store *Store, action string, preview MutationPreview) error {
	_, err := store.Commit(action, preview.Digest, preview.ExpectedRevision)
	return err
}

func hasNode(snapshot Snapshot, nodeID string) bool {
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func verifyParent(nodeID, parentID string) func(*testing.T, Snapshot) {
	return func(t *testing.T, snapshot Snapshot) {
		t.Helper()
		for _, node := range snapshot.Nodes {
			if node.ID != nodeID {
				continue
			}
			if parentID == "" && node.ParentID == nil {
				return
			}
			if node.ParentID != nil && *node.ParentID == parentID {
				return
			}
			t.Fatalf("parent for %s = %#v, want %q", nodeID, node.ParentID, parentID)
		}
		t.Fatalf("node %s not found", nodeID)
	}
}

package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestSnapshotIsSortedAndDetached(t *testing.T) {
	tree := testTree(t)
	snapshot, err := tree.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.Revision != 0 || len(snapshot.Digest) != 64 {
		t.Fatalf("unexpected snapshot header: %#v", snapshot)
	}
	if snapshot.Nodes[0].ID != "alpha" || snapshot.Nodes[len(snapshot.Nodes)-1].ID != "sibling" {
		t.Fatalf("nodes are not sorted: %#v", snapshot.Nodes)
	}
	if len(snapshot.Roots) != 4 || snapshot.Roots[0] != "bridge" {
		t.Fatalf("unexpected roots: %#v", snapshot.Roots)
	}
	snapshot.Nodes[0].ID = "poisoned"
	snapshot.Nodes[0].Metadata["path"] = "../escape"
	second, err := tree.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if second.Nodes[0].ID != "alpha" || len(second.Nodes[0].Metadata) != 0 {
		t.Fatalf("snapshot mutation leaked: %#v", second.Nodes[0])
	}
}

func TestSnapshotDigestIncludesConfigurationButNotStatistics(t *testing.T) {
	base := testNodes()
	first := mustTree(t, base)
	base[0].Stats = Statistics{
		UsedBytes: 123, IndexedDocuments: 2, TotalDocuments: 3,
		IndexedChunks: 5, TotalChunks: 8,
	}
	statsChanged := mustTree(t, base)
	base[0].CapacityBytes++
	capacityChanged := mustTree(t, base)

	firstSnapshot, err := first.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	statsSnapshot, err := statsChanged.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	capacitySnapshot, err := capacityChanged.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if firstSnapshot.Digest != statsSnapshot.Digest {
		t.Fatal("runtime statistics changed the configuration digest")
	}
	if firstSnapshot.Digest == capacitySnapshot.Digest {
		t.Fatal("capacity did not change the configuration digest")
	}
	var global Node
	for _, node := range statsSnapshot.Nodes {
		if node.ID == "global" {
			global = node
			break
		}
	}
	if global.Stats.UsedBytes != 123 {
		t.Fatal("runtime statistics were omitted from the response")
	}
}

func TestSnapshotDigestUsesPythonCanonicalUnicodeStrings(t *testing.T) {
	node := testNode("global", TypeGlobal, nil)
	node.Title = "R&D <全球>\u2028段\u2029尾"
	tree := mustTree(t, []Node{node})
	snapshot, err := tree.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	summary := digestBody{
		Edges: []Edge{}, Nodes: digestNodes(snapshot.Nodes),
		Revision: snapshot.Revision, Roots: snapshot.Roots,
	}
	content, err := marshalCanonicalJSON(summary)
	if err != nil {
		t.Fatalf("marshalCanonicalJSON() error = %v", err)
	}
	want := "{\"edges\":[],\"nodes\":[{\"capacity_bytes\":1073741824," +
		"\"id\":\"global\",\"metadata\":{},\"parent_id\":null," +
		"\"title\":\"R&D <全球>\u2028段\u2029尾\",\"type\":\"global\"}]," +
		"\"revision\":0,\"roots\":[\"global\"]}"
	if string(content) != want {
		t.Fatalf("canonical JSON = %q, want %q", content, want)
	}
	expectedDigest := sha256.Sum256([]byte(want))
	if snapshot.Digest != hex.EncodeToString(expectedDigest[:]) {
		t.Fatalf("digest = %q, want %x", snapshot.Digest, expectedDigest)
	}
}

func TestSnapshotJSONUsesCanonicalNodeContract(t *testing.T) {
	snapshot, err := testTree(t).Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	payload, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var value map[string]any
	if err := json.Unmarshal(payload, &value); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	nodes := value["nodes"].([]any)
	first := nodes[0].(map[string]any)
	if _, exists := first["capacity_bytes"]; !exists {
		t.Fatal("capacity_bytes is missing")
	}
	if _, exists := first["stats"]; !exists {
		t.Fatal("stats is missing")
	}
}

func TestNewTreeRejectsInvalidNodesAndTopology(t *testing.T) {
	tests := []struct {
		name  string
		nodes []Node
		code  string
	}{
		{
			name:  "duplicate node",
			nodes: []Node{testNode("same", TypeGroup, nil), testNode("same", TypeGroup, nil)},
			code:  "DUPLICATE_NODE",
		},
		{
			name:  "unknown parent",
			nodes: []Node{testNode("child", TypeGroup, stringPointer("missing"))},
			code:  "UNKNOWN_NODE",
		},
		{
			name:  "self parent",
			nodes: []Node{testNode("same", TypeGroup, stringPointer("same"))},
			code:  "SELF_PARENT",
		},
		{
			name: "cycle",
			nodes: []Node{
				testNode("a", TypeGroup, stringPointer("b")),
				testNode("b", TypeGroup, stringPointer("a")),
			},
			code: "CYCLE_DETECTED",
		},
		{
			name: "invalid stats",
			nodes: func() []Node {
				node := testNode("a", TypeGroup, nil)
				node.Stats.IndexedDocuments = 2
				node.Stats.TotalDocuments = 1
				return []Node{node}
			}(),
			code: "INVALID_LIBRARY_STATS",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewTree(test.nodes, 0)
			assertCode(t, err, test.code)
		})
	}
}

func testTree(t *testing.T) *Tree {
	t.Helper()
	return mustTree(t, testNodes())
}

func mustTree(t *testing.T, nodes []Node) *Tree {
	t.Helper()
	tree, err := NewTree(nodes, 0)
	if err != nil {
		t.Fatalf("NewTree() error = %v", err)
	}
	return tree
}

func testNodes() []Node {
	return []Node{
		testNode("global", TypeGlobal, nil),
		testNode("alpha", TypeProject, stringPointer("global")),
		testNode("leaf", TypeGroup, stringPointer("alpha")),
		testNode("sibling", TypeProject, stringPointer("global")),
		testNode("other", TypeGroup, nil),
		testNode("bridge", TypeGroup, nil),
		testNode("orphan", TypeGroup, nil),
	}
}

func testNode(id string, nodeType Type, parentID *string) Node {
	return Node{
		ID: id, Title: "Node " + id, Type: nodeType,
		ParentID: parentID, CapacityBytes: oneGiB,
		Metadata: map[string]string{},
	}
}

func stringPointer(value string) *string {
	return &value
}

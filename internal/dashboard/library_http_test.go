package dashboard

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
)

func TestLibraryTreeUsesCanonicalSnapshot(t *testing.T) {
	root := t.TempDir()
	writeLibraryDashboardFixture(t, filepath.Join(root, "catalog.json"), map[string]any{
		"schema_version": 6,
		"projects": map[string]any{
			"project-z": map[string]any{"name": "Zulu"},
			"project-a": map[string]any{"name": "Alpha"},
		},
	})
	writeLibraryDashboardFixture(t, filepath.Join(root, "projects", "project-a", "manifest.json"), map[string]any{
		"stats": map[string]any{"files": 3, "chunks": 7},
	})
	writeLibraryDashboardFixture(t, filepath.Join(root, "projects", "project-z", "manifest.json"), map[string]any{
		"stats": map[string]any{"files": 5, "chunks": 11},
	})

	response := httptest.NewRecorder()
	server := &Server{KnowledgeRoot: root}
	if status := server.tree(response); status != http.StatusOK {
		t.Fatalf("tree status = %d, body = %s", status, response.Body.String())
	}
	var payload struct {
		OK   bool             `json:"ok"`
		Tree library.Snapshot `json:"tree"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK {
		t.Fatal("tree response is not ok")
	}
	snapshot := payload.Tree
	if len(snapshot.Nodes) != 3 || snapshot.Nodes[0].ID != "global" || snapshot.Nodes[1].ID != "project-a" || snapshot.Nodes[2].ID != "project-z" {
		t.Fatalf("nodes = %#v", snapshot.Nodes)
	}
	if len(snapshot.Roots) != 1 || snapshot.Roots[0] != "global" || len(snapshot.Edges) != 2 {
		t.Fatalf("topology = roots %#v, edges %#v", snapshot.Roots, snapshot.Edges)
	}
	if snapshot.Nodes[1].CapacityBytes != defaultLibraryCapacity || snapshot.Nodes[1].Stats.IndexedDocuments != 3 || snapshot.Nodes[1].Stats.IndexedChunks != 7 {
		t.Fatalf("project-a = %#v", snapshot.Nodes[1])
	}
	if len(snapshot.Digest) != 64 {
		t.Fatalf("digest = %q", snapshot.Digest)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, exists := raw["root"]; exists {
		t.Fatal("legacy root field is still exposed at the response root")
	}
	if _, exists := raw["tree"]; !exists {
		t.Fatal("canonical tree envelope is missing")
	}
}

func TestLibraryCreatePreviewCommitPersistsAcrossServerRestart(t *testing.T) {
	root := t.TempDir()
	server := &Server{KnowledgeRoot: root}
	before := readTreeResponse(t, server)
	previewBody := `{
		"action":"create",
		"payload":{
			"node_id":"team","title":"Team","type":"group",
			"parent_id":"global","capacity_bytes":1073741824,"metadata":{}
		}
	}`
	previewResponse := httptest.NewRecorder()
	previewRequest := httptest.NewRequest(http.MethodPost, "/api/libraries/preview", strings.NewReader(previewBody))
	if status := server.treePreview(previewResponse, previewRequest); status != http.StatusOK {
		t.Fatalf("preview status = %d, body = %s", status, previewResponse.Body.String())
	}
	var previewPayload struct {
		OK      bool                    `json:"ok"`
		Preview library.MutationPreview `json:"preview"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &previewPayload); err != nil {
		t.Fatal(err)
	}
	if !previewPayload.OK || len(previewPayload.Preview.Digest) != 64 || previewPayload.Preview.ExpectedRevision != before.Revision {
		t.Fatalf("preview = %#v", previewPayload)
	}
	afterPreview := readTreeResponse(t, server)
	if afterPreview.Revision != before.Revision || afterPreview.Digest != before.Digest || treeHasNode(afterPreview, "team") {
		t.Fatalf("preview mutated tree: before=%#v after=%#v", before, afterPreview)
	}

	commitBody, err := json.Marshal(map[string]any{
		"digest":            previewPayload.Preview.Digest,
		"expected_revision": previewPayload.Preview.ExpectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	commitResponse := httptest.NewRecorder()
	commitRequest := httptest.NewRequest(http.MethodPost, "/api/libraries/create", strings.NewReader(string(commitBody)))
	if status := server.handleAPI(commitResponse, commitRequest); status != http.StatusOK {
		t.Fatalf("commit status = %d, body = %s", status, commitResponse.Body.String())
	}
	var commitPayload struct {
		OK   bool             `json:"ok"`
		Tree library.Snapshot `json:"tree"`
	}
	if err := json.Unmarshal(commitResponse.Body.Bytes(), &commitPayload); err != nil {
		t.Fatal(err)
	}
	if !commitPayload.OK || commitPayload.Tree.Revision != before.Revision+1 || !treeHasNode(commitPayload.Tree, "team") {
		t.Fatalf("commit response = %#v", commitPayload)
	}

	restarted := &Server{KnowledgeRoot: root}
	persisted := readTreeResponse(t, restarted)
	if persisted.Revision != commitPayload.Tree.Revision || persisted.Digest != commitPayload.Tree.Digest || !treeHasNode(persisted, "team") {
		t.Fatalf("persisted tree = %#v", persisted)
	}
	replayResponse := httptest.NewRecorder()
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/libraries/create", strings.NewReader(string(commitBody)))
	if status := restarted.handleAPI(replayResponse, replayRequest); status != http.StatusConflict {
		t.Fatalf("replay status = %d, body = %s", status, replayResponse.Body.String())
	}
	assertDashboardErrorCode(t, replayResponse, "PREVIEW_REPLAYED")
}

func TestLibraryCreatePreviewRequiresCapacityBytes(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/libraries/preview", strings.NewReader(`{
		"action":"create",
		"payload":{"node_id":"team","title":"Team","type":"group","parent_id":null,"metadata":{}}
	}`))
	if status := server.treePreview(response, request); status != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", status, response.Body.String())
	}
	assertDashboardErrorCode(t, response, "INVALID_REQUEST_FIELDS")
}

func TestUnknownLibraryMutationRouteReturnsNotFound(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir()}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/libraries/unknown", strings.NewReader(`{}`))

	if status := server.handleAPI(response, request); status != http.StatusNotFound {
		t.Fatalf("status = %d, body = %s", status, response.Body.String())
	}
	assertDashboardErrorCode(t, response, "NOT_FOUND")
}

func TestLibraryRoutesRejectGroupIndexMaterialization(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir()}
	previewResponse := httptest.NewRecorder()
	previewRequest := httptest.NewRequest(http.MethodPost, "/api/libraries/preview", strings.NewReader(
		`{"action":"rebuild_index","payload":{"node_id":"operations","document_ids":[]}}`,
	))
	if status := server.treePreview(previewResponse, previewRequest); status != http.StatusBadRequest {
		t.Fatalf("preview status = %d, body = %s", status, previewResponse.Body.String())
	}
	assertDashboardErrorCode(t, previewResponse, "INVALID_ACTION")

	routeResponse := httptest.NewRecorder()
	routeRequest := httptest.NewRequest(http.MethodPost, "/api/libraries/rebuild-index", strings.NewReader(`{}`))
	if status := server.handleAPI(routeResponse, routeRequest); status != http.StatusNotFound {
		t.Fatalf("route status = %d, body = %s", status, routeResponse.Body.String())
	}
	assertDashboardErrorCode(t, routeResponse, "NOT_FOUND")
}

func TestLibraryTreeReconcilesNewCatalogProjectsWithoutOverwritingExistingNodes(t *testing.T) {
	root := t.TempDir()
	catalogPath := filepath.Join(root, "catalog.json")
	writeLibraryDashboardFixture(t, catalogPath, map[string]any{
		"schema_version": 6,
		"projects": map[string]any{
			"project-a": map[string]any{"name": "Alpha"},
		},
	})
	server := &Server{KnowledgeRoot: root}
	before := readTreeResponse(t, server)

	writeLibraryDashboardFixture(t, catalogPath, map[string]any{
		"schema_version": 6,
		"projects": map[string]any{
			"project-a": map[string]any{"name": "Renamed"},
			"project-b": map[string]any{"name": "Beta"},
		},
	})
	after := readTreeResponse(t, server)

	if after.Revision != before.Revision+1 || !treeHasNode(after, "project-b") {
		t.Fatalf("catalog reconcile = %#v", after)
	}
	projectA := treeNode(after, "project-a")
	if projectA.Title != "Alpha" {
		t.Fatalf("existing project title was overwritten: %#v", projectA)
	}
}

func TestLibraryFutureSchemaReturnsServiceUnavailable(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "service", "library-v1.sqlite3")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("PRAGMA user_version = 2"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	server := &Server{KnowledgeRoot: root}
	if status := server.tree(response); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", status, response.Body.String())
	}
	assertDashboardErrorCode(t, response, "LIBRARY_SCHEMA_TOO_NEW")
}

func TestLibraryInvalidCatalogSeedReturnsServiceUnavailable(t *testing.T) {
	root := t.TempDir()
	writeLibraryDashboardFixture(t, filepath.Join(root, "catalog.json"), map[string]any{
		"schema_version": 6,
		"projects": map[string]any{
			"bad/id": map[string]any{"name": "Invalid"},
		},
	})

	response := httptest.NewRecorder()
	server := &Server{KnowledgeRoot: root}
	if status := server.tree(response); status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", status, response.Body.String())
	}
	assertDashboardErrorCode(t, response, "LIBRARY_SEED_CONFLICT")
}

func readTreeResponse(t *testing.T, server *Server) library.Snapshot {
	t.Helper()
	response := httptest.NewRecorder()
	if status := server.tree(response); status != http.StatusOK {
		t.Fatalf("tree status = %d, body = %s", status, response.Body.String())
	}
	var payload struct {
		OK   bool             `json:"ok"`
		Tree library.Snapshot `json:"tree"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK {
		t.Fatal("tree response is not ok")
	}
	return payload.Tree
}

func treeHasNode(snapshot library.Snapshot, nodeID string) bool {
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID {
			return true
		}
	}
	return false
}

func treeNode(snapshot library.Snapshot, nodeID string) library.Node {
	for _, node := range snapshot.Nodes {
		if node.ID == nodeID {
			return node
		}
	}
	return library.Node{}
}

func assertDashboardErrorCode(t *testing.T, response *httptest.ResponseRecorder, expected string) {
	t.Helper()
	var payload struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != expected {
		t.Fatalf("error code = %q, want %q", payload.Error.Code, expected)
	}
}

func writeLibraryDashboardFixture(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

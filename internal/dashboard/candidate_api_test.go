package dashboard

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestDashboardSnapshotMatchesFrontendContract(t *testing.T) {
	root := t.TempDir()
	writeDashboardFixture(t, root, "verified/fact.md", "verified fact")
	writeDashboardFixture(t, root, "personal-experience/approved/lesson.md", "approved lesson")
	writeDashboardFixture(t, root, "error-experience/candidates/draft.md", "candidate draft")
	writeDashboardFixture(t, root, "personal-experience/candidates/imports/chunks/internal.md", "internal sidecar")
	if err := safeio.WriteJSON(filepath.Join(root, "sessions", "hashed", "anchor.json"), map[string]any{
		"session_key": "a1b2c3d4e5f67890", "project_id": "project-a",
		"created_at": "2026-01-01T00:00:00Z", "last_activity_at": "2026-01-02T00:00:00Z",
		"archived_at": nil, "memory": "private memory must not leave the server",
	}); err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(filepath.Join(root, "catalog.json"), rag.Catalog{
		SchemaVersion: rag.SchemaVersion,
		Projects: map[string]rag.CatalogProject{
			"project-a": {Name: "Project A", TrackingState: "INDEXED"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	manifest := rag.Manifest{
		SchemaVersion: rag.SchemaVersion,
		Identity:      rag.ProjectIdentity{ID: "project-a", Name: "Project A"},
		Stats:         rag.Stats{Files: 2, Chunks: 3, TextBytes: 40},
		Vector:        map[string]any{"status": "READY"}, IndexedAt: "2026-01-02T00:00:00Z",
	}
	if err := safeio.WriteJSON(filepath.Join(root, "projects", "project-a", "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(filepath.Join(root, "global-cache", "manifest.json"), rag.Manifest{
		SchemaVersion: rag.SchemaVersion, Stats: rag.Stats{Files: 2, Chunks: 4}, IndexedAt: "2026-01-02T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	server := dashboardTestServer(root)
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/snapshot", nil)
	request.Host = "127.0.0.1:8765"
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("snapshot = %d, %s", response.Code, response.Body.String())
	}
	if bytes.Contains(response.Body.Bytes(), []byte("private memory")) {
		t.Fatal("session memory leaked through dashboard snapshot")
	}
	var body struct {
		Counts struct {
			Verified  int `json:"verified"`
			Approved  int `json:"approved"`
			Candidate int `json:"candidate"`
			Sessions  int `json:"sessions"`
		} `json:"counts"`
		Documents []map[string]any `json:"documents"`
		Projects  []struct {
			ID       string `json:"id"`
			Tracking string `json:"tracking"`
			Cache    struct {
				Entries   int    `json:"entries"`
				UsedBytes int    `json:"used_bytes"`
				Policy    string `json:"policy"`
			} `json:"cache"`
		} `json:"projects"`
		Sessions []struct {
			Key       string `json:"key"`
			HasMemory bool   `json:"has_memory"`
		} `json:"sessions"`
		GlobalLibrary struct {
			Files  int `json:"files"`
			Chunks int `json:"chunks"`
		} `json:"global_library"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Counts.Verified != 1 || body.Counts.Approved != 1 || body.Counts.Candidate != 1 || body.Counts.Sessions != 1 || len(body.Documents) != 3 {
		t.Fatalf("snapshot counts = %#v, documents=%#v", body.Counts, body.Documents)
	}
	if len(body.Projects) != 1 || body.Projects[0].ID != "project-a" || body.Projects[0].Tracking != "INDEXED" || body.Projects[0].Cache.Policy != "LFU_LRU" {
		t.Fatalf("projects = %#v", body.Projects)
	}
	if len(body.Sessions) != 1 || body.Sessions[0].Key != "a1b2c3d4e5f6" || !body.Sessions[0].HasMemory {
		t.Fatalf("sessions = %#v", body.Sessions)
	}
	if body.GlobalLibrary.Files != 2 || body.GlobalLibrary.Chunks != 4 {
		t.Fatalf("global library = %#v", body.GlobalLibrary)
	}
}

func TestCandidateLifecycleUsesVersionCASAndGovernedIndex(t *testing.T) {
	root := t.TempDir()
	relative := "personal-experience/candidates/guide.md"
	initial := "---\nstatus: CANDIDATE\n---\n\nInitial draft.\n"
	writeDashboardFixture(t, root, relative, initial)
	writeDashboardFixture(t, root, "error-experience/candidates/unapproved.md", "candidate must not be indexed")
	server := dashboardTestServer(root)

	read := dashboardRequest(t, server, http.MethodGet, "/api/document?path="+url.QueryEscape(relative), nil)
	if read.Code != http.StatusOK {
		t.Fatalf("read = %d, %s", read.Code, read.Body.String())
	}
	var document struct {
		Content string `json:"content"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(read.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Content != initial || !validVersion(document.Version) {
		t.Fatalf("document = %#v", document)
	}

	updated := "---\nstatus: CANDIDATE\nsource: https://example.invalid/evidence\n---\n\n" + strings.Repeat("Tested migration evidence. ", 12) + "\n"
	updateBody, _ := json.Marshal(map[string]any{"path": relative, "content": updated, "expected_version": document.Version})
	update := dashboardRequest(t, server, http.MethodPut, "/api/candidate", updateBody)
	if update.Code != http.StatusOK {
		t.Fatalf("update = %d, %s", update.Code, update.Body.String())
	}
	var saved struct {
		Version    string             `json:"version"`
		Assessment approvalAssessment `json:"assessment"`
	}
	if err := json.Unmarshal(update.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Version == document.Version || saved.Assessment.Decision != "READY_FOR_REVIEW" {
		t.Fatalf("saved = %#v", saved)
	}

	staleBody, _ := json.Marshal(map[string]any{"path": relative, "content": "stale overwrite", "expected_version": document.Version})
	stale := dashboardRequest(t, server, http.MethodPut, "/api/candidate", staleBody)
	if stale.Code != http.StatusConflict {
		t.Fatalf("stale update = %d, %s", stale.Code, stale.Body.String())
	}
	current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || string(current) != updated {
		t.Fatalf("stale update changed file = %q, %v", current, err)
	}

	secretBody, _ := json.Marshal(map[string]any{
		"path": relative, "content": "authorization: bearer must-not-store", "expected_version": saved.Version,
	})
	secret := dashboardRequest(t, server, http.MethodPut, "/api/candidate", secretBody)
	if secret.Code != http.StatusBadRequest || bytes.Contains(secret.Body.Bytes(), []byte("must-not-store")) {
		t.Fatalf("secret update = %d, %s", secret.Code, secret.Body.String())
	}

	approveBody, _ := json.Marshal(map[string]any{"path": relative, "expected_version": saved.Version})
	approved := dashboardRequest(t, server, http.MethodPost, "/api/candidate/approve", approveBody)
	if approved.Code != http.StatusOK || !bytes.Contains(approved.Body.Bytes(), []byte(`"index_status":"REBUILT"`)) {
		t.Fatalf("approve = %d, %s", approved.Code, approved.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errorsIsNotExist(err) {
		t.Fatalf("candidate still exists: %v", err)
	}
	approvedPath := filepath.Join(root, "personal-experience", "approved", "guide.md")
	approvedContent, err := os.ReadFile(approvedPath)
	if err != nil || !bytes.Contains(approvedContent, []byte("status: APPROVED")) || !bytes.Contains(approvedContent, []byte("approval: manual-dashboard")) {
		t.Fatalf("approved content = %q, %v", approvedContent, err)
	}
	var index rag.Index
	if err := safeio.ReadJSON(filepath.Join(root, "global-cache", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	foundApproved := false
	for _, chunk := range index.Chunks {
		if chunk.Path == "error-experience/candidates/unapproved.md" {
			t.Fatal("candidate leaked into governed global index")
		}
		if chunk.Path == "personal-experience/approved/guide.md" {
			foundApproved = true
		}
	}
	if !foundApproved {
		t.Fatalf("approved document missing from global index: %#v", index.Chunks)
	}
}

func TestCandidateDeleteChecksVersionAndPathScope(t *testing.T) {
	root := t.TempDir()
	relative := "error-experience/candidates/delete.md"
	writeDashboardFixture(t, root, relative, "delete candidate")
	server := dashboardTestServer(root)
	version := candidateVersion([]byte("delete candidate"))

	wrongScope, _ := json.Marshal(map[string]any{"path": "verified/candidates/delete.md", "content": "value", "expected_version": version})
	invalid := dashboardRequest(t, server, http.MethodPut, "/api/candidate", wrongScope)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("wrong scope = %d, %s", invalid.Code, invalid.Body.String())
	}
	stale, _ := json.Marshal(map[string]any{"path": relative, "expected_version": strings.Repeat("0", 64)})
	conflict := dashboardRequest(t, server, http.MethodDelete, "/api/candidate", stale)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("stale delete = %d, %s", conflict.Code, conflict.Body.String())
	}
	valid, _ := json.Marshal(map[string]any{"path": relative, "expected_version": version})
	deleted := dashboardRequest(t, server, http.MethodDelete, "/api/candidate", valid)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete = %d, %s", deleted.Code, deleted.Body.String())
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !errorsIsNotExist(err) {
		t.Fatalf("candidate still exists: %v", err)
	}
}

func TestCandidateHardLinkIsRejected(t *testing.T) {
	root := t.TempDir()
	relative := "personal-experience/candidates/linked.md"
	writeDashboardFixture(t, root, relative, "linked candidate")
	alias := filepath.Join(root, "alias.md")
	if err := os.Link(filepath.Join(root, filepath.FromSlash(relative)), alias); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	server := dashboardTestServer(root)
	response := dashboardRequest(t, server, http.MethodGet, "/api/document?path="+url.QueryEscape(relative), nil)
	if response.Code != http.StatusNotFound {
		t.Fatalf("hard-linked candidate = %d, %s", response.Code, response.Body.String())
	}
}

func TestSnapshotMemoryFlagUsesJSONTruthiness(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{value: `null`, want: false}, {value: `false`, want: false},
		{value: `""`, want: false}, {value: `{}`, want: false},
		{value: `"summary"`, want: true}, {value: `{"saved":true}`, want: true},
	} {
		if got := hasJSONValue(json.RawMessage(test.value)); got != test.want {
			t.Fatalf("hasJSONValue(%s) = %v", test.value, got)
		}
	}
}

func dashboardTestServer(root string) *Server {
	return &Server{KnowledgeRoot: root, Port: 8765, logger: log.New(io.Discard, "", 0)}
}

func dashboardRequest(t *testing.T, server *Server, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, bytes.NewReader(body))
	request.Host = "127.0.0.1:8765"
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://127.0.0.1:8765")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

func writeDashboardFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func errorsIsNotExist(err error) bool {
	return err != nil && os.IsNotExist(err)
}

package dashboard

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
)

func TestServerEnforcesLoopbackHostAndStaticContainment(t *testing.T) {
	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{KnowledgeRoot: t.TempDir(), Assets: assets, Port: 8765, logger: log.New(io.Discard, "", 0)}

	badRequest := httptest.NewRequest(http.MethodGet, "http://example.test/api/health", nil)
	badRequest.Host = "example.test:8765"
	badResponse := httptest.NewRecorder()
	server.ServeHTTP(badResponse, badRequest)
	if badResponse.Code != http.StatusForbidden {
		t.Fatalf("bad host status = %d", badResponse.Code)
	}

	goodRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/health", nil)
	goodRequest.Host = "127.0.0.1:8765"
	goodResponse := httptest.NewRecorder()
	server.ServeHTTP(goodResponse, goodRequest)
	if goodResponse.Code != http.StatusOK {
		t.Fatalf("health status = %d, body = %s", goodResponse.Code, goodResponse.Body.String())
	}

	traversal := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/..%2Fgo.mod", nil)
	traversalResponse := httptest.NewRecorder()
	server.ServeHTTP(traversalResponse, traversal)
	if traversalResponse.Code != http.StatusNotFound {
		t.Fatalf("traversal status = %d", traversalResponse.Code)
	}
}

func TestWorkbenchStateIncludesActiveSnapshotDocuments(t *testing.T) {
	service, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	projectID, err := service.CreateProject("project", "workbench")
	if err != nil {
		t.Fatal(err)
	}
	documentID, err := service.CreateCandidate(projectID, "snapshot", "content", "test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.CreateSnapshot(projectID); err != nil {
		t.Fatal(err)
	}
	workbench := &Workbench{service: service, project: projectID, csrf: "token", host: "127.0.0.1:4321", created: map[string]bool{}}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4321/api/state", nil)
	request.Host = "127.0.0.1:4321"
	response := httptest.NewRecorder()
	workbench.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("workbench state status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Snapshot struct {
			Generation int `json:"generation"`
		} `json:"snapshot"`
		Documents []struct {
			ID       string              `json:"id"`
			Versions []knowledge.Version `json:"versions"`
		} `json:"snapshot_documents"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Snapshot.Generation != 1 || len(payload.Documents) != 1 || payload.Documents[0].ID != documentID || len(payload.Documents[0].Versions) != 1 {
		t.Fatalf("workbench state = %#v", payload)
	}
}

package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
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

func TestDashboardUpdateAPIUsesPreparedSnapshotBackend(t *testing.T) {
	backend := &fakeUpdateBackend{
		check:   upgrade.CheckResult{CurrentVersion: "0.6.10", LatestVersion: "0.7.0", UpdateAvailable: true},
		release: upgrade.Release{Version: "0.7.0", PageURL: "https://github.com/0tingqu0/ytqjk-marketplace/releases/tag/v0.7.0"},
	}
	server := &Server{
		KnowledgeRoot: t.TempDir(), Port: 8765, logger: log.New(io.Discard, "", 0),
		updateToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		updates:     backend,
	}
	statusRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/update", nil)
	statusRequest.Host = "127.0.0.1:8765"
	statusResponse := httptest.NewRecorder()
	server.ServeHTTP(statusResponse, statusRequest)
	if statusResponse.Code != http.StatusOK || !bytes.Contains(statusResponse.Body.Bytes(), []byte(`"upgrade_mode":"transactional-snapshot"`)) {
		t.Fatalf("status = %d, %s", statusResponse.Code, statusResponse.Body.String())
	}

	wrong := updateRequest(t, `{"token":"wrong"}`)
	wrongResponse := httptest.NewRecorder()
	server.ServeHTTP(wrongResponse, wrong)
	if wrongResponse.Code != http.StatusBadRequest || backend.launches != 0 {
		t.Fatalf("wrong token = %d, launches=%d", wrongResponse.Code, backend.launches)
	}

	request := updateRequest(t, `{"token":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || backend.prepares != 1 || backend.launches != 1 {
		t.Fatalf("update = %d, prepares=%d launches=%d, body=%s", response.Code, backend.prepares, backend.launches, response.Body.String())
	}
}

func TestDashboardPeerConfigurationNeverReturnsSecret(t *testing.T) {
	knowledgeRoot := t.TempDir()
	if err := safeio.WriteJSON(filepath.Join(knowledgeRoot, "catalog.json"), rag.Catalog{
		SchemaVersion: rag.SchemaVersion,
		Projects: map[string]rag.CatalogProject{
			"project-a": {Name: "Project A", TrackingState: "REGISTERED"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	server := &Server{KnowledgeRoot: knowledgeRoot, ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	defer server.closeStores()

	bootstrap := dashboardPost(t, "/api/peers/bootstrap", `{}`)
	bootstrapResponse := httptest.NewRecorder()
	server.ServeHTTP(bootstrapResponse, bootstrap)
	if bootstrapResponse.Code != http.StatusOK {
		t.Fatalf("bootstrap = %d, %s", bootstrapResponse.Code, bootstrapResponse.Body.String())
	}
	var initialized struct {
		Service peer.PublicSettings `json:"peer_service"`
	}
	if err := json.Unmarshal(bootstrapResponse.Body.Bytes(), &initialized); err != nil {
		t.Fatal(err)
	}
	secret, err := peer.NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{
		"expected_revision": initialized.Service.Revision,
		"peer_id":           "peer-remote", "title": "Remote", "project_id": "project-a",
		"endpoint": "http://127.0.0.1:18766", "secret": secret,
		"remote_node_id": nil, "export_node_ids": []string{"project-a"},
		"allow_insecure": false, "enabled": true,
	})
	upsert := dashboardPost(t, "/api/peers/upsert", string(body))
	upsertResponse := httptest.NewRecorder()
	server.ServeHTTP(upsertResponse, upsert)
	if upsertResponse.Code != http.StatusOK {
		t.Fatalf("upsert = %d, %s", upsertResponse.Code, upsertResponse.Body.String())
	}
	if bytes.Contains(upsertResponse.Body.Bytes(), []byte(secret)) || bytes.Contains(upsertResponse.Body.Bytes(), []byte(`"secret"`)) {
		t.Fatalf("secret leaked: %s", upsertResponse.Body.String())
	}
	var saved struct {
		Service peer.PublicSettings `json:"peer_service"`
	}
	if err := json.Unmarshal(upsertResponse.Body.Bytes(), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Service.Peers) != 1 || saved.Service.Peers[0].KeyFingerprint == "" {
		t.Fatalf("saved service = %#v", saved.Service)
	}

	staleBody, _ := json.Marshal(map[string]any{
		"expected_revision": initialized.Service.Revision,
		"peer_id":           "peer-remote", "title": "Remote", "project_id": "project-a",
		"endpoint": "http://127.0.0.1:18766", "secret": nil,
		"remote_node_id": nil, "export_node_ids": []string{"project-a"},
		"allow_insecure": false, "enabled": true,
	})
	stale := dashboardPost(t, "/api/peers/upsert", string(staleBody))
	staleResponse := httptest.NewRecorder()
	server.ServeHTTP(staleResponse, stale)
	if staleResponse.Code != http.StatusConflict {
		t.Fatalf("stale = %d, %s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestPeerRuntimeStartsOnlyForEnabledConfiguration(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir(), Port: 8765, logger: log.New(io.Discard, "", 0)}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	defer server.closeStores()
	settings, err := server.peerStore.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	port := unusedPort(t)
	if _, err := server.peerStore.Configure(context.Background(), settings.Revision, true, "127.0.0.1", port, false); err != nil {
		t.Fatal(err)
	}
	server.startPeerRuntime()
	if status := server.peerRuntimeStatus(); status.Status != "RUNNING" || status.Port != port {
		t.Fatalf("runtime = %#v", status)
	}
	server.stopPeerRuntime()
	if status := server.peerRuntimeStatus(); status.Status != "STOPPED" {
		t.Fatalf("stopped runtime = %#v", status)
	}
}

type fakeUpdateBackend struct {
	check    upgrade.CheckResult
	release  upgrade.Release
	prepares int
	launches int
}

func (backend *fakeUpdateBackend) Check(context.Context, string) (upgrade.CheckResult, upgrade.Release, error) {
	return backend.check, backend.release, nil
}

func (backend *fakeUpdateBackend) Prepare(_ context.Context, _ upgrade.Release, options upgrade.PrepareOptions) (upgrade.Plan, error) {
	backend.prepares++
	return upgrade.Plan{ToVersion: backend.release.Version, Port: options.Port}, nil
}

func (backend *fakeUpdateBackend) Launch(upgrade.Plan, int) error {
	backend.launches++
	return nil
}

func updateRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765/api/update", bytes.NewBufferString(body))
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	return request
}

func dashboardPost(t *testing.T, path, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8765"+path, bytes.NewBufferString(body))
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	return request
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
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
	workbench := &Workbench{
		project: projectID, csrf: "token", host: "127.0.0.1:4321", created: map[string]bool{},
		admit: func(_ context.Context, action func(*knowledge.Service) error) error {
			return action(service)
		},
	}
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

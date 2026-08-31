package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

type peerPair struct {
	client         *Client
	clientSettings Settings
	serverSettings Settings
	server         *httptest.Server
	secret         string
	projectID      string
	logs           *bytes.Buffer
}

func TestTwoPeersQueryAndFetchMaterial(t *testing.T) {
	pair := newPeerPair(t, "LAN_UNIQUE_MARKER")
	ctx := context.Background()
	health, err := pair.client.Health(ctx, pair.serverSettings.LocalPeerID, pair.projectID)
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "READY" || len(health.ExportNodes) != 1 || health.ExportNodes[0].ID != pair.projectID {
		t.Fatalf("health = %#v", health)
	}
	result, err := pair.client.Query(ctx, pair.serverSettings.LocalPeerID, pair.projectID, "LAN_UNIQUE_MARKER", 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "PEER_HIT" || len(result.Results) != 1 || result.Results[0].Content != "LAN_UNIQUE_MARKER" {
		t.Fatalf("query = %#v", result)
	}
	material, err := pair.client.Material(ctx, pair.serverSettings.LocalPeerID, pair.projectID, result.Results[0].MaterialID, result.Results[0].LibraryNode)
	if err != nil || material.Content != "LAN_UNIQUE_MARKER" {
		t.Fatalf("material = %#v, %v", material, err)
	}
	logText := pair.logs.String()
	if !strings.Contains(logText, "route=/v1/query") || strings.Contains(logText, "LAN_UNIQUE_MARKER") || strings.Contains(logText, pair.secret) {
		t.Fatalf("unsafe peer logs: %s", logText)
	}
}

func TestServerRejectsReplayAndCrossProject(t *testing.T) {
	pair := newPeerPair(t, "LAN_MARKER")
	body, _ := json.Marshal(map[string]any{
		"project_id": pair.projectID, "node_id": pair.projectID,
		"query": "LAN_MARKER", "limit": 5,
	})
	headers, err := SignedHeaders(pair.clientSettings.LocalPeerID, pair.secret, http.MethodPost, "/v1/query", body, timeNow(), strings.Repeat("R", 22))
	if err != nil {
		t.Fatal(err)
	}
	firstStatus, firstCode := rawPeerPost(t, pair.server.URL+"/v1/query", body, headers)
	secondStatus, secondCode := rawPeerPost(t, pair.server.URL+"/v1/query", body, headers)
	if firstStatus != http.StatusOK || firstCode != "" || secondStatus != http.StatusUnauthorized || secondCode != "PEER_REPLAY_REJECTED" {
		t.Fatalf("first=(%d,%s) second=(%d,%s)", firstStatus, firstCode, secondStatus, secondCode)
	}
	foreign, _ := json.Marshal(map[string]any{
		"project_id": "foreign-project", "node_id": pair.projectID,
		"query": "LAN_MARKER", "limit": 5,
	})
	foreignHeaders, _ := SignedHeaders(pair.clientSettings.LocalPeerID, pair.secret, http.MethodPost, "/v1/query", foreign, timeNow(), strings.Repeat("S", 22))
	status, code := rawPeerPost(t, pair.server.URL+"/v1/query", foreign, foreignHeaders)
	if status != http.StatusUnauthorized || code != "PEER_PROJECT_FORBIDDEN" {
		t.Fatalf("foreign = (%d,%s)", status, code)
	}
}

func TestServerRejectsDuplicateJSONFields(t *testing.T) {
	pair := newPeerPair(t, "LAN_MARKER")
	body := []byte(`{"project_id":"shared-project","project_id":"shared-project","node_id":"shared-project","query":"LAN_MARKER","limit":5}`)
	headers, _ := SignedHeaders(pair.clientSettings.LocalPeerID, pair.secret, http.MethodPost, "/v1/query", body, timeNow(), strings.Repeat("D", 22))
	status, code := rawPeerPost(t, pair.server.URL+"/v1/query", body, headers)
	if status != http.StatusBadRequest || code != "INVALID_PEER_REQUEST_FIELDS" {
		t.Fatalf("duplicate fields = (%d,%s)", status, code)
	}
}

func newPeerPair(t *testing.T, content string) peerPair {
	t.Helper()
	projectID := "shared-project"
	serverRoot := filepath.Join(t.TempDir(), "server")
	clientRoot := filepath.Join(t.TempDir(), "client")
	writeCatalog(t, serverRoot, projectID)
	writePeerIndex(t, filepath.Join(serverRoot, "projects", projectID), projectID, content)
	serverDatabase := filepath.Join(serverRoot, "service", "knowledge.sqlite3")
	serverStore, err := OpenStore(serverDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverStore.Close() })
	serverSettings, err := serverStore.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	serverTrees, err := tree.OpenStore(serverDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = serverTrees.Close() })
	if _, err := serverTrees.BootstrapProjects(context.Background(), []tree.Node{{NodeID: projectID, Title: "Shared", Kind: "project"}}); err != nil {
		t.Fatal(err)
	}
	clientStore, err := OpenStore(filepath.Join(clientRoot, "service", "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = clientStore.Close() })
	clientSettings, err := clientStore.Bootstrap(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := NewSecret()
	serverSettings, err = serverStore.Upsert(context.Background(), serverSettings.Revision, Record{
		PeerID: clientSettings.LocalPeerID, Title: "Client", ProjectID: projectID,
		Endpoint: "http://127.0.0.1:9", Secret: secret,
		RemoteNodeID: projectID, ExportNodeIDs: []string{projectID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	server := httptest.NewServer(&Handler{
		KnowledgeRoot: serverRoot, Peers: serverStore, Trees: serverTrees,
		Logger: log.New(logs, "", 0),
	})
	t.Cleanup(server.Close)
	clientSettings, err = clientStore.Upsert(context.Background(), clientSettings.Revision, Record{
		PeerID: serverSettings.LocalPeerID, Title: "Server", ProjectID: projectID,
		Endpoint: server.URL, Secret: secret, RemoteNodeID: projectID,
		ExportNodeIDs: []string{projectID}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return peerPair{
		client: NewClient(clientStore), clientSettings: clientSettings,
		serverSettings: serverSettings, server: server, secret: secret,
		projectID: projectID, logs: logs,
	}
}

func rawPeerPost(t *testing.T, endpoint string, body []byte, headers http.Header) (int, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header = headers.Clone()
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	var payload map[string]any
	if err := json.Unmarshal(content, &payload); err != nil {
		t.Fatal(err)
	}
	code, _ := payload["error"].(string)
	return response.StatusCode, code
}

func timeNow() time.Time { return time.Now() }

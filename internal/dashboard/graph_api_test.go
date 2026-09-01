package dashboard

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestKnowledgeGraphHTTPContract(t *testing.T) {
	root := writeGraphTestFixture(t)
	server := dashboardTestServer(root)

	response := dashboardRequest(t, server, http.MethodGet, "/api/knowledge-graph?limit=120", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("graph status = %d, body = %s", response.Code, response.Body.String())
	}
	var body struct {
		OK          bool           `json:"ok"`
		GeneratedAt string         `json:"generated_at"`
		Graph       knowledgeGraph `json:"graph"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.GeneratedAt == "" || body.Graph.Schema != 1 {
		t.Fatalf("graph envelope = %#v", body)
	}
	if body.Graph.Stats.Documents != 3 || body.Graph.Stats.SourceChunks != 3 {
		t.Fatalf("graph stats = %#v", body.Graph.Stats)
	}
	if len(body.Graph.Nodes) == 0 || len(body.Graph.Edges) == 0 {
		t.Fatalf("graph is empty: nodes=%d edges=%d", len(body.Graph.Nodes), len(body.Graph.Edges))
	}
	wantedDocument := graphDocumentID("global", "verified/go-upgrade.md")
	if !hasGraphNode(body.Graph.Nodes, wantedDocument) {
		t.Fatalf("global document %s missing", wantedDocument)
	}
	if !hasGraphEdgeType(body.Graph.Edges, "uses") || !hasGraphEdgeType(body.Graph.Edges, "supports") {
		t.Fatalf("relation edges missing: %#v", body.Graph.Edges)
	}
	if body.Graph.Capabilities.EntityExtraction != "go-rules-v1" || !body.Graph.Capabilities.Recommendations || !body.Graph.Capabilities.PathExploration {
		t.Fatalf("capabilities = %#v", body.Graph.Capabilities)
	}

	cached := dashboardRequest(t, server, http.MethodGet, "/api/knowledge-graph?limit=120", nil)
	if cached.Code != http.StatusOK || cached.Body.String() != response.Body.String() {
		t.Fatalf("unchanged graph was not served deterministically:\n%s\n%s", response.Body.String(), cached.Body.String())
	}
}

func TestKnowledgeSearchRecommendationAndPathContracts(t *testing.T) {
	root := writeGraphTestFixture(t)
	server := dashboardTestServer(root)

	search := dashboardRequest(t, server, http.MethodPost, "/api/knowledge-search", []byte(`{"query":"SnapshotEngine","limit":8}`))
	if search.Code != http.StatusOK {
		t.Fatalf("search status = %d, body = %s", search.Code, search.Body.String())
	}
	var searchBody struct {
		OK      bool                   `json:"ok"`
		Query   string                 `json:"query"`
		Mode    string                 `json:"mode"`
		Results []semanticSearchResult `json:"results"`
	}
	if err := json.Unmarshal(search.Body.Bytes(), &searchBody); err != nil {
		t.Fatal(err)
	}
	if !searchBody.OK || searchBody.Query != "SnapshotEngine" || searchBody.Mode != "concept-hybrid" || len(searchBody.Results) < 2 {
		t.Fatalf("search response = %#v", searchBody)
	}
	if searchBody.Results[0].NodeID == "" || searchBody.Results[0].Title == "" || searchBody.Results[0].Snippet == "" || searchBody.Results[0].LineStart < 1 {
		t.Fatalf("search result = %#v", searchBody.Results[0])
	}

	goServiceID := graphEntityID("GoService")
	recommendationPayload := []byte(`{"node_id":"` + goServiceID + `","limit":6}`)
	recommendation := dashboardRequest(t, server, http.MethodPost, "/api/knowledge-recommendations", recommendationPayload)
	if recommendation.Code != http.StatusOK {
		t.Fatalf("recommendation status = %d, body = %s", recommendation.Code, recommendation.Body.String())
	}
	var recommendationBody struct {
		OK      bool             `json:"ok"`
		NodeID  string           `json:"node_id"`
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(recommendation.Body.Bytes(), &recommendationBody); err != nil {
		t.Fatal(err)
	}
	if !recommendationBody.OK || recommendationBody.NodeID != goServiceID || !mapResultsContainLabel(recommendationBody.Results, "SnapshotEngine") {
		t.Fatalf("recommendation response = %#v", recommendationBody)
	}

	pathPayload := []byte(`{"source":"` + graphDocumentID("global", "verified/go-upgrade.md") + `","target":"` + graphEntityID("AtomicReplace") + `","max_depth":5}`)
	path := dashboardRequest(t, server, http.MethodPost, "/api/knowledge-path", pathPayload)
	if path.Code != http.StatusOK {
		t.Fatalf("path status = %d, body = %s", path.Code, path.Body.String())
	}
	var pathBody struct {
		OK    bool        `json:"ok"`
		Found bool        `json:"found"`
		Nodes []graphNode `json:"nodes"`
		Edges []graphEdge `json:"edges"`
		Hops  int         `json:"hops"`
	}
	if err := json.Unmarshal(path.Body.Bytes(), &pathBody); err != nil {
		t.Fatal(err)
	}
	if !pathBody.OK || !pathBody.Found || pathBody.Hops < 2 || len(pathBody.Nodes) != pathBody.Hops+1 || len(pathBody.Edges) != pathBody.Hops {
		t.Fatalf("path response = %#v", pathBody)
	}
	if pathBody.Nodes[0].ID != graphDocumentID("global", "verified/go-upgrade.md") || pathBody.Nodes[len(pathBody.Nodes)-1].ID != graphEntityID("AtomicReplace") {
		t.Fatalf("path endpoints = %#v", pathBody.Nodes)
	}
}

func TestKnowledgeGraphRequestValidation(t *testing.T) {
	server := dashboardTestServer(writeGraphTestFixture(t))
	tests := []struct {
		method string
		path   string
		body   []byte
		code   string
	}{
		{method: http.MethodGet, path: "/api/knowledge-graph?limit=19", code: "INVALID_LIMIT"},
		{method: http.MethodPost, path: "/api/knowledge-search", body: []byte(`{"query":"","limit":8}`), code: "EMPTY_QUERY"},
		{method: http.MethodPost, path: "/api/knowledge-search", body: []byte(`{"query":"Go","extra":true}`), code: "INVALID_REQUEST_FIELDS"},
		{method: http.MethodPost, path: "/api/knowledge-recommendations", body: []byte(`{"node_id":"","limit":6}`), code: "INVALID_NODE_ID"},
		{method: http.MethodPost, path: "/api/knowledge-path", body: []byte(`{"source":"doc:a","target":"doc:b","max_depth":7}`), code: "INVALID_MAX_DEPTH"},
	}
	for _, test := range tests {
		response := dashboardRequest(t, server, test.method, test.path, test.body)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"code":"`+test.code+`"`) {
			t.Errorf("%s %s = %d, %s", test.method, test.path, response.Code, response.Body.String())
		}
	}
}

func TestLibraryHTTPContracts(t *testing.T) {
	root := writeGraphTestFixture(t)
	server := dashboardTestServer(root)

	global := dashboardRequest(t, server, http.MethodGet, "/api/global-library", nil)
	if global.Code != http.StatusOK {
		t.Fatalf("global library status = %d, body = %s", global.Code, global.Body.String())
	}
	var globalBody struct {
		OK             bool             `json:"ok"`
		Files          [][]libraryChunk `json:"files"`
		FileCount      int              `json:"file_count"`
		ChunkCount     int              `json:"chunk_count"`
		ExpectedFiles  int              `json:"expected_files"`
		ExpectedChunks int              `json:"expected_chunks"`
	}
	if err := json.Unmarshal(global.Body.Bytes(), &globalBody); err != nil {
		t.Fatal(err)
	}
	if !globalBody.OK || globalBody.FileCount != 2 || globalBody.ChunkCount != 2 || len(globalBody.Files) != 2 || globalBody.ExpectedFiles != 2 || globalBody.ExpectedChunks != 2 {
		t.Fatalf("global library = %#v", globalBody)
	}
	if globalBody.Files[0][0].LineStart < 1 || globalBody.Files[0][0].SourceSHA256 == "" {
		t.Fatalf("global chunk = %#v", globalBody.Files[0][0])
	}

	project := dashboardRequest(t, server, http.MethodGet, "/api/project-library?id=project-a", nil)
	if project.Code != http.StatusOK {
		t.Fatalf("project library status = %d, body = %s", project.Code, project.Body.String())
	}
	var projectBody struct {
		OK       bool             `json:"ok"`
		Files    [][]libraryChunk `json:"files"`
		Prefetch []map[string]any `json:"prefetch"`
		Cache    map[string]any   `json:"cache"`
	}
	if err := json.Unmarshal(project.Body.Bytes(), &projectBody); err != nil {
		t.Fatal(err)
	}
	if !projectBody.OK || len(projectBody.Files) != 1 || projectBody.Prefetch == nil || projectBody.Cache["policy"] != "LFU_LRU" {
		t.Fatalf("project library = %#v", projectBody)
	}

	missingRoot := t.TempDir()
	missing := dashboardRequest(t, dashboardTestServer(missingRoot), http.MethodGet, "/api/project-library?id=missing", nil)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing project status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestKnowledgeGraphCacheIsConcurrent(t *testing.T) {
	server := dashboardTestServer(writeGraphTestFixture(t))
	const workers = 24
	start := make(chan struct{})
	errors := make(chan string, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			graph, generatedAt, revision := server.currentKnowledgeGraph(120)
			if generatedAt == "" || revision == "" || graph.Stats.Documents != 3 || len(graph.Nodes) == 0 {
				errors <- generatedAt
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for detail := range errors {
		t.Fatalf("concurrent graph result was incomplete: %q", detail)
	}
}

func writeGraphTestFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	globalDirectory := filepath.Join(root, "global-cache")
	projectDirectory := filepath.Join(root, "projects", "project-a")
	if err := os.MkdirAll(globalDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	globalChunks := []rag.Chunk{
		graphTestChunk("verified/go-upgrade.md", "# Go 升级\nGoService 使用 SnapshotEngine。\nSnapshotEngine 支持 VirtualAB。"),
		graphTestChunk("verified/snapshot.md", "# Snapshot Engine\nSnapshotEngine 依赖 AtomicReplace。\nAtomicReplace 支持 RollbackPlan。"),
	}
	projectChunks := []rag.Chunk{
		graphTestChunk("docs/app.md", "# App Architecture\nAppService 调用 GoService。"),
	}
	writeGraphTestIndex(t, globalDirectory, "global", "Global", globalChunks)
	writeGraphTestIndex(t, projectDirectory, "project-a", "Project A", projectChunks)
	return root
}

func writeGraphTestIndex(t *testing.T, directory, identifier, name string, chunks []rag.Chunk) {
	t.Helper()
	if err := safeio.WriteJSON(filepath.Join(directory, "index.json"), rag.Index{
		SchemaVersion: rag.SchemaVersion, ProjectID: identifier, Chunks: chunks,
	}); err != nil {
		t.Fatal(err)
	}
	manifest := rag.Manifest{
		SchemaVersion: rag.SchemaVersion,
		Identity:      rag.ProjectIdentity{ID: identifier, Name: name},
		Stats:         rag.Stats{Files: len(chunks), Chunks: len(chunks)},
		VectorMode:    "off", Vector: map[string]any{"enabled": false, "status": "DISABLED"},
		SourceFingerprint: safeio.SHA256([]byte(identifier)),
		IndexedAt:         "2026-08-31T00:00:00Z", UpdatedAt: "2026-08-31T00:00:00Z",
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}

func graphTestChunk(path, content string) rag.Chunk {
	digest := safeio.SHA256([]byte(content))
	return rag.Chunk{
		ID: safeio.SHA256([]byte(path + "\x00" + digest)), Path: path,
		Start: 0, End: utf8.RuneCountInString(content), LineStart: 1,
		LineEnd: strings.Count(content, "\n") + 1, Content: content, Digest: digest,
	}
}

func hasGraphNode(nodes []graphNode, identifier string) bool {
	for _, node := range nodes {
		if node.ID == identifier {
			return true
		}
	}
	return false
}

func hasGraphEdgeType(edges []graphEdge, edgeType string) bool {
	for _, edge := range edges {
		if edge.Type == edgeType {
			return true
		}
	}
	return false
}

func mapResultsContainLabel(results []map[string]any, label string) bool {
	for _, result := range results {
		if result["label"] == label {
			return true
		}
	}
	return false
}

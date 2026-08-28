package dashboard

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func TestKnowledgeGraphHTTPContractAndRevision(t *testing.T) {
	root := t.TempDir()
	writeGraphApprovedFile(
		t, root, "personal-experience/approved/docs/shared.md",
		"# 共享知识\n[[知识图谱]] 使用 `Graph API`\n知识图谱 支持 语义搜索",
	)
	writeGraphApprovedFile(
		t, root, "error-experience/approved/other/shared.md",
		"# 共享知识\n[[语义搜索]] 依赖 [[知识图谱]]",
	)
	writeGraphApprovedFile(
		t, root, "personal-experience/candidates/must-not-leak.md",
		"# 未批准知识\n[[候选秘密]] 关联 [[知识图谱]]",
	)
	server := newGraphTestServer(root)

	response := graphTestRequest(t, server, http.MethodGet, "/api/knowledge-graph?limit=120", "")
	if response.Code != http.StatusOK {
		t.Fatalf("graph status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		OK       bool           `json:"ok"`
		Revision string         `json:"revision"`
		Graph    knowledgeGraph `json:"graph"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if !payload.OK || payload.Revision == "" || payload.Graph.Schema != 1 {
		t.Fatalf("invalid graph envelope: %#v", payload)
	}

	displayLabels := map[string]bool{}
	documentID := ""
	entityID := ""
	var entityEvidence []graphEvidence
	for _, node := range payload.Graph.Nodes {
		if strings.Contains(node.Path, "/candidates/") || node.Label == "候选秘密" {
			t.Fatalf("candidate knowledge leaked into graph: %#v", node)
		}
		if node.Type == "document" && node.Label == "共享知识" {
			displayLabels[node.DisplayLabel] = true
			if node.Path == "personal-experience/approved/docs/shared.md" {
				documentID = node.ID
			}
		}
		if node.Type == "entity" && node.Label == "知识图谱" {
			entityID = node.ID
			entityEvidence = node.Evidence
		}
	}
	if len(displayLabels) != 2 || documentID == "" || entityID == "" {
		t.Fatalf("expected disambiguated documents and entity, nodes = %#v", payload.Graph.Nodes)
	}
	if len(entityEvidence) == 0 || entityEvidence[0].Excerpt == "" || entityEvidence[0].LineStart < 1 {
		t.Fatalf("entity evidence is not auditable: %#v", entityEvidence)
	}

	revisionResponse := graphTestRequest(t, server, http.MethodGet, "/api/knowledge-graph-revision", "")
	var revisionPayload struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(revisionResponse.Body.Bytes(), &revisionPayload); err != nil {
		t.Fatal(err)
	}
	if revisionResponse.Code != http.StatusOK || revisionPayload.Revision != payload.Revision {
		t.Fatalf("revision response = %d %q", revisionResponse.Code, revisionPayload.Revision)
	}
	writeGraphApprovedFile(
		t, root, "personal-experience/candidates/must-not-leak.md",
		"# 仍未批准\n[[候选秘密]] 关联 [[知识服务]]",
	)
	candidateResponse := graphTestRequest(t, server, http.MethodGet, "/api/knowledge-graph-revision", "")
	var candidatePayload struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(candidateResponse.Body.Bytes(), &candidatePayload); err != nil {
		t.Fatal(err)
	}
	if candidatePayload.Revision != payload.Revision {
		t.Fatalf("candidate change affected approved graph revision: %q", candidatePayload.Revision)
	}

	searchResponse := graphTestRequest(
		t, server, http.MethodPost, "/api/knowledge-search", `{"query":"语义搜索","limit":8}`,
	)
	var searchPayload struct {
		Results []map[string]any `json:"results"`
	}
	if err := json.Unmarshal(searchResponse.Body.Bytes(), &searchPayload); err != nil {
		t.Fatal(err)
	}
	if searchResponse.Code != http.StatusOK || len(searchPayload.Results) == 0 {
		t.Fatalf("search response = %d %s", searchResponse.Code, searchResponse.Body.String())
	}

	pathBody := `{"source":"` + documentID + `","target":"` + entityID + `","max_depth":5}`
	pathResponse := graphTestRequest(t, server, http.MethodPost, "/api/knowledge-path", pathBody)
	var pathPayload struct {
		Found bool        `json:"found"`
		Nodes []graphNode `json:"nodes"`
		Edges []graphEdge `json:"edges"`
	}
	if err := json.Unmarshal(pathResponse.Body.Bytes(), &pathPayload); err != nil {
		t.Fatal(err)
	}
	if pathResponse.Code != http.StatusOK || !pathPayload.Found || len(pathPayload.Nodes) < 2 || len(pathPayload.Edges) == 0 {
		t.Fatalf("path response = %d %s", pathResponse.Code, pathResponse.Body.String())
	}
	if pathPayload.Nodes[0].ID != documentID || pathPayload.Nodes[len(pathPayload.Nodes)-1].ID != entityID {
		t.Fatalf("path returned identifiers instead of ordered full nodes: %#v", pathPayload.Nodes)
	}

	writeGraphApprovedFile(
		t, root, "verified/new.md", "# 新知识\n[[知识服务]] 支持 [[知识图谱]]",
	)
	changedResponse := graphTestRequest(t, server, http.MethodGet, "/api/knowledge-graph-revision", "")
	var changedPayload struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal(changedResponse.Body.Bytes(), &changedPayload); err != nil {
		t.Fatal(err)
	}
	if changedPayload.Revision == "" || changedPayload.Revision == payload.Revision {
		t.Fatalf("revision did not change: before=%q after=%q", payload.Revision, changedPayload.Revision)
	}
}

func TestProjectIndexOffsetsAreNotReportedAsLines(t *testing.T) {
	root := t.TempDir()
	writeGraphProjectIndex(t, root, "project-a", "project-a", []rag.Chunk{
		{
			ID: "chunk-a", Path: "docs/project.md", Start: 0, End: 40,
			Content: "# 项目知识\n[[KnowledgeGraph]] abcdefghij",
		},
		{
			ID: "chunk-b", Path: "docs/project.md", Start: 38, End: 45,
			Content: "ijKLMNO",
		},
	})
	documents, _, err := loadGraphDocuments(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 1 {
		t.Fatalf("project documents = %#v", documents)
	}
	document := documents[0]
	if document.LineStart != 0 || document.LineEnd != 0 {
		t.Fatalf("rune offsets leaked as line numbers: %#v", document)
	}
	if document.Title != "项目知识" || strings.Contains(document.Content, "ijij") {
		t.Fatalf("project chunks were not merged correctly: %#v", document)
	}
	result, err := semanticGraphSearch(root, "KnowledgeGraph", 8)
	if err != nil {
		t.Fatal(err)
	}
	rows := result["results"].([]map[string]any)
	if len(rows) != 1 {
		t.Fatalf("project search results = %#v", rows)
	}
	if _, exists := rows[0]["line_start"]; exists {
		t.Fatalf("unknown project line number was serialized: %#v", rows[0])
	}
}

func TestProjectIndexCannotOverrideDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	writeGraphProjectIndex(t, root, "project-a", "project-b", []rag.Chunk{{
		ID: "chunk-a", Path: "same.md", Start: 0, End: 18, Content: "# 伪造归属\n[[知识图谱]]",
	}})
	documents, _, err := loadGraphDocuments(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(documents) != 0 {
		t.Fatalf("mismatched project identity was accepted: %#v", documents)
	}
}

func TestGraphProjectSourceBudgetBoundsCountAndBytes(t *testing.T) {
	byteBudget := graphProjectSourceBudget{}
	if !byteBudget.allow(maxGraphIndexBytes) || !byteBudget.allow(maxGraphIndexBytes) {
		t.Fatal("valid project index byte budget was rejected")
	}
	if byteBudget.allow(1) {
		t.Fatal("project index total byte budget was exceeded")
	}
	countBudget := graphProjectSourceBudget{}
	for index := 0; index < maxGraphProjectIndexes; index++ {
		if !countBudget.allow(1) {
			t.Fatalf("project source %d rejected before count limit", index)
		}
	}
	if countBudget.allow(1) {
		t.Fatal("project index count budget was exceeded")
	}
}

func TestGraphExtractionRejectsLowInformationTerms(t *testing.T) {
	mentions, relations := extractGraphKnowledge(
		"`if` `ArgumentParser` `KnowledgeGraph`\n[[知识图谱]] 使用 `Graph API`",
	)
	labels := map[string]bool{}
	for _, mention := range mentions {
		labels[mention.Label] = true
	}
	if labels["if"] || labels["ArgumentParser"] {
		t.Fatalf("low-information terms leaked into graph: %#v", labels)
	}
	if !labels["KnowledgeGraph"] || !labels["知识图谱"] || !labels["Graph API"] {
		t.Fatalf("meaningful terms missing from graph: %#v", labels)
	}
	if len(relations) != 1 || relations[0].Type != "uses" || relations[0].Source != "知识图谱" || relations[0].Target != "Graph API" {
		t.Fatalf("relation extraction = %#v", relations)
	}
}

func writeGraphApprovedFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeGraphProjectIndex(
	t *testing.T,
	root, directoryID, declaredID string,
	chunks []rag.Chunk,
) {
	t.Helper()
	directory := filepath.Join(root, "projects", directoryID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(rag.Index{SchemaVersion: 1, ProjectID: declaredID, Chunks: chunks})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "index.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func newGraphTestServer(root string) *Server {
	return &Server{
		KnowledgeRoot: root,
		Port:          8765,
		logger:        log.New(io.Discard, "", 0),
	}
}

func graphTestRequest(t *testing.T, server *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, "http://127.0.0.1:8765"+path, strings.NewReader(body))
	request.Host = "127.0.0.1:8765"
	if method != http.MethodGet {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", "http://127.0.0.1:8765")
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	return response
}

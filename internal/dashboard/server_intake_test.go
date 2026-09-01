package dashboard

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

func TestDashboardIntakeRunsPersistentGoJob(t *testing.T) {
	knowledgeRoot := t.TempDir()
	server := &Server{KnowledgeRoot: knowledgeRoot, Port: 8765, logger: log.New(io.Discard, "", 0)}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	defer server.closeStores()

	request := dashboardPost(t, "/api/intake", `{"name":"guide.md","content":"# Go guide\n\nUse the snapshot upgrade flow.","purpose":"migration","relativePath":"docs/guide.md","encoding":"utf8"}`)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake = %d, %s", response.Code, response.Body.String())
	}
	job := decodeDashboardIntakeJob(t, response.Body.Bytes())
	if !intakeJobID.MatchString(job.ID) || job.Stage != "validate" {
		t.Fatalf("accepted job = %#v", job)
	}
	job = waitForDashboardIntake(t, server, job.ID)
	if job.State != "SUCCEEDED" || job.Stage != "complete" || job.Progress != 100 || job.Result.Candidate.State != "CANDIDATE" || len(job.Result.Candidate.Chunks) == 0 {
		t.Fatalf("finished job = %#v", job)
	}
	candidate, err := safeDocumentPath(knowledgeRoot, job.Result.Candidate.Path)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(candidate)
	if err != nil || !bytes.Contains(content, []byte("Use the snapshot upgrade flow.")) || !bytes.Contains(content, []byte("status: CANDIDATE")) {
		t.Fatalf("candidate = %q, %v", content, err)
	}
	uploads, err := os.ReadDir(filepath.Join(knowledgeRoot, "service", "intake", "uploads"))
	if err != nil || len(uploads) != 0 {
		t.Fatalf("staged uploads were not cleaned: %v, %v", uploads, err)
	}
}

func TestDashboardIntakeFailsSecretsWithoutLeakingThem(t *testing.T) {
	knowledgeRoot := t.TempDir()
	server := &Server{KnowledgeRoot: knowledgeRoot, Port: 8765, logger: log.New(io.Discard, "", 0)}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	defer server.closeStores()

	request := dashboardPost(t, "/api/intake", `{"name":"secret.md","content":"authorization: bearer do-not-store-this","purpose":"test","relativePath":"","encoding":"utf8"}`)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("intake = %d, %s", response.Code, response.Body.String())
	}
	job := waitForDashboardIntake(t, server, decodeDashboardIntakeJob(t, response.Body.Bytes()).ID)
	if job.State != "FAILED" || job.Error.Category != "SECURITY" || job.Error.Retryable || len(job.Error.Ref) != 64 {
		t.Fatalf("secret job = %#v", job)
	}
	if bytes.Contains(mustJSON(t, job), []byte("do-not-store-this")) {
		t.Fatal("secret leaked through the public job response")
	}
	if countMarkdown(filepath.Join(knowledgeRoot, "personal-experience", "candidates")) != 0 {
		t.Fatal("secret intake created a candidate")
	}
}

func TestDashboardRebuildsGovernedGroupIndexInGo(t *testing.T) {
	knowledgeRoot := t.TempDir()
	server := &Server{KnowledgeRoot: knowledgeRoot, Port: 8765, logger: log.New(io.Discard, "", 0)}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	defer server.closeStores()
	value, err := server.treeStore.Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base := value.Revision()
	if err := value.AddNode(tree.Node{NodeID: "operations", Title: "Operations", Kind: "group"}, "global"); err != nil {
		t.Fatal(err)
	}
	if err := value.IncrementRevision(base); err != nil {
		t.Fatal(err)
	}
	if err := server.treeStore.Save(context.Background(), value, base); err != nil {
		t.Fatal(err)
	}
	approved := filepath.Join(knowledgeRoot, "personal-experience", "approved", "runbook.md")
	if err := safeio.AtomicWrite(approved, []byte("approved rollback runbook"), 0o600); err != nil {
		t.Fatal(err)
	}

	previewRequest := dashboardPost(t, "/api/libraries/preview", `{"action":"rebuild_index","payload":{"node_id":"operations","document_ids":[]}}`)
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d, %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Preview struct {
			Digest           string `json:"digest"`
			ExpectedRevision int64  `json:"expected_revision"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	commitBody, _ := json.Marshal(map[string]any{"digest": preview.Preview.Digest, "expected_revision": preview.Preview.ExpectedRevision})
	commitRequest := dashboardPost(t, "/api/libraries/rebuild-index", string(commitBody))
	commitResponse := httptest.NewRecorder()
	server.ServeHTTP(commitResponse, commitRequest)
	if commitResponse.Code != http.StatusOK {
		t.Fatalf("commit = %d, %s", commitResponse.Code, commitResponse.Body.String())
	}
	var committed struct {
		Action          string `json:"action"`
		Revision        int64  `json:"revision"`
		Materialization struct {
			Status      string `json:"status"`
			Documents   int    `json:"documents"`
			SourceScope string `json:"source_scope"`
		} `json:"materialization"`
		Tree struct {
			Nodes []struct {
				ID    string `json:"id"`
				Index struct {
					Status string `json:"status"`
				} `json:"index"`
			} `json:"nodes"`
		} `json:"tree"`
	}
	if err := json.Unmarshal(commitResponse.Body.Bytes(), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.Action != "rebuild_index" || committed.Revision != preview.Preview.ExpectedRevision || committed.Materialization.Status != "REBUILT" || committed.Materialization.Documents != 1 || committed.Materialization.SourceScope != "approved-verified-only" {
		t.Fatalf("commit body = %#v", committed)
	}
	foundReady := false
	for _, node := range committed.Tree.Nodes {
		if node.ID == "operations" && node.Index.Status == "READY" {
			foundReady = true
		}
	}
	if !foundReady {
		t.Fatalf("group index was not READY: %#v", committed.Tree.Nodes)
	}
}

type dashboardIntakeJob struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Stage     string `json:"stage"`
	Progress  int    `json:"progress"`
	PageCount *int   `json:"page_count"`
	Revision  int    `json:"revision"`
	Result    struct {
		Status    string `json:"status"`
		Retryable bool   `json:"retryable"`
		Candidate struct {
			State  string           `json:"state"`
			Path   string           `json:"path"`
			Chunks []map[string]any `json:"chunks"`
		} `json:"candidate"`
	} `json:"result"`
	Error struct {
		Category  string `json:"category"`
		Ref       string `json:"ref"`
		Retryable bool   `json:"retryable"`
	} `json:"error"`
}

func decodeDashboardIntakeJob(t *testing.T, value []byte) dashboardIntakeJob {
	t.Helper()
	var body struct {
		Job dashboardIntakeJob `json:"job"`
	}
	if err := json.Unmarshal(value, &body); err != nil {
		t.Fatal(err)
	}
	return body.Job
}

func waitForDashboardIntake(t *testing.T, server *Server, identifier string) dashboardIntakeJob {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/intake/jobs/"+identifier, nil)
		request.Host = "127.0.0.1:8765"
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("job status = %d, %s", response.Code, response.Body.String())
		}
		job := decodeDashboardIntakeJob(t, response.Body.Bytes())
		if job.State == "SUCCEEDED" || job.State == "FAILED" || job.State == "CANCELLED" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("intake job %s did not finish", identifier)
	return dashboardIntakeJob{}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

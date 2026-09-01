package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestDashboardIntakeRunsPersistentGoJob(t *testing.T) {
	knowledgeRoot := stableIntakeTempDir(t)
	server := &Server{KnowledgeRoot: knowledgeRoot, ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
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
	assertDirectoryStablyEmpty(t, filepath.Join(knowledgeRoot, "service", "intake", "uploads"))
}

func TestDashboardIntakeFailsSecretsWithoutLeakingThem(t *testing.T) {
	knowledgeRoot := stableIntakeTempDir(t)
	server := &Server{KnowledgeRoot: knowledgeRoot, ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
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
	assertDirectoryStablyEmpty(t, filepath.Join(knowledgeRoot, "service", "intake", "uploads"))
}

func TestDashboardRebuildsGovernedGroupIndexInGo(t *testing.T) {
	knowledgeRoot := t.TempDir()
	server := &Server{KnowledgeRoot: knowledgeRoot, ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
	before := createDashboardLibraryGroup(t, server, "operations")
	approved := filepath.Join(knowledgeRoot, "personal-experience", "approved", "runbook.md")
	if err := safeio.AtomicWrite(approved, []byte("approved rollback runbook"), 0o600); err != nil {
		t.Fatal(err)
	}

	previewRequest := dashboardPost(t, "/api/group-indexes/preview", `{"node_id":"operations","document_ids":[]}`)
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, previewRequest)
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d, %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Preview struct {
			Digest           string `json:"digest"`
			ExpectedRevision int64  `json:"expected_revision"`
			LibraryDigest    string `json:"library_digest"`
		} `json:"preview"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	commitBody, _ := json.Marshal(map[string]any{"digest": preview.Preview.Digest, "expected_revision": preview.Preview.ExpectedRevision})
	if preview.Preview.ExpectedRevision != before.Revision || preview.Preview.LibraryDigest != before.Digest {
		t.Fatalf("preview binding = %#v, tree = %#v", preview.Preview, before)
	}
	commitRequest := dashboardPost(t, "/api/group-indexes/rebuild", string(commitBody))
	commitResponse := httptest.NewRecorder()
	server.ServeHTTP(commitResponse, commitRequest)
	if commitResponse.Code != http.StatusOK {
		t.Fatalf("commit = %d, %s", commitResponse.Code, commitResponse.Body.String())
	}
	var committed struct {
		Action          string `json:"action"`
		LibraryRevision int64  `json:"library_revision"`
		LibraryDigest   string `json:"library_digest"`
		Materialization struct {
			Status      string `json:"status"`
			Documents   int    `json:"documents"`
			SourceScope string `json:"source_scope"`
		} `json:"materialization"`
	}
	if err := json.Unmarshal(commitResponse.Body.Bytes(), &committed); err != nil {
		t.Fatal(err)
	}
	if committed.Action != "rebuild" || committed.LibraryRevision != before.Revision ||
		committed.LibraryDigest != before.Digest || committed.Materialization.Status != "REBUILT" ||
		committed.Materialization.Documents != 1 || committed.Materialization.SourceScope != "approved-verified-only" {
		t.Fatalf("commit body = %#v", committed)
	}
	after := readTreeResponse(t, server)
	if after.Revision != before.Revision || after.Digest != before.Digest {
		t.Fatalf("group materialization changed Library topology: before=%#v after=%#v", before, after)
	}
	var groupNode library.Node
	for _, node := range after.Nodes {
		if node.ID == "operations" {
			groupNode = node
			break
		}
	}
	if groupNode.Stats.IndexedDocuments != 1 || groupNode.Stats.IndexedChunks < 1 || groupNode.Stats.UsedBytes < 1 {
		t.Fatalf("group Library statistics = %#v", groupNode.Stats)
	}
	if status := rag.ReadGroupStatus(knowledgeRoot, "operations"); status.Status != "READY" || status.Documents != 1 {
		t.Fatalf("group index status = %#v", status)
	}
	replayResponse := httptest.NewRecorder()
	server.ServeHTTP(replayResponse, dashboardPost(t, "/api/group-indexes/rebuild", string(commitBody)))
	if replayResponse.Code != http.StatusConflict {
		t.Fatalf("replay = %d, %s", replayResponse.Code, replayResponse.Body.String())
	}
	assertDashboardErrorCode(t, replayResponse, "PREVIEW_REPLAYED")
}

func TestGroupIndexPreviewRejectsTopologyChange(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir(), ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
	createDashboardLibraryGroup(t, server, "operations")
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, dashboardPost(t, "/api/group-indexes/preview", `{"node_id":"operations","document_ids":[]}`))
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
	createDashboardLibraryGroup(t, server, "changed")
	commitBody, err := json.Marshal(map[string]any{
		"digest": preview.Preview.Digest, "expected_revision": preview.Preview.ExpectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, dashboardPost(t, "/api/group-indexes/rebuild", string(commitBody)))
	if response.Code != http.StatusConflict {
		t.Fatalf("stale commit = %d, %s", response.Code, response.Body.String())
	}
	assertDashboardErrorCode(t, response, "REVISION_CONFLICT")
	retry := httptest.NewRecorder()
	server.ServeHTTP(retry, dashboardPost(t, "/api/group-indexes/rebuild", string(commitBody)))
	if retry.Code != http.StatusConflict {
		t.Fatalf("stale retry = %d, %s", retry.Code, retry.Body.String())
	}
	assertDashboardErrorCode(t, retry, "REVISION_CONFLICT")
}

func createDashboardLibraryGroup(t *testing.T, server *Server, nodeID string) library.Snapshot {
	t.Helper()
	previewBody, err := json.Marshal(map[string]any{
		"action": "create",
		"payload": map[string]any{
			"node_id": nodeID, "title": nodeID, "type": "group", "parent_id": "global",
			"capacity_bytes": int64(1024 * 1024 * 1024), "metadata": map[string]string{},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, dashboardPost(t, "/api/libraries/preview", string(previewBody)))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("Library preview = %d, %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Preview library.MutationPreview `json:"preview"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	commitBody, err := json.Marshal(map[string]any{
		"digest": preview.Preview.Digest, "expected_revision": preview.Preview.ExpectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	commitResponse := httptest.NewRecorder()
	server.ServeHTTP(commitResponse, dashboardPost(t, "/api/libraries/create", string(commitBody)))
	if commitResponse.Code != http.StatusOK {
		t.Fatalf("Library commit = %d, %s", commitResponse.Code, commitResponse.Body.String())
	}
	var committed struct {
		Tree library.Snapshot `json:"tree"`
	}
	if err := json.Unmarshal(commitResponse.Body.Bytes(), &committed); err != nil {
		t.Fatal(err)
	}
	return committed.Tree
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

func assertDirectoryStablyEmpty(t *testing.T, directory string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	const quietWindow = 50 * time.Millisecond
	var emptySince time.Time
	var (
		entries []os.DirEntry
		err     error
	)
	for time.Now().Before(deadline) {
		entries, err = os.ReadDir(directory)
		now := time.Now()
		if err == nil && len(entries) == 0 {
			if emptySince.IsZero() {
				emptySince = now
			}
			if now.Sub(emptySince) >= quietWindow {
				entries, err = os.ReadDir(directory)
				if err == nil && len(entries) == 0 {
					return
				}
				emptySince = time.Time{}
			}
		} else {
			emptySince = time.Time{}
		}
		time.Sleep(5 * time.Millisecond)
	}
	entries, err = os.ReadDir(directory)
	t.Fatalf("directory did not become stably empty: %v, %v", entries, err)
}

func stableIntakeTempDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "ytqjk-dashboard-intake-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		deadline := time.Now().Add(time.Second)
		const quietWindow = 50 * time.Millisecond
		var absentSince time.Time
		var cleanupErr error
		for time.Now().Before(deadline) {
			cleanupErr = os.RemoveAll(directory)
			now := time.Now()
			_, statErr := os.Lstat(directory)
			if cleanupErr == nil && errors.Is(statErr, os.ErrNotExist) {
				if absentSince.IsZero() {
					absentSince = now
				}
				if now.Sub(absentSince) >= quietWindow {
					_, statErr = os.Lstat(directory)
					if errors.Is(statErr, os.ErrNotExist) {
						return
					}
					absentSince = time.Time{}
				}
			} else {
				absentSince = time.Time{}
			}
			time.Sleep(5 * time.Millisecond)
		}
		_, statErr := os.Lstat(directory)
		t.Errorf("temporary intake directory cleanup did not stabilize: remove=%v stat=%v", cleanupErr, statErr)
	})
	return directory
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

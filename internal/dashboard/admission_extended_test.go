package dashboard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestDashboardStartupFailsClosedDuringMaintenance(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	scope := maintenance.Scope{ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot}
	lease, err := maintenance.BeginExclusive(context.Background(), scope, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("d", 64), Purpose: "DASHBOARD_STARTUP_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
			t.Errorf("complete maintenance lease: %v", err)
		}
	}()
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot, Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	if err := server.initializeStartup(context.Background()); !maintenance.IsCode(err, maintenance.CodeActive) {
		t.Fatalf("startup error = %v", err)
	}
	if server.treeStore != nil || server.peerStore != nil || server.intakeStore != nil {
		t.Fatal("startup initialized stores after admission closed")
	}
	if _, err := os.Lstat(server.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("pid publication exists after failed startup: %v", err)
	}
}

func TestRunDoesNotCreateMissingKnowledgeRootDuringMaintenance(t *testing.T) {
	knowledgeRoot := filepath.Join(t.TempDir(), "missing-knowledge")
	controlRoot := dashboardTestControlRoot(t)
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, ProspectiveRoots: []string{knowledgeRoot},
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("1", 64), Purpose: "DASHBOARD_MISSING_ROOT_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
			t.Errorf("complete maintenance lease: %v", err)
		}
	}()
	err = runWithControlRoot(knowledgeRoot, controlRoot, t.TempDir(), 8765, log.New(io.Discard, "", 0))
	if !maintenance.IsCode(err, maintenance.CodeInvalid) {
		t.Fatalf("missing knowledge root error = %v", err)
	}
	if _, err := os.Lstat(knowledgeRoot); !os.IsNotExist(err) {
		t.Fatalf("startup recreated missing knowledge root: %v", err)
	}
}

func TestDashboardStartupPublishesPIDAfterGuardedInitialization(t *testing.T) {
	server := &Server{
		KnowledgeRoot: t.TempDir(), ControlRoot: dashboardTestControlRoot(t), Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	if err := server.initializeStartup(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		server.stopPeerRuntime()
		server.closeStores()
		_ = os.Remove(server.pidPath())
	})
	if server.treeStore == nil || server.peerStore == nil || server.intakeStore == nil {
		t.Fatal("guarded startup did not initialize stores")
	}
	if content, err := os.ReadFile(server.pidPath()); err != nil || strings.TrimSpace(string(content)) == "" {
		t.Fatalf("pid publication = %q, %v", content, err)
	}
}

func TestStartupUnknownFinalizationFailsWithoutUnsafeCleanup(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	var logs bytes.Buffer
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot, Port: 8765,
		logger: log.New(&logs, "", 0),
	}
	var lease *maintenance.Lease
	t.Cleanup(func() {
		if lease != nil {
			_, _ = lease.Complete(maintenance.OutcomeAborted)
		}
		server.startupCommit = nil
		server.shutdown(context.Background())
	})
	server.startupCommit = func(
		permit *maintenance.Permit,
		action func(maintenance.Fence) error,
	) error {
		if err := permit.Commit(action); err != nil {
			return err
		}
		var err error
		lease, err = maintenance.BeginExclusive(context.Background(), maintenance.Scope{
			ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
		}, maintenance.ExclusiveOptions{
			OperationID: strings.Repeat("2", 64), Purpose: "DASHBOARD_FINALIZATION_TEST",
		})
		if err != nil {
			return err
		}
		return &maintenance.Error{Code: maintenance.CodeCommitResultUnknown, Err: errors.New("injected finalization error")}
	}
	if err := server.initializeStartup(context.Background()); !maintenance.IsCode(err, maintenance.CodeCommitResultUnknown) {
		t.Fatalf("startup finalization error = %v", err)
	}
	if server.treeStore == nil || server.peerStore == nil || server.intakeStore == nil {
		t.Fatal("startup cleaned stores after exclusive maintenance acquired")
	}
	if _, err := os.Lstat(server.pidPath()); err != nil {
		t.Fatalf("startup removed PID after exclusive maintenance acquired: %v", err)
	}
	if !strings.Contains(logs.String(), "injected finalization error") {
		t.Fatalf("startup log omitted finalization error: %q", logs.String())
	}

	server.shutdown(context.Background())
	if server.treeStore == nil || server.peerStore == nil || server.intakeStore == nil {
		t.Fatal("shutdown closed stores without shared admission")
	}
	if _, err := os.Lstat(server.pidPath()); err != nil {
		t.Fatalf("shutdown removed PID without shared admission: %v", err)
	}
	if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
		t.Fatal(err)
	}
	lease = nil
	server.startupCommit = nil
	server.shutdown(context.Background())
	if server.treeStore != nil || server.peerStore != nil || server.intakeStore != nil {
		t.Fatal("guarded shutdown left stores open")
	}
	if _, err := os.Lstat(server.pidPath()); !os.IsNotExist(err) {
		t.Fatalf("guarded shutdown left PID behind: %v", err)
	}
}

func TestDashboardPreviewAndTreeRoutesCloseDuringMaintenance(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot, Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("e", 64), Purpose: "DASHBOARD_PREVIEW_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = lease.Complete(maintenance.OutcomeAborted) }()
	requests := []*http.Request{
		dashboardPost(t, "/api/libraries/preview", `{}`),
		dashboardPost(t, "/api/group-indexes/preview", `{}`),
		dashboardGet("/api/libraries/tree"),
		dashboardGet("/api/project-library?id=project"),
	}
	for _, request := range requests {
		response := httptest.NewRecorder()
		server.ServeHTTP(response, request)
		if response.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, %s", request.URL.Path, response.Code, response.Body.String())
			continue
		}
		assertDashboardErrorCode(t, response, maintenance.CodeActive)
	}
}

func TestPeerAdmissionWrapsWholeRequest(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot,
		logger: log.New(io.Discard, "", 0),
	}
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("f", 64), Purpose: "PEER_ADMISSION_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	contextBound := false
	handler := &peerAdmissionHandler{server: server, next: http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		called = true
		_, fenceErr := maintenance.SharedFenceFromContext(request.Context(), maintenance.Scope{
			ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
		})
		contextBound = fenceErr == nil
	})}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/health", strings.NewReader(`{}`)))
	if response.Code != http.StatusServiceUnavailable || called || response.Header().Get("Retry-After") != "5" {
		t.Fatalf("active peer response=%d called=%v headers=%v body=%s", response.Code, called, response.Header(), response.Body.String())
	}
	assertPeerMaintenanceCode(t, response, maintenance.CodeActive)
	if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
		t.Fatal(err)
	}
	accepted := httptest.NewRecorder()
	handler.ServeHTTP(accepted, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/health", strings.NewReader(`{}`)))
	if !called || !contextBound {
		t.Fatalf("admitted peer request called=%v context_bound=%v", called, contextBound)
	}

	permanent := httptest.NewRecorder()
	badServer := &Server{KnowledgeRoot: knowledgeRoot, logger: log.New(io.Discard, "", 0)}
	(&peerAdmissionHandler{server: badServer, next: handler.next}).ServeHTTP(
		permanent, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/health", strings.NewReader(`{}`)),
	)
	if permanent.Header().Get("Retry-After") != "" {
		t.Fatalf("permanent peer error advertised retry: %v", permanent.Header())
	}
	assertPeerMaintenanceCode(t, permanent, maintenance.CodeInvalid)
}

func TestPeerAdmissionDiscardsBufferedSuccessWhenFinalFenceUnknown(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot,
		logger: log.New(io.Discard, "", 0),
	}
	recordPath := filepath.Join(controlRoot, "maintenance", "record.json")
	handler := &peerAdmissionHandler{server: server, next: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-Uncommitted-Response", "discard")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"ok":true}`))
		if err := os.WriteFile(recordPath, []byte("{"), 0o600); err != nil {
			t.Errorf("corrupt maintenance record: %v", err)
		}
	})}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/health", strings.NewReader(`{}`)))
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("X-Uncommitted-Response") != "" {
		t.Fatalf("response=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
	if strings.Contains(response.Body.String(), `"ok":true`) || response.Header().Get("Retry-After") != "" {
		t.Fatalf("uncommitted success escaped: headers=%v body=%s", response.Header(), response.Body.String())
	}
	assertPeerMaintenanceCode(t, response, maintenance.CodeCommitResultUnknown)
}

func TestIntakePermanentAdmissionErrorStopsAutomaticRetry(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	var logs bytes.Buffer
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot,
		logger: log.New(&logs, "", 0),
	}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.closeStores)
	job, err := server.intakeStore.Enqueue(context.Background(), map[string]any{
		"name": "queued.md", "staging_ref": "service/intake/uploads/queued.md",
		"source_sha256": strings.Repeat("a", 64),
	}, map[string]any{"extractor": "go-document-v1"})
	if err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(controlRoot, "maintenance", "record.json")
	if err := os.WriteFile(record, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if mode := server.processIntakeJob(job.ID); mode != intakeRetryNone {
		t.Fatalf("retry mode = %d, want none", mode)
	}
	current, err := server.intakeStore.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "QUEUED" || current.Attempt != 0 || !strings.Contains(logs.String(), maintenance.CodeStateCorrupt) {
		t.Fatalf("job=%#v logs=%q", current, logs.String())
	}
}

func TestIntakeStatusDoesNotDeleteCompletedSource(t *testing.T) {
	knowledgeRoot := t.TempDir()
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: dashboardTestControlRoot(t), Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.closeStores)
	content := []byte("# durable intake\n\ncontent")
	digest := sha256.Sum256(content)
	reference := "service/intake/uploads/completed.md"
	path, err := server.intakeSourcePath(reference)
	if err != nil {
		t.Fatal(err)
	}
	if err := safeio.AtomicWrite(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	job, err := server.intakeStore.Enqueue(context.Background(), map[string]any{
		"name": "completed.md", "purpose": "test", "relative_path": "",
		"staging_ref": reference, "source_sha256": hex.EncodeToString(digest[:]),
	}, map[string]any{"extractor": "go-document-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if mode := server.processIntakeJob(job.ID); mode != intakeRetryImmediate {
		t.Fatalf("process retry mode = %d", mode)
	}
	if err := safeio.AtomicWrite(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, dashboardGet("/api/intake/jobs/"+job.ID))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, %s", response.Code, response.Body.String())
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("GET status deleted completed source: %v", err)
	}
}

func dashboardGet(path string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765"+path, nil)
	request.Host = "127.0.0.1:8765"
	return request
}

func assertPeerMaintenanceCode(t *testing.T, response *httptest.ResponseRecorder, code string) {
	t.Helper()
	var payload struct {
		Error APIError `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != code || payload.Error.Message != code {
		t.Fatalf("peer maintenance payload = %#v", payload)
	}
}

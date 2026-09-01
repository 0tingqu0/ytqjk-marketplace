package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

func TestDurableMutationRequestClassifiesRoutesExplicitly(t *testing.T) {
	jobID := "01234567-89ab-cdef-0123-456789abcdef"
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodPost, "/api/intake", true},
		{http.MethodPost, "/api/candidate/approve", true},
		{http.MethodPut, "/api/candidate", true},
		{http.MethodDelete, "/api/candidate", true},
		{http.MethodPost, "/api/intake/jobs/" + jobID + "/retry", true},
		{http.MethodPost, "/api/intake/jobs/" + jobID + "/cancel", true},
		{http.MethodPost, "/api/libraries/create", true},
		{http.MethodPost, "/api/libraries/attach", true},
		{http.MethodPost, "/api/libraries/detach", true},
		{http.MethodPost, "/api/libraries/move", true},
		{http.MethodPost, "/api/libraries/insert-between", true},
		{http.MethodPost, "/api/libraries/preview", true},
		{http.MethodPost, "/api/group-indexes/preview", true},
		{http.MethodPost, "/api/group-indexes/rebuild", true},
		{http.MethodPost, "/api/peers/bootstrap", true},
		{http.MethodPost, "/api/peers/secret", true},
		{http.MethodPost, "/api/peers/configure", true},
		{http.MethodPost, "/api/peers/upsert", true},
		{http.MethodPost, "/api/peers/remove", true},
		{http.MethodPost, "/api/knowledge-search", false},
		{http.MethodPost, "/api/knowledge-recommendations", false},
		{http.MethodPost, "/api/knowledge-path", false},
		{http.MethodPost, "/api/update", false},
		{http.MethodPost, "/api/peers/discover", false},
		{http.MethodPost, "/api/peers/health", false},
		{http.MethodPost, "/api/peers/dispatch", false},
		{http.MethodPost, "/api/peers/material", false},
		{http.MethodPost, "/api/intake/jobs/not-a-job/retry", false},
		{http.MethodPost, "/api/peers/unknown", false},
		{http.MethodGet, "/api/libraries/tree", true},
		{http.MethodGet, "/api/project-library", true},
		{http.MethodGet, "/api/intake/jobs/" + jobID, false},
	}
	for _, test := range tests {
		if got := durableMutationRequest(test.method, test.path); got != test.want {
			t.Errorf("durableMutationRequest(%s, %s) = %v, want %v", test.method, test.path, got, test.want)
		}
	}
}

func TestDashboardDurableMutationReturnsMaintenanceContract(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot, Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	scope := maintenance.Scope{ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot}
	lease, err := maintenance.BeginExclusive(context.Background(), scope, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("a", 64), Purpose: "DASHBOARD_ADMISSION_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
			t.Errorf("complete maintenance lease: %v", err)
		}
	})

	response := httptest.NewRecorder()
	server.ServeHTTP(response, dashboardPost(t, "/api/intake", `{}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Retry-After") != "5" {
		t.Fatalf("Retry-After = %q", response.Header().Get("Retry-After"))
	}
	assertDashboardErrorCode(t, response, maintenance.CodeActive)

	update := httptest.NewRecorder()
	server.ServeHTTP(update, dashboardPost(t, "/api/update", `{}`))
	if update.Code == http.StatusServiceUnavailable {
		t.Fatalf("update self-deadlocked on shared admission: %s", update.Body.String())
	}
	healthRequest := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8765/api/health", nil)
	healthRequest.Host = "127.0.0.1:8765"
	health := httptest.NewRecorder()
	server.ServeHTTP(health, healthRequest)
	if health.Code != http.StatusOK {
		t.Fatalf("read request was blocked: %d, %s", health.Code, health.Body.String())
	}
}

func TestDashboardRequiresInjectedControlRootForMutation(t *testing.T) {
	server := &Server{
		KnowledgeRoot: t.TempDir(), Port: 8765, logger: log.New(io.Discard, "", 0),
	}
	response := httptest.NewRecorder()
	server.ServeHTTP(response, dashboardPost(t, "/api/candidate/approve", `{}`))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	assertDashboardErrorCode(t, response, maintenance.CodeInvalid)
}

func TestMaintenanceErrorsUseCanonicalHTTPContract(t *testing.T) {
	for _, test := range []struct {
		code       string
		retryAfter string
	}{
		{maintenance.CodeActive, "5"},
		{maintenance.CodeWriterDrainTimeout, "5"},
		{maintenance.CodeRecoveryRequired, ""},
		{maintenance.CodeStateCorrupt, ""},
		{maintenance.CodeGenerationConflict, ""},
		{maintenance.CodeCommitResultUnknown, ""},
	} {
		t.Run(test.code, func(t *testing.T) {
			response := httptest.NewRecorder()
			status := writeMaintenanceUnavailable(response, &maintenance.Error{Code: test.code})
			if status != http.StatusServiceUnavailable || response.Header().Get("Retry-After") != test.retryAfter {
				t.Fatalf("status=%d Retry-After=%q", status, response.Header().Get("Retry-After"))
			}
			var payload struct {
				Error APIError `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Error.Code != test.code || payload.Error.Message != test.code {
				t.Fatalf("payload = %#v", payload)
			}
		})
	}
}

func TestIntakeWorkerKeepsJobQueuedDuringMaintenance(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := dashboardTestControlRoot(t)
	server := &Server{
		KnowledgeRoot: knowledgeRoot, ControlRoot: controlRoot, Port: 8765,
		logger: log.New(io.Discard, "", 0),
	}
	if err := server.ensureStores(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.closeStores)
	job, err := server.intakeStore.Enqueue(context.Background(), map[string]any{
		"name": "queued.md", "staging_ref": "service/intake/uploads/queued.md",
		"source_sha256": strings.Repeat("b", 64),
	}, map[string]any{"extractor": "go-document-v1"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("c", 64), Purpose: "INTAKE_ADMISSION_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := lease.Complete(maintenance.OutcomeAborted); err != nil {
			t.Errorf("complete maintenance lease: %v", err)
		}
	}()
	if mode := server.processIntakeJob(job.ID); mode != intakeRetryDelayed {
		t.Fatalf("worker retry mode = %d, want delayed", mode)
	}
	current, err := server.intakeStore.Get(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.State != "QUEUED" || current.Attempt != 0 || current.ErrorCategory != "" {
		t.Fatalf("job changed during maintenance: %#v", current)
	}
}

func dashboardTestControlRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := maintenance.BootstrapControlRoot(context.Background(), root); err != nil {
		t.Fatalf("bootstrap maintenance control root: %v", err)
	}
	return root
}

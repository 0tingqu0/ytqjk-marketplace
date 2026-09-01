package dashboard

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

func TestWorkbenchDiscardsBufferedMutationSuccessWhenAdmissionUnknown(t *testing.T) {
	service, err := knowledge.Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	projectID, err := service.CreateProject("project", "workbench-admission")
	if err != nil {
		t.Fatal(err)
	}
	workbench := &Workbench{
		project: projectID, csrf: "token", host: "127.0.0.1:4321", created: map[string]bool{},
		admit: func(_ context.Context, action func(*knowledge.Service) error) error {
			if err := action(service); err != nil {
				return err
			}
			return &maintenance.Error{Code: maintenance.CodeCommitResultUnknown}
		},
	}
	request := httptest.NewRequest(
		http.MethodPost,
		"http://127.0.0.1:4321/api/candidates",
		bytes.NewBufferString(`{"title":"candidate","content":"body","source":"test"}`),
	)
	request.Host = "127.0.0.1:4321"
	request.Header.Set("Origin", "http://127.0.0.1:4321")
	request.Header.Set("X-CSRF-Token", "token")
	response := httptest.NewRecorder()
	workbench.ServeHTTP(response, request)

	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("workbench status = %d, body = %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "CANDIDATE_CREATED") {
		t.Fatalf("buffered success escaped final fence: %s", response.Body.String())
	}
	if response.Header().Get("Retry-After") != "" {
		t.Fatalf("unknown commit must not advertise a blind retry")
	}
	assertDashboardErrorCode(t, response, maintenance.CodeCommitResultUnknown)
}

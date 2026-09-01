package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
)

func TestGroupIndexPreviewRejectsMalformedContracts(t *testing.T) {
	tests := []struct {
		name string
		body string
		code string
	}{
		{"duplicate key", `{"node_id":"operations","node_id":"other","document_ids":[]}`, "DUPLICATE_JSON_KEY"},
		{"null documents", `{"node_id":"operations","document_ids":null}`, "INVALID_REQUEST_FIELDS"},
		{"missing documents", `{"node_id":"operations"}`, "INVALID_REQUEST_FIELDS"},
		{"extra field", `{"node_id":"operations","document_ids":[],"legacy":true}`, "INVALID_REQUEST_FIELDS"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{KnowledgeRoot: t.TempDir(), ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, dashboardPost(t, "/api/group-indexes/preview", test.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertDashboardErrorCode(t, response, test.code)
		})
	}
}

func TestGroupIndexCommitUsesCanonicalCASContract(t *testing.T) {
	digest := strings.Repeat("a", 64)
	tests := []struct {
		name string
		body string
		code string
	}{
		{"negative revision", `{"digest":"` + digest + `","expected_revision":-1}`, "INVALID_EXPECTED_REVISION"},
		{"invalid digest", `{"digest":"invalid","expected_revision":0}`, "INVALID_PREVIEW_DIGEST"},
		{"duplicate revision", `{"digest":"` + digest + `","expected_revision":0,"expected_revision":1}`, "DUPLICATE_JSON_KEY"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &Server{KnowledgeRoot: t.TempDir(), ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
			response := httptest.NewRecorder()
			server.ServeHTTP(response, dashboardPost(t, "/api/group-indexes/rebuild", test.body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			assertDashboardErrorCode(t, response, test.code)
		})
	}
}

func TestGroupIndexPreviewClaimStateTransitions(t *testing.T) {
	digest := strings.Repeat("b", 64)
	server := &Server{groupPreviews: map[string]issuedGroupIndexPreview{
		digest: {ExpectedRevision: 7, CreatedAt: time.Now().UTC(), State: groupPreviewActive},
	}}
	request := library.CommitRequest{Digest: digest, ExpectedRevision: 7}
	if _, status, code := server.claimGroupIndexPreview(request); status != 0 || code != "" {
		t.Fatalf("first claim = %d, %s", status, code)
	}
	if _, status, code := server.claimGroupIndexPreview(request); status != http.StatusConflict || code != "PREVIEW_IN_PROGRESS" {
		t.Fatalf("concurrent claim = %d, %s", status, code)
	}
	server.finishGroupIndexPreview(digest, false)
	if _, status, code := server.claimGroupIndexPreview(request); status != 0 || code != "" {
		t.Fatalf("retry claim = %d, %s", status, code)
	}
	server.finishGroupIndexPreview(digest, true)
	if _, status, code := server.claimGroupIndexPreview(request); status != http.StatusConflict || code != "PREVIEW_REPLAYED" {
		t.Fatalf("consumed claim = %d, %s", status, code)
	}
}

func TestGroupIndexRebuildSurvivesRequestCancellation(t *testing.T) {
	server := &Server{KnowledgeRoot: t.TempDir(), ControlRoot: dashboardTestControlRoot(t), Port: 8765, logger: log.New(io.Discard, "", 0)}
	createDashboardLibraryGroup(t, server, "operations")
	previewResponse := httptest.NewRecorder()
	server.ServeHTTP(previewResponse, dashboardPost(t, "/api/group-indexes/preview", `{"node_id":"operations","document_ids":[]}`))
	if previewResponse.Code != http.StatusOK {
		t.Fatalf("preview = %d, %s", previewResponse.Code, previewResponse.Body.String())
	}
	var preview struct {
		Preview library.MutationPreview `json:"preview"`
	}
	if err := json.Unmarshal(previewResponse.Body.Bytes(), &preview); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"digest": preview.Preview.Digest, "expected_revision": preview.Preview.ExpectedRevision,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := dashboardPost(t, "/api/group-indexes/rebuild", string(body)).WithContext(requestContext)
	response := httptest.NewRecorder()
	server.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("cancelled request commit = %d, %s", response.Code, response.Body.String())
	}
}

func TestGroupIndexPreviewCapacityPreservesRunningRecords(t *testing.T) {
	server := &Server{groupPreviews: make(map[string]issuedGroupIndexPreview)}
	createdAt := time.Now().UTC().Add(-2 * groupIndexPreviewTTL)
	for index := 0; index < maxGroupIndexPreview; index++ {
		server.groupPreviews[fmt.Sprintf("running-%03d", index)] = issuedGroupIndexPreview{
			CreatedAt: createdAt, State: groupPreviewRunning,
		}
	}
	if server.storeGroupIndexPreview("new", issuedGroupIndexPreview{CreatedAt: time.Now().UTC(), State: groupPreviewActive}) {
		t.Fatal("preview was stored by evicting an in-progress operation")
	}
	if len(server.groupPreviews) != maxGroupIndexPreview {
		t.Fatalf("preview count = %d", len(server.groupPreviews))
	}
	server.groupPreviews["running-000"] = issuedGroupIndexPreview{CreatedAt: createdAt, State: groupPreviewConsumed}
	if !server.storeGroupIndexPreview("new", issuedGroupIndexPreview{CreatedAt: time.Now().UTC(), State: groupPreviewActive}) {
		t.Fatal("completed preview was not reclaimed")
	}
	if _, found := server.groupPreviews["running-000"]; found {
		t.Fatal("old completed preview was retained")
	}
}

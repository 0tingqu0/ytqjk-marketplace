package dashboard

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateRelativeRejectsUnsafePaths(t *testing.T) {
	invalid := []string{
		"personal-experience/candidates/../../catalog.json",
		"error-experience/candidates/../../../catalog.json",
		"personal-experience/candidates-archive/item.md",
		"personal-experience/candidates/chunks/item.md",
		"error-experience/candidates/originals/item.md",
		"personal-experience/candidates/item.txt",
		"catalog.json",
	}
	for _, relative := range invalid {
		t.Run(relative, func(t *testing.T) {
			if _, err := candidateRelative(relative); err == nil {
				t.Fatalf("candidateRelative(%q) succeeded", relative)
			}
		})
	}
}

func TestUpdateCandidateCannotOverwriteCatalog(t *testing.T) {
	root := t.TempDir()
	catalog := filepath.Join(root, "catalog.json")
	if err := os.WriteFile(catalog, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{
		KnowledgeRoot: root,
		ControlRoot:   dashboardTestControlRoot(t),
		Port:          8765,
		logger:        log.New(io.Discard, "", 0),
	}
	body := []byte(`{"path":"personal-experience/candidates/../../catalog.json","content":"overwritten","expected_version":"` + strings.Repeat("0", 64) + `"}`)
	request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8765/api/candidate", bytes.NewReader(body))
	request.Host = "127.0.0.1:8765"
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "http://127.0.0.1:8765")
	response := httptest.NewRecorder()

	server.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	data, err := os.ReadFile(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("catalog = %q, want original", data)
	}
}

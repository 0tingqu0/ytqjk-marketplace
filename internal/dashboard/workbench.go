package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

type Workbench struct {
	admit     WorkbenchAdmission
	project   string
	assets    string
	csrf      string
	host      string
	created   map[string]bool
	createdMu sync.Mutex
}

// WorkbenchAdmission owns one short-lived knowledge service from open through
// close. Implementations must not return until the final maintenance fence has
// been checked.
type WorkbenchAdmission func(context.Context, func(*knowledge.Service) error) error

func RunWorkbench(
	projectID, assets string,
	port int,
	output io.Writer,
	admit WorkbenchAdmission,
) error {
	if port < 0 || port > 65535 {
		return errors.New("only ports 0..65535 are permitted")
	}
	if admit == nil {
		return errors.New("workbench admission is required")
	}
	if err := admit(context.Background(), func(*knowledge.Service) error { return nil }); err != nil {
		return err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return err
	}
	workbench := &Workbench{
		admit: admit, project: projectID, assets: assets,
		csrf: base64.RawURLEncoding.EncodeToString(tokenBytes), created: map[string]bool{},
	}
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	defer listener.Close()
	actual := listener.Addr().(*net.TCPAddr).Port
	workbench.host = fmt.Sprintf("127.0.0.1:%d", actual)
	data, _ := json.Marshal(map[string]any{"address": fmt.Sprintf("http://127.0.0.1:%d", actual), "bind": "127.0.0.1"})
	fmt.Fprintln(output, string(data))
	server := &http.Server{Handler: workbench, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 30 * time.Second}
	return server.Serve(listener)
}

func (w *Workbench) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Host != w.host {
		workbenchError(writer, http.StatusForbidden, "LOCAL_HOST_REQUIRED")
		return
	}
	if request.Method == http.MethodGet {
		switch request.URL.Path {
		case "/api/state":
			w.withService(writer, request, func(response http.ResponseWriter, service *knowledge.Service) {
				w.state(response, service)
			})
			return
		case "/":
			w.asset(writer, "index.html", "text/html; charset=utf-8")
			return
		case "/app.css":
			w.asset(writer, "app.css", "text/css; charset=utf-8")
			return
		case "/app.js":
			w.asset(writer, "app.js", "text/javascript; charset=utf-8")
			return
		}
	}
	if request.Method != http.MethodPost {
		workbenchError(writer, http.StatusNotFound, "NOT_FOUND")
		return
	}
	origin := "http://" + request.Host
	if request.Header.Get("Origin") != origin || request.Header.Get("X-CSRF-Token") != w.csrf {
		workbenchError(writer, http.StatusForbidden, "CSRF_REQUIRED")
		return
	}
	var payload map[string]any
	decoder := json.NewDecoder(io.LimitReader(request.Body, 65537))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		workbenchError(writer, http.StatusBadRequest, "INVALID_JSON")
		return
	}
	switch request.URL.Path {
	case "/api/candidates":
		w.withService(writer, request, func(response http.ResponseWriter, service *knowledge.Service) {
			documentID, err := service.CreateCandidate(
				w.project, requiredText(payload, "title"),
				requiredText(payload, "content"), requiredText(payload, "source"),
			)
			if err != nil {
				workbenchError(response, http.StatusBadRequest, "INVALID_REQUEST")
				return
			}
			w.createdMu.Lock()
			w.created[documentID] = true
			w.createdMu.Unlock()
			workbenchJSON(response, http.StatusOK, map[string]any{
				"status": "CANDIDATE_CREATED", "document_id": documentID,
			})
		})
	case "/api/candidates/edit":
		w.withService(writer, request, func(response http.ResponseWriter, service *knowledge.Service) {
			documentID := requiredText(payload, "document_id")
			if !w.owned(documentID) || service.EditCandidate(
				documentID, requiredText(payload, "content"), requiredText(payload, "source"),
			) != nil {
				workbenchError(response, http.StatusBadRequest, "INVALID_REQUEST")
				return
			}
			workbenchJSON(response, http.StatusOK, map[string]any{"status": "CANDIDATE_UPDATED"})
		})
	case "/api/candidates/delete":
		w.withService(writer, request, func(response http.ResponseWriter, service *knowledge.Service) {
			documentID := requiredText(payload, "document_id")
			if !w.owned(documentID) || service.SoftDeleteCandidate(documentID) != nil {
				workbenchError(response, http.StatusBadRequest, "INVALID_REQUEST")
				return
			}
			w.createdMu.Lock()
			delete(w.created, documentID)
			w.createdMu.Unlock()
			workbenchJSON(response, http.StatusOK, map[string]any{"status": "CANDIDATE_DELETED"})
		})
	case "/api/candidates/approve":
		workbenchJSON(writer, http.StatusConflict, map[string]any{"status": "NOT_CONFIGURED", "promotion": "FAIL_CLOSED"})
	default:
		workbenchError(writer, http.StatusNotFound, "NOT_FOUND")
	}
}

func (w *Workbench) withService(
	writer http.ResponseWriter,
	request *http.Request,
	action func(http.ResponseWriter, *knowledge.Service),
) {
	if w.admit == nil {
		workbenchError(writer, http.StatusServiceUnavailable, maintenance.CodeInvalid)
		return
	}
	response := newDeferredHTTPResponse()
	err := w.admit(request.Context(), func(service *knowledge.Service) error {
		action(response, service)
		return nil
	})
	if err != nil {
		writeMaintenanceUnavailable(writer, err)
		return
	}
	_ = response.flush(writer)
}

func (w *Workbench) state(writer http.ResponseWriter, service *knowledge.Service) {
	project, err := service.Project(w.project)
	if err != nil {
		workbenchError(writer, http.StatusNotFound, "PROJECT_NOT_FOUND")
		return
	}
	active, _ := service.ActiveSnapshot(w.project)
	snapshot := map[string]any{"state": "NOT_CONFIGURED", "generation": nil}
	snapshotDocuments := []map[string]any{}
	versions := []knowledge.Version{}
	if active != nil {
		snapshot = map[string]any{"id": active.Snapshot.ID, "project_id": active.Snapshot.ProjectID, "generation": active.Snapshot.Generation, "state": active.Snapshot.State, "created_at": active.Snapshot.CreatedAt}
		for _, member := range active.Versions {
			items, readErr := service.DocumentVersions(member.DocumentID)
			if readErr != nil {
				workbenchError(writer, http.StatusInternalServerError, "STATE_READ_FAILED")
				return
			}
			selected := []knowledge.Version{}
			for _, item := range items {
				if item.ID == member.VersionID {
					selected = append(selected, item)
					versions = append(versions, item)
				}
			}
			snapshotDocuments = append(snapshotDocuments, map[string]any{"id": member.DocumentID, "versions": selected})
		}
	}
	w.createdMu.Lock()
	pending := make([]map[string]any, 0, len(w.created))
	identifiers := make([]string, 0, len(w.created))
	for identifier := range w.created {
		identifiers = append(identifiers, identifier)
	}
	sort.Strings(identifiers)
	for _, identifier := range identifiers {
		items, _ := service.DocumentVersions(identifier)
		if len(items) > 0 && items[len(items)-1].State == "candidate" {
			pending = append(pending, map[string]any{"id": identifier, "versions": []knowledge.Version{items[len(items)-1]}})
		}
	}
	w.createdMu.Unlock()
	workbenchJSON(writer, http.StatusOK, map[string]any{
		"project": project, "snapshot": snapshot, "snapshot_documents": snapshotDocuments, "versions": versions,
		"project_pending_candidates": map[string]any{"state": "PROCESS_LOCAL", "restart_recovery": "NOT_CONFIGURED", "items": pending},
		"writer_jobs":                map[string]any{"state": "DURABLE_FIFO", "items": []any{}},
		"intake_jobs":                map[string]any{"state": "NOT_CONFIGURED", "items": []any{}},
		"retrieval":                  map[string]any{"state": "LEXICAL", "results": []any{}, "citations": []any{}},
		"governance":                 map[string]any{"state": "CONFIGURED", "promotion": "FAIL_CLOSED"}, "csrf_token": w.csrf,
	})
}

func (w *Workbench) owned(documentID string) bool {
	w.createdMu.Lock()
	defer w.createdMu.Unlock()
	return w.created[documentID]
}

func (w *Workbench) asset(writer http.ResponseWriter, name, contentType string) {
	data, err := os.ReadFile(filepath.Join(w.assets, name))
	if err != nil {
		workbenchError(writer, http.StatusNotFound, "NOT_FOUND")
		return
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; style-src 'self'; script-src 'self'")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
}

func requiredText(payload map[string]any, key string) string {
	value, _ := payload[key].(string)
	return strings.TrimSpace(value)
}

func workbenchJSON(writer http.ResponseWriter, status int, value any) {
	data, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}

func workbenchError(writer http.ResponseWriter, status int, code string) {
	workbenchJSON(writer, status, map[string]any{"error": map[string]string{"code": code}})
}

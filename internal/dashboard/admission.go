package dashboard

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

const (
	maintenanceRetryAfterSeconds = 5
	maintenanceRetryDelay        = maintenanceRetryAfterSeconds * time.Second
)

var durablePostRoutes = map[string]struct{}{
	"/api/intake":                   {},
	"/api/candidate/approve":        {},
	"/api/libraries/preview":        {},
	"/api/libraries/create":         {},
	"/api/libraries/attach":         {},
	"/api/libraries/detach":         {},
	"/api/libraries/move":           {},
	"/api/libraries/insert-between": {},
	"/api/group-indexes/preview":    {},
	"/api/group-indexes/rebuild":    {},
}

var durablePeerActions = map[string]struct{}{
	"bootstrap": {},
	"secret":    {},
	"configure": {},
	"upsert":    {},
	"remove":    {},
}

func (s *Server) handleAPIWithAdmission(writer http.ResponseWriter, request *http.Request) int {
	if !durableMutationRequest(request.Method, request.URL.Path) {
		return s.handleAPI(writer, request)
	}
	permit, err := s.acquireSharedPermit(request.Context())
	if err != nil {
		return writeMaintenanceUnavailable(writer, err)
	}
	admittedContext, err := maintenance.WithShared(request.Context(), permit)
	if err != nil {
		_ = permit.Release()
		return writeMaintenanceUnavailable(writer, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = permit.Release()
		}
	}()
	invoked := false
	status := http.StatusServiceUnavailable
	response := newDeferredHTTPResponse()
	err = permit.Commit(func(maintenance.Fence) error {
		invoked = true
		status = s.handleAPI(response, request.WithContext(admittedContext))
		return nil
	})
	committed = true
	if err == nil {
		if flushErr := response.flush(writer); flushErr != nil && s.logger != nil {
			s.logger.Printf("dashboard response flush failed after committed mutation: route=%s error=%v", safeRoute(request.URL.Path), flushErr)
		}
		return status
	}
	if invoked && s.logger != nil {
		s.logger.Printf(
			"maintenance permit release uncertain after HTTP mutation: route=%s error=%v",
			safeRoute(request.URL.Path), err,
		)
	}
	return writeMaintenanceUnavailable(writer, err)
}

func (s *Server) acquireSharedPermit(ctx context.Context) (*maintenance.Permit, error) {
	return maintenance.AcquireShared(ctx, maintenance.Scope{
		ControlRoot: s.ControlRoot, KnowledgeRoot: s.KnowledgeRoot,
	})
}

func durableMutationRequest(method, path string) bool {
	switch method {
	case http.MethodGet:
		return path == "/api/libraries/tree" || path == "/api/project-library"
	case http.MethodPut, http.MethodDelete:
		return path == "/api/candidate"
	case http.MethodPost:
	default:
		return false
	}
	if _, found := durablePostRoutes[path]; found {
		return true
	}
	if strings.HasPrefix(path, "/api/intake/jobs/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/intake/jobs/"), "/")
		return len(parts) == 2 && intakeJobID.MatchString(parts[0]) &&
			(parts[1] == "retry" || parts[1] == "cancel")
	}
	if strings.HasPrefix(path, "/api/peers/") {
		action := strings.TrimPrefix(path, "/api/peers/")
		_, found := durablePeerActions[action]
		return found
	}
	return false
}

func writeMaintenanceUnavailable(writer http.ResponseWriter, err error) int {
	code := maintenanceErrorCode(err)
	if maintenanceAdmissionRetryable(err) {
		writer.Header().Set("Retry-After", "5")
	}
	writeError(writer, http.StatusServiceUnavailable, code, code)
	return http.StatusServiceUnavailable
}

func maintenanceErrorCode(err error) string {
	var value *maintenance.Error
	if errors.As(err, &value) && value.Code != "" {
		return value.Code
	}
	return maintenance.CodeLockFailed
}

func maintenanceAdmissionRetryable(err error) bool {
	code := maintenanceErrorCode(err)
	return code == maintenance.CodeActive || code == maintenance.CodeWriterDrainTimeout
}

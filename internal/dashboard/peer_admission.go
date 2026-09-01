package dashboard

import (
	"encoding/json"
	"net/http"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

type peerAdmissionHandler struct {
	server *Server
	next   http.Handler
}

func (handler *peerAdmissionHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler == nil || handler.server == nil || handler.next == nil {
		writePeerMaintenanceUnavailable(writer, &maintenance.Error{Code: maintenance.CodeInvalid})
		return
	}
	permit, err := handler.server.acquireSharedPermit(request.Context())
	if err != nil {
		writePeerMaintenanceUnavailable(writer, err)
		return
	}
	admittedContext, err := maintenance.WithShared(request.Context(), permit)
	if err != nil {
		_ = permit.Release()
		writePeerMaintenanceUnavailable(writer, err)
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = permit.Release()
		}
	}()
	invoked := false
	response := newDeferredHTTPResponse()
	err = permit.Commit(func(maintenance.Fence) error {
		invoked = true
		handler.next.ServeHTTP(response, request.WithContext(admittedContext))
		return nil
	})
	committed = true
	if err == nil {
		if flushErr := response.flush(writer); flushErr != nil && handler.server.logger != nil {
			handler.server.logger.Printf("peer response flush failed after committed mutation: route=%s error=%v", request.URL.Path, flushErr)
		}
		return
	}
	if invoked && handler.server.logger != nil {
		handler.server.logger.Printf("peer maintenance permit release uncertain: route=%s error=%v", request.URL.Path, err)
	}
	writePeerMaintenanceUnavailable(writer, err)
}

func writePeerMaintenanceUnavailable(writer http.ResponseWriter, err error) {
	code := maintenanceErrorCode(err)
	if maintenanceAdmissionRetryable(err) {
		writer.Header().Set("Retry-After", "5")
	}
	body, _ := json.Marshal(map[string]any{
		"ok":    false,
		"error": map[string]string{"code": code, "message": code},
	})
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write(body)
}

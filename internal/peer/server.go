package peer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

var errorCodePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)

type Handler struct {
	KnowledgeRoot string
	Peers         *Store
	Trees         *tree.Store
	Logger        *log.Logger
	Clock         func() time.Time
}

func (h *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	status := http.StatusInternalServerError
	defer func() {
		if recovered := recover(); recovered != nil {
			status = http.StatusInternalServerError
			writePeerError(writer, status, "PEER_INTERNAL_ERROR")
		}
		if h.Logger != nil {
			h.Logger.Printf("method=%s route=%s status=%d duration_ms=%d", request.Method, safePeerRoute(request.URL.Path), status, time.Since(started).Milliseconds())
		}
	}()
	status = h.serve(writer, request)
}

func (h *Handler) serve(writer http.ResponseWriter, request *http.Request) int {
	if h.Peers == nil || h.Trees == nil || h.KnowledgeRoot == "" {
		writePeerError(writer, http.StatusServiceUnavailable, "PEER_SERVICE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	if request.Method != http.MethodPost || request.URL.RawQuery != "" || !knownPeerRoute(request.URL.Path) {
		writePeerError(writer, http.StatusNotFound, "PEER_API_NOT_FOUND")
		return http.StatusNotFound
	}
	body, err := readPeerBody(request)
	if err != nil {
		writePeerError(writer, http.StatusUnauthorized, errorCode(err))
		return http.StatusUnauthorized
	}
	settings, err := h.Peers.Load(request.Context())
	if err != nil {
		writePeerError(writer, http.StatusServiceUnavailable, errorCode(err))
		return http.StatusServiceUnavailable
	}
	peerID, err := oneHeader(request.Header, PeerHeader)
	if err != nil {
		writePeerError(writer, http.StatusUnauthorized, "PEER_AUTH_REQUIRED")
		return http.StatusUnauthorized
	}
	record, found := settings.Peer(peerID)
	if !found || !record.Enabled {
		writePeerError(writer, http.StatusUnauthorized, "PEER_NOT_AUTHORIZED")
		return http.StatusUnauthorized
	}
	now := time.Now()
	if h.Clock != nil {
		now = h.Clock()
	}
	auth, err := VerifyHeaders(request.Header, record.Secret, http.MethodPost, request.URL.Path, body, now)
	if err != nil || auth.PeerID != record.PeerID {
		writePeerError(writer, http.StatusUnauthorized, errorCode(err))
		return http.StatusUnauthorized
	}
	accepted, err := h.Peers.AcceptReplay(request.Context(), auth.PeerID, auth.Nonce, auth.Timestamp)
	if err != nil {
		writePeerError(writer, http.StatusServiceUnavailable, errorCode(err))
		return http.StatusServiceUnavailable
	}
	if !accepted {
		writePeerError(writer, http.StatusUnauthorized, "PEER_REPLAY_REJECTED")
		return http.StatusUnauthorized
	}
	value, err := h.Trees.Load(request.Context())
	if err != nil {
		writePeerError(writer, http.StatusServiceUnavailable, "PEER_TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	result, err := h.route(request.Context(), request.URL.Path, body, settings, record, value)
	if err != nil {
		code := errorCode(err)
		status := http.StatusBadRequest
		if code == "PEER_PROJECT_FORBIDDEN" {
			status = http.StatusUnauthorized
		}
		writePeerError(writer, status, code)
		return status
	}
	return writePeerSuccess(writer, request.URL.Path, auth.Nonce, settings.LocalPeerID, record.Secret, result)
}

func (h *Handler) route(_ context.Context, path string, body []byte, settings Settings, record Record, value *tree.Tree) (map[string]any, error) {
	switch path {
	case "/v1/health":
		var payload struct {
			ProjectID string `json:"project_id"`
		}
		if err := decodeExact(body, &payload, "project_id"); err != nil {
			return nil, errors.New("INVALID_PEER_REQUEST_FIELDS")
		}
		if payload.ProjectID != record.ProjectID {
			return nil, errors.New("PEER_PROJECT_FORBIDDEN")
		}
		exports, libraryCount, err := ExportCatalog(h.KnowledgeRoot, payload.ProjectID, record.ExportNodeIDs, value)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "status": "READY", "peer_id": settings.LocalPeerID,
			"project_id": payload.ProjectID, "export_nodes": exports,
			"library_count": libraryCount,
			"capabilities":  []string{"query-v1", "material-v1", "response-hmac-v1"},
		}, nil
	case "/v1/query":
		var payload struct {
			ProjectID string `json:"project_id"`
			NodeID    string `json:"node_id"`
			Query     string `json:"query"`
			Limit     int    `json:"limit"`
		}
		if err := decodeExact(body, &payload, "project_id", "node_id", "query", "limit"); err != nil {
			return nil, errors.New("INVALID_PEER_REQUEST_FIELDS")
		}
		if payload.ProjectID != record.ProjectID {
			return nil, errors.New("PEER_PROJECT_FORBIDDEN")
		}
		if !containsString(record.ExportNodeIDs, payload.NodeID) {
			return nil, errors.New("PEER_EXPORT_NODE_FORBIDDEN")
		}
		response, err := QueryLibrarySubtree(h.KnowledgeRoot, payload.ProjectID, payload.NodeID, payload.Query, payload.Limit, value)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "status": response.Status, "peer_id": settings.LocalPeerID,
			"project_id": response.ProjectID, "node_id": response.NodeID,
			"generation": response.Generation, "results": response.Results,
		}, nil
	case "/v1/material":
		var payload struct {
			ProjectID   string `json:"project_id"`
			NodeID      string `json:"node_id"`
			LibraryNode string `json:"library_node"`
			MaterialID  string `json:"material_id"`
		}
		if err := decodeExact(body, &payload, "project_id", "node_id", "library_node", "material_id"); err != nil {
			return nil, errors.New("INVALID_PEER_REQUEST_FIELDS")
		}
		if payload.ProjectID != record.ProjectID {
			return nil, errors.New("PEER_PROJECT_FORBIDDEN")
		}
		if !containsString(record.ExportNodeIDs, payload.NodeID) {
			return nil, errors.New("PEER_EXPORT_NODE_FORBIDDEN")
		}
		material, err := FetchSubtreeMaterial(h.KnowledgeRoot, payload.ProjectID, payload.NodeID, payload.LibraryNode, payload.MaterialID, value)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"ok": true, "status": "MATERIAL_READY", "peer_id": settings.LocalPeerID,
			"project_id": payload.ProjectID, "node_id": payload.NodeID,
			"library_node": payload.LibraryNode, "material": material,
		}, nil
	default:
		return nil, errors.New("PEER_API_NOT_FOUND")
	}
}

func readPeerBody(request *http.Request) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" || request.ContentLength < 1 || request.ContentLength > MaxBodyBytes || len(request.TransferEncoding) != 0 {
		return nil, errors.New("PEER_REQUEST_INVALID")
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, MaxBodyBytes+1))
	if err != nil || int64(len(body)) != request.ContentLength || len(body) > MaxBodyBytes {
		return nil, errors.New("PEER_REQUEST_INVALID")
	}
	return body, nil
}

func writePeerSuccess(writer http.ResponseWriter, path, nonce, peerID, secret string, value map[string]any) int {
	body, err := json.Marshal(value)
	if err != nil || len(body) > MaxResponseBytes {
		writePeerError(writer, http.StatusInternalServerError, "PEER_RESPONSE_TOO_LARGE")
		return http.StatusInternalServerError
	}
	headers, err := SignedResponseHeaders(peerID, secret, http.StatusOK, path, nonce, body)
	if err != nil {
		writePeerError(writer, http.StatusServiceUnavailable, "PEER_RESPONSE_AUTH_INVALID")
		return http.StatusServiceUnavailable
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	for name, values := range headers {
		for _, value := range values {
			writer.Header().Add(name, value)
		}
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
	return http.StatusOK
}

func writePeerError(writer http.ResponseWriter, status int, code string) {
	if writer.Header().Get("Content-Type") != "" {
		return
	}
	body, _ := json.Marshal(map[string]any{"ok": false, "error": code})
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
}

func errorCode(err error) string {
	if err != nil && errorCodePattern.MatchString(err.Error()) {
		return err.Error()
	}
	return "PEER_INTERNAL_ERROR"
}

func knownPeerRoute(path string) bool {
	return path == "/v1/health" || path == "/v1/query" || path == "/v1/material"
}

func safePeerRoute(path string) string {
	if knownPeerRoute(path) {
		return path
	}
	return "/v1/{unknown}"
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

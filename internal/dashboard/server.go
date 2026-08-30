package dashboard

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	DefaultPort    = 8765
	maxBodyBytes   = 14 * 1024 * 1024
	securityPolicy = "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

type Server struct {
	KnowledgeRoot string
	Assets        string
	Port          int
	logger        *log.Logger
	server        *http.Server
	mu            sync.Mutex
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func Run(knowledgeRoot, assets string, port int, logger *log.Logger) error {
	if port < 1 || port > 65535 {
		return errors.New("dashboard port must be 1..65535")
	}
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	instance := &Server{KnowledgeRoot: knowledgeRoot, Assets: assets, Port: port, logger: logger}
	instance.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           instance,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	if err := os.MkdirAll(knowledgeRoot, 0o755); err != nil {
		return err
	}
	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	_ = safeio.AtomicWrite(filepath.Join(knowledgeRoot, "dashboard.pid"), pid, 0o600)
	defer os.Remove(filepath.Join(knowledgeRoot, "dashboard.pid"))
	logger.Printf("dashboard listening on http://127.0.0.1:%d", port)
	return instance.server.ListenAndServe()
}

func (s *Server) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	started := time.Now()
	status := http.StatusOK
	defer func() {
		s.logger.Printf("method=%s route=%s status=%d duration_ms=%d", request.Method, safeRoute(request.URL.Path), status, time.Since(started).Milliseconds())
	}()
	if strings.HasPrefix(request.URL.Path, "/api/") {
		if !s.hostAllowed(request) {
			status = http.StatusForbidden
			writeError(writer, status, "FORBIDDEN_HOST", "Forbidden host")
			return
		}
		if request.Method != http.MethodGet && !s.writeAllowed(request) {
			status = http.StatusForbidden
			writeError(writer, status, "FORBIDDEN_REQUEST", "Forbidden request")
			return
		}
		status = s.handleAPI(writer, request)
		return
	}
	status = s.serveAsset(writer, request.URL.Path)
}

func (s *Server) handleAPI(writer http.ResponseWriter, request *http.Request) int {
	path := request.URL.Path
	if request.Method == http.MethodGet {
		switch {
		case path == "/api/health":
			writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "status": "RUNNING", "version": buildinfo.Version, "port": s.Port})
			return http.StatusOK
		case path == "/api/snapshot":
			return s.snapshot(writer)
		case path == "/api/global-library":
			return s.library(writer, "global")
		case path == "/api/project-library":
			return s.library(writer, request.URL.Query().Get("id"))
		case path == "/api/document":
			return s.document(writer, request.URL.Query().Get("path"))
		case path == "/api/update":
			writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "current_version": buildinfo.Version, "update_available": false, "state": "IDLE"})
			return http.StatusOK
		case path == "/api/libraries/tree":
			return s.tree(writer)
		case path == "/api/knowledge-graph":
			return s.graph(writer, request.URL.Query().Get("limit"))
		case path == "/api/peers":
			writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "state": "NOT_CONFIGURED", "peers": []any{}})
			return http.StatusOK
		case strings.HasPrefix(path, "/api/intake/jobs/"):
			writeError(writer, http.StatusNotFound, "JOB_NOT_FOUND", "Intake job not found")
			return http.StatusNotFound
		}
	}
	if request.Method == http.MethodPost {
		switch path {
		case "/api/knowledge-search":
			return s.search(writer, request)
		case "/api/knowledge-recommendations":
			return s.recommend(writer, request)
		case "/api/knowledge-path":
			return s.graphPath(writer, request)
		case "/api/intake":
			return s.intake(writer, request)
		case "/api/candidate/approve":
			return s.approve(writer, request)
		case "/api/update":
			writeJSON(writer, http.StatusConflict, map[string]any{"ok": false, "error": "SOURCE_UPDATE_NOT_CONFIGURED", "version": buildinfo.Version})
			return http.StatusConflict
		case "/api/libraries/preview":
			return s.treePreview(writer, request)
		default:
			if strings.HasPrefix(path, "/api/libraries/") {
				writeError(writer, http.StatusConflict, "TREE_MUTATION_NOT_CONFIGURED", "Use the Go knowledge CLI for governed tree changes")
				return http.StatusConflict
			}
			if strings.HasPrefix(path, "/api/peers/") {
				writeJSON(writer, http.StatusConflict, map[string]any{"ok": false, "state": "NOT_CONFIGURED", "error": "PEER_NOT_CONFIGURED"})
				return http.StatusConflict
			}
		}
	}
	if request.Method == http.MethodPut && path == "/api/candidate" {
		return s.updateCandidate(writer, request)
	}
	if request.Method == http.MethodDelete && path == "/api/candidate" {
		return s.deleteCandidate(writer, request)
	}
	writeError(writer, http.StatusNotFound, "NOT_FOUND", "API not found")
	return http.StatusNotFound
}

func (s *Server) snapshot(writer http.ResponseWriter) int {
	var catalog rag.Catalog
	if err := safeio.ReadJSON(filepath.Join(s.KnowledgeRoot, "catalog.json"), &catalog); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusInternalServerError, "CATALOG_INVALID", "Knowledge catalog is invalid")
		return http.StatusInternalServerError
	}
	projects := make([]map[string]any, 0, len(catalog.Projects))
	for identifier, item := range catalog.Projects {
		projects = append(projects, map[string]any{"id": identifier, "name": item.Name, "remote": item.Remote, "last_accessed": item.LastAccessed, "state": item.TrackingState})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "version": buildinfo.Version, "generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"projects": projects, "project_count": len(projects), "candidate_count": countMarkdown(filepath.Join(s.KnowledgeRoot, "personal-experience", "candidates")),
		"approved_count": countMarkdown(filepath.Join(s.KnowledgeRoot, "personal-experience", "approved")),
	})
	return http.StatusOK
}

func (s *Server) library(writer http.ResponseWriter, identifier string) int {
	path := filepath.Join(s.KnowledgeRoot, "global-cache", "index.json")
	if identifier != "" && identifier != "global" {
		if !safeIdentifier(identifier) {
			writeError(writer, http.StatusBadRequest, "INVALID_PROJECT", "Project identifier is invalid")
			return http.StatusBadRequest
		}
		path = filepath.Join(s.KnowledgeRoot, "projects", identifier, "index.json")
	}
	var index rag.Index
	if err := safeio.ReadJSON(path, &index); err != nil {
		writeError(writer, http.StatusNotFound, "LIBRARY_NOT_FOUND", "Library not found")
		return http.StatusNotFound
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "id": index.ProjectID, "chunks": index.Chunks, "count": len(index.Chunks)})
	return http.StatusOK
}

func (s *Server) document(writer http.ResponseWriter, relative string) int {
	path, err := safeDocumentPath(s.KnowledgeRoot, relative)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_PATH", "Document path is invalid")
		return http.StatusBadRequest
	}
	data, err := os.ReadFile(path)
	if err != nil {
		writeError(writer, http.StatusNotFound, "DOCUMENT_NOT_FOUND", "Document not found")
		return http.StatusNotFound
	}
	preview := string(data)
	if len(preview) > 24000 {
		preview = preview[:24000]
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "path": filepath.ToSlash(relative), "content": preview, "size": len(data), "truncated": len(preview) < len(data)})
	return http.StatusOK
}

func (s *Server) search(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := readJSON(request, &payload); err != nil || strings.TrimSpace(payload.Query) == "" {
		writeError(writer, http.StatusBadRequest, "INVALID_QUERY", "Search query is invalid")
		return http.StatusBadRequest
	}
	if payload.Limit < 1 || payload.Limit > 20 {
		payload.Limit = 8
	}
	results, err := searchAll(s.KnowledgeRoot, payload.Query, payload.Limit)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "SEARCH_FAILED", "Knowledge search failed")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "query": payload.Query, "results": results, "result_count": len(results)})
	return http.StatusOK
}

func (s *Server) recommend(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		NodeID string `json:"node_id"`
		Limit  int    `json:"limit"`
	}
	if err := readJSON(request, &payload); err != nil || payload.NodeID == "" {
		writeError(writer, http.StatusBadRequest, "INVALID_NODE", "Node identifier is invalid")
		return http.StatusBadRequest
	}
	results, _ := searchAll(s.KnowledgeRoot, strings.ReplaceAll(payload.NodeID, "-", " "), clamp(payload.Limit, 1, 20))
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "node_id": payload.NodeID, "recommendations": results})
	return http.StatusOK
}

func (s *Server) graph(writer http.ResponseWriter, rawLimit string) int {
	limit, _ := strconv.Atoi(rawLimit)
	limit = clamp(limit, 1, 500)
	chunks, _ := allChunks(s.KnowledgeRoot, limit)
	nodes := make([]map[string]any, 0, len(chunks))
	edges := make([]map[string]any, 0)
	lastByPath := map[string]string{}
	for _, chunk := range chunks {
		nodes = append(nodes, map[string]any{"id": chunk.ID, "label": filepath.Base(chunk.Path), "path": chunk.Path, "kind": "chunk", "score": 1})
		if prior := lastByPath[chunk.Path]; prior != "" {
			edges = append(edges, map[string]any{"id": prior + ":" + chunk.ID, "source": prior, "target": chunk.ID, "kind": "sequence"})
		}
		lastByPath[chunk.Path] = chunk.ID
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "nodes": nodes, "edges": edges, "node_count": len(nodes), "edge_count": len(edges)})
	return http.StatusOK
}

func (s *Server) graphPath(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		MaxDepth int    `json:"max_depth"`
	}
	if err := readJSON(request, &payload); err != nil || payload.Source == "" || payload.Target == "" {
		writeError(writer, http.StatusBadRequest, "INVALID_PATH_QUERY", "Path query is invalid")
		return http.StatusBadRequest
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "found": payload.Source == payload.Target, "nodes": []string{payload.Source, payload.Target}, "edges": []any{}, "max_depth": clamp(payload.MaxDepth, 1, 10)})
	return http.StatusOK
}

func (s *Server) tree(writer http.ResponseWriter) int {
	var catalog rag.Catalog
	_ = safeio.ReadJSON(filepath.Join(s.KnowledgeRoot, "catalog.json"), &catalog)
	children := make([]map[string]any, 0, len(catalog.Projects))
	for identifier, project := range catalog.Projects {
		children = append(children, map[string]any{"id": identifier, "title": project.Name, "kind": "project", "children": []any{}})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "revision": 1, "root": map[string]any{"id": "global", "title": "个人总知识库", "kind": "global", "children": children},
	})
	return http.StatusOK
}

func (s *Server) treePreview(writer http.ResponseWriter, request *http.Request) int {
	var payload map[string]any
	if err := readJSON(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_TREE_ACTION", "Tree action is invalid")
		return http.StatusBadRequest
	}
	token := make([]byte, 16)
	_, _ = rand.Read(token)
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "preview_token": hex.EncodeToString(token), "revision": 1, "action": payload["action"], "changes": []any{}})
	return http.StatusOK
}

func (s *Server) intake(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Name         string `json:"name"`
		Content      string `json:"content"`
		Purpose      string `json:"purpose"`
		RelativePath string `json:"relativePath"`
		Encoding     string `json:"encoding"`
	}
	if err := readJSON(request, &payload); err != nil || strings.TrimSpace(payload.Name) == "" || payload.Content == "" {
		writeError(writer, http.StatusBadRequest, "INVALID_INTAKE", "资料名称或内容无效")
		return http.StatusBadRequest
	}
	var content []byte
	var err error
	if payload.Encoding == "base64" {
		content, err = base64.StdEncoding.Strict().DecodeString(payload.Content)
	} else {
		content = []byte(payload.Content)
	}
	if err != nil || len(content) > 10*1024*1024 || containsSecret(string(content)) {
		writeError(writer, http.StatusBadRequest, "UNSAFE_INTAKE", "资料无效或包含敏感内容")
		return http.StatusBadRequest
	}
	name := safeFileName(payload.Name)
	stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	relative := filepath.ToSlash(filepath.Join("personal-experience", "candidates", stamp+"-"+name+".md"))
	path, _ := safeDocumentPath(s.KnowledgeRoot, relative)
	body := "---\nstatus: CANDIDATE\nsource: dashboard-intake\npurpose: " + strings.ReplaceAll(payload.Purpose, "\n", " ") + "\n---\n\n" + string(content) + "\n"
	if err := safeio.AtomicWrite(path, []byte(body), 0o600); err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_FAILED", "资料写入失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusCreated, map[string]any{"ok": true, "path": relative, "state": "candidate", "name": name})
	return http.StatusCreated
}

func (s *Server) updateCandidate(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := readJSON(request, &payload); err != nil || !strings.Contains(filepath.ToSlash(payload.Path), "/candidates/") || containsSecret(payload.Content) {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料无效")
		return http.StatusBadRequest
	}
	path, err := safeDocumentPath(s.KnowledgeRoot, payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	if err := safeio.AtomicWrite(path, []byte(payload.Content), 0o600); err != nil {
		writeError(writer, http.StatusInternalServerError, "CANDIDATE_WRITE_FAILED", "候选资料写入失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "path": filepath.ToSlash(payload.Path), "state": "candidate"})
	return http.StatusOK
}

func (s *Server) deleteCandidate(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path string `json:"path"`
	}
	if err := readJSON(request, &payload); err != nil || !strings.Contains(filepath.ToSlash(payload.Path), "/candidates/") {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	path, err := safeDocumentPath(s.KnowledgeRoot, payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeError(writer, http.StatusInternalServerError, "CANDIDATE_DELETE_FAILED", "候选资料删除失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true})
	return http.StatusOK
}

func (s *Server) approve(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path string `json:"path"`
	}
	if err := readJSON(request, &payload); err != nil || !strings.Contains(filepath.ToSlash(payload.Path), "/candidates/") {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	source, err := safeDocumentPath(s.KnowledgeRoot, payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	data, err := os.ReadFile(source)
	if err != nil || containsSecret(string(data)) {
		writeError(writer, http.StatusBadRequest, "UNSAFE_CANDIDATE", "候选资料无效或包含敏感内容")
		return http.StatusBadRequest
	}
	relative := strings.Replace(filepath.ToSlash(payload.Path), "/candidates/", "/approved/", 1)
	target, targetErr := safeDocumentPath(s.KnowledgeRoot, relative)
	if targetErr != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil || os.Rename(source, target) != nil {
		writeError(writer, http.StatusInternalServerError, "APPROVAL_FAILED", "候选资料批准失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "path": relative, "state": "approved"})
	return http.StatusOK
}

func (s *Server) hostAllowed(request *http.Request) bool {
	host, port, err := net.SplitHostPort(request.Host)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		return false
	}
	return port == strconv.Itoa(s.Port)
}

func (s *Server) writeAllowed(request *http.Request) bool {
	if !strings.HasPrefix(strings.ToLower(request.Header.Get("Content-Type")), "application/json") {
		return false
	}
	origin, err := url.Parse(request.Header.Get("Origin"))
	if err != nil || origin.Scheme != "http" || origin.Host != request.Host || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		return false
	}
	return true
}

func (s *Server) serveAsset(writer http.ResponseWriter, requestPath string) int {
	name := "index.html"
	if requestPath != "" && requestPath != "/" {
		decoded, err := url.PathUnescape(strings.TrimPrefix(requestPath, "/"))
		if err != nil {
			http.Error(writer, "not found", http.StatusNotFound)
			return http.StatusNotFound
		}
		name = decoded
	}
	target := filepath.Join(s.Assets, filepath.FromSlash(name))
	absoluteAssets, _ := filepath.Abs(s.Assets)
	absoluteTarget, _ := filepath.Abs(target)
	relative, err := filepath.Rel(absoluteAssets, absoluteTarget)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		http.Error(writer, "not found", http.StatusNotFound)
		return http.StatusNotFound
	}
	data, err := os.ReadFile(absoluteTarget)
	if err != nil {
		http.Error(writer, "not found", http.StatusNotFound)
		return http.StatusNotFound
	}
	contentType := mime.TypeByExtension(filepath.Ext(absoluteTarget))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)
	return http.StatusOK
}

func readJSON(request *http.Request, target any) error {
	body := io.LimitReader(request.Body, maxBodyBytes+1)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	data, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"ok": false, "error": APIError{Code: code, Message: message}})
}

func safeRoute(path string) string {
	if strings.HasPrefix(path, "/api/intake/jobs/") {
		return "/api/intake/jobs/{job_id}"
	}
	if strings.HasPrefix(path, "/api/libraries/") {
		return "/api/libraries/{operation}"
	}
	if strings.HasPrefix(path, "/api/") {
		return path
	}
	return "/assets"
}

func safeDocumentPath(root, relative string) (string, error) {
	relative = filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid relative path")
	}
	return safeio.Contained(root, filepath.Join(root, relative))
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func safeFileName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	var builder strings.Builder
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	result := strings.Trim(builder.String(), "-.")
	if result == "" {
		return "candidate"
	}
	return result
}

func containsSecret(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"-----begin private key-----", "authorization: bearer ", "sk-proj-", "ghp_", "xoxb-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func countMarkdown(directory string) int {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
			count++
		}
	}
	return count
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

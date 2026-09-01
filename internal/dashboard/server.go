package dashboard

import (
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
	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

const (
	DefaultPort    = 8765
	maxBodyBytes   = 18 * 1024 * 1024
	securityPolicy = "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

type Server struct {
	KnowledgeRoot string
	Assets        string
	Port          int
	logger        *log.Logger
	server        *http.Server
	mu            sync.Mutex
	updateMu      sync.Mutex
	peerRuntimeMu sync.RWMutex
	intakeMu      sync.Mutex
	candidateMu   sync.Mutex
	globalIndexMu sync.Mutex
	treeCommitMu  sync.Mutex
	groupIndexMu  sync.RWMutex
	graphMu       sync.Mutex
	treeStore     *tree.Store
	peerStore     *peer.Store
	intakeStore   *document.JobStore
	peerServer    *http.Server
	peerListener  net.Listener
	peerRuntime   PeerRuntimeStatus
	treePreviews  map[string]issuedTreePreview
	updateToken   string
	updates       updateBackend
	intakeRunning map[string]struct{}
	intakeSlots   chan struct{}
	intakeStop    chan struct{}
	intakeClosing bool
	intakeWG      sync.WaitGroup
	graphCache    graphCacheEntry
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
	instance := &Server{KnowledgeRoot: knowledgeRoot, Assets: assets, Port: port, logger: logger, treePreviews: map[string]issuedTreePreview{}}
	instance.server = &http.Server{
		Addr:              fmt.Sprintf("127.0.0.1:%d", port),
		Handler:           instance,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
	if err := os.MkdirAll(knowledgeRoot, 0o755); err != nil {
		return err
	}
	if err := instance.ensureStores(); err != nil {
		return err
	}
	defer instance.closeStores()
	instance.startPeerRuntime()
	defer instance.stopPeerRuntime()
	instance.resumeIntakeJobs()
	pid := []byte(strconv.Itoa(os.Getpid()) + "\n")
	_ = safeio.AtomicWrite(filepath.Join(knowledgeRoot, "dashboard.pid"), pid, 0o600)
	defer os.Remove(filepath.Join(knowledgeRoot, "dashboard.pid"))
	logger.Printf("dashboard listening on http://127.0.0.1:%d", port)
	err := instance.server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
			return s.updateStatus(writer, request)
		case path == "/api/libraries/tree":
			return s.treeSnapshot(writer)
		case path == "/api/knowledge-graph":
			return s.graph(writer, request.URL.Query().Get("limit"))
		case path == "/api/knowledge-graph-revision":
			return s.graphRevision(writer)
		case path == "/api/peers":
			return s.peerSnapshot(writer, request)
		case strings.HasPrefix(path, "/api/intake/jobs/"):
			return s.intakeJobStatus(writer, request, strings.TrimPrefix(path, "/api/intake/jobs/"))
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
			return s.startUpdate(writer, request)
		case "/api/libraries/preview":
			return s.treeActionPreview(writer, request)
		default:
			if strings.HasPrefix(path, "/api/intake/jobs/") {
				return s.intakeJobAction(writer, request, strings.TrimPrefix(path, "/api/intake/jobs/"))
			}
			if strings.HasPrefix(path, "/api/libraries/") {
				action := strings.ReplaceAll(strings.TrimPrefix(path, "/api/libraries/"), "-", "_")
				if validTreeAction(action) && action != "preview" {
					return s.treeActionCommit(writer, request, action)
				}
			}
			if strings.HasPrefix(path, "/api/peers/") {
				return s.peerAction(writer, request, strings.TrimPrefix(path, "/api/peers/"))
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
	return s.writeSnapshot(writer)
}

func (s *Server) library(writer http.ResponseWriter, identifier string) int {
	return s.writeLibrary(writer, identifier)
}

func (s *Server) document(writer http.ResponseWriter, relative string) int {
	return s.readDocument(writer, relative)
}

func (s *Server) search(writer http.ResponseWriter, request *http.Request) int {
	return s.semanticSearchHTTP(writer, request)
}

func (s *Server) recommend(writer http.ResponseWriter, request *http.Request) int {
	return s.recommendationsHTTP(writer, request)
}

func (s *Server) graph(writer http.ResponseWriter, rawLimit string) int {
	return s.graphHTTP(writer, rawLimit)
}

func (s *Server) graphRevision(writer http.ResponseWriter) int {
	return s.graphRevisionHTTP(writer)
}

func (s *Server) graphPath(writer http.ResponseWriter, request *http.Request) int {
	return s.graphPathHTTP(writer, request)
}

func (s *Server) intake(writer http.ResponseWriter, request *http.Request) int {
	return s.enqueueIntake(writer, request)
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
	return securitycheck.ContainsHighConfidenceSecret(value)
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

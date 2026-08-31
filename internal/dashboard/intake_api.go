package dashboard

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const intakeWorkerLimit = 2

var intakeJobID = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)

type intakeRequest struct {
	Name         string `json:"name"`
	Content      string `json:"content"`
	Purpose      string `json:"purpose"`
	RelativePath string `json:"relativePath"`
	Encoding     string `json:"encoding"`
}

func (s *Server) enqueueIntake(writer http.ResponseWriter, request *http.Request) int {
	var payload intakeRequest
	if err := readJSON(request, &payload); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_INTAKE", "资料请求格式无效")
		return http.StatusBadRequest
	}
	payload.Name = safeFileName(payload.Name)
	if payload.Name == "candidate" || len(payload.Name) > 240 || len(payload.Purpose) > 2000 || len(payload.RelativePath) > 4096 || strings.ContainsRune(payload.Purpose, 0) || strings.ContainsRune(payload.RelativePath, 0) {
		writeError(writer, http.StatusBadRequest, "INVALID_INTAKE", "资料名称或元数据无效")
		return http.StatusBadRequest
	}
	var content []byte
	var err error
	switch payload.Encoding {
	case "base64":
		content, err = base64.StdEncoding.Strict().DecodeString(payload.Content)
	case "", "utf8":
		content = []byte(payload.Content)
	default:
		err = errors.New("unsupported intake encoding")
	}
	if err != nil || len(content) == 0 || len(content) > document.MaxSourceBytes {
		writeError(writer, http.StatusBadRequest, "INVALID_INTAKE", "资料内容或编码无效")
		return http.StatusBadRequest
	}
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_UNAVAILABLE", "入库任务服务不可用")
		return http.StatusInternalServerError
	}
	digest := sha256.Sum256(content)
	sourceSHA := hex.EncodeToString(digest[:])
	nameDigest := sha256.Sum256([]byte(payload.Name))
	extension := strings.ToLower(filepath.Ext(payload.Name))
	if len(extension) > 12 {
		extension = ""
	}
	stagingRelative := filepath.ToSlash(filepath.Join("service", "intake", "uploads", sourceSHA+"-"+hex.EncodeToString(nameDigest[:6])+extension))
	stagingPath, err := safeDocumentPath(s.KnowledgeRoot, stagingRelative)
	if err != nil || safeio.AtomicWrite(stagingPath, content, 0o600) != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_STAGE_FAILED", "资料暂存失败")
		return http.StatusInternalServerError
	}
	job, err := s.intakeStore.Enqueue(request.Context(), map[string]any{
		"name": payload.Name, "purpose": payload.Purpose, "relative_path": payload.RelativePath,
		"staging_ref": stagingRelative, "source_sha256": sourceSHA,
	}, map[string]any{"extractor": "go-document-v1", "auto_approve": false})
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_ENQUEUE_FAILED", "入库任务创建失败")
		return http.StatusInternalServerError
	}
	if job.State == "QUEUED" {
		s.launchIntakeJob(job.ID)
	}
	writeJSON(writer, http.StatusAccepted, map[string]any{"ok": true, "job": publicIntakeJob(job)})
	return http.StatusAccepted
}

func (s *Server) intakeJobStatus(writer http.ResponseWriter, request *http.Request, identifier string) int {
	if !intakeJobID.MatchString(identifier) {
		writeError(writer, http.StatusNotFound, "JOB_NOT_FOUND", "Intake job not found")
		return http.StatusNotFound
	}
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_UNAVAILABLE", "入库任务服务不可用")
		return http.StatusInternalServerError
	}
	job, err := s.intakeStore.Get(request.Context(), identifier)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "JOB_NOT_FOUND", "Intake job not found")
		return http.StatusNotFound
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_STATUS_FAILED", "入库任务状态读取失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "job": publicIntakeJob(job)})
	return http.StatusOK
}

func (s *Server) intakeJobAction(writer http.ResponseWriter, request *http.Request, suffix string) int {
	parts := strings.Split(suffix, "/")
	if len(parts) != 2 || !intakeJobID.MatchString(parts[0]) || parts[1] != "retry" && parts[1] != "cancel" {
		writeError(writer, http.StatusNotFound, "JOB_ACTION_NOT_FOUND", "Intake job action not found")
		return http.StatusNotFound
	}
	var empty struct{}
	if err := readJSON(request, &empty); err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_JOB_ACTION", "任务操作请求无效")
		return http.StatusBadRequest
	}
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_UNAVAILABLE", "入库任务服务不可用")
		return http.StatusInternalServerError
	}
	if _, err := s.intakeStore.Get(request.Context(), parts[0]); errors.Is(err, sql.ErrNoRows) {
		writeError(writer, http.StatusNotFound, "JOB_NOT_FOUND", "Intake job not found")
		return http.StatusNotFound
	} else if err != nil {
		writeError(writer, http.StatusInternalServerError, "INTAKE_STATUS_FAILED", "入库任务状态读取失败")
		return http.StatusInternalServerError
	}
	var (
		job document.Job
		err error
	)
	if parts[1] == "retry" {
		job, err = s.intakeStore.Retry(request.Context(), parts[0])
	} else {
		job, err = s.intakeStore.Cancel(request.Context(), parts[0])
	}
	if errors.Is(err, document.ErrInvalidJobState) {
		writeError(writer, http.StatusConflict, "INVALID_JOB_STATE", "当前任务状态不允许该操作")
		return http.StatusConflict
	}
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "JOB_ACTION_FAILED", "任务操作失败")
		return http.StatusInternalServerError
	}
	if parts[1] == "retry" {
		s.launchIntakeJob(job.ID)
		writeJSON(writer, http.StatusAccepted, map[string]any{"ok": true, "job": publicIntakeJob(job)})
		return http.StatusAccepted
	}
	s.removeIntakeSource(job)
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "job": publicIntakeJob(job)})
	return http.StatusOK
}

func publicIntakeJob(job document.Job) map[string]any {
	stage := map[string]string{
		"inspect": "validate", "parse": "security-scan", "extract": "native-extract",
		"ocr": "ocr-primary", "chunk": "chunk", "assess": "candidate-write", "persist": "complete",
	}[job.Stage]
	result := job.Result
	response := map[string]any{
		"id": job.ID, "state": job.State, "stage": stage, "progress": job.Progress,
		"page_count": job.PageCount, "revision": job.Revision,
	}
	if result != nil {
		response["result"] = result
	}
	if job.State == "FAILED" {
		retryable := intakeFailureRetryable(job.ErrorCategory) && job.Attempt < job.MaxAttempts
		response["result"] = map[string]any{"status": "FAILED", "retryable": retryable}
		response["error"] = map[string]any{"category": job.ErrorCategory, "ref": job.ErrorRef, "retryable": retryable}
	}
	return response
}

func intakeFailureRetryable(category string) bool {
	switch category {
	case "SECURITY", "UNSAFE_SOURCE", "UNSUPPORTED_FORMAT", "INVALID_DOCUMENT":
		return false
	default:
		return true
	}
}

func (s *Server) resumeIntakeJobs() {
	if err := s.ensureStores(); err != nil {
		s.logger.Printf("intake recovery unavailable")
		return
	}
	jobs, err := s.intakeStore.List(context.Background(), 1000)
	if err != nil {
		s.logger.Printf("intake recovery failed")
		return
	}
	for _, job := range jobs {
		if job.State == "QUEUED" {
			s.launchIntakeJob(job.ID)
		}
	}
}

func (s *Server) launchIntakeJob(identifier string) {
	s.intakeMu.Lock()
	if s.intakeClosing {
		s.intakeMu.Unlock()
		return
	}
	if s.intakeRunning == nil {
		s.intakeRunning = map[string]struct{}{}
		s.intakeSlots = make(chan struct{}, intakeWorkerLimit)
		s.intakeStop = make(chan struct{})
	}
	if _, exists := s.intakeRunning[identifier]; exists {
		s.intakeMu.Unlock()
		return
	}
	s.intakeRunning[identifier] = struct{}{}
	stop, slots := s.intakeStop, s.intakeSlots
	s.intakeWG.Add(1)
	s.intakeMu.Unlock()
	go func() {
		defer func() {
			s.intakeMu.Lock()
			delete(s.intakeRunning, identifier)
			closing := s.intakeClosing
			s.intakeMu.Unlock()
			if !closing {
				job, err := s.intakeStore.Get(context.Background(), identifier)
				if err == nil && job.State == "QUEUED" {
					s.launchIntakeJob(identifier)
				}
			}
			s.intakeWG.Done()
		}()
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		case <-stop:
			return
		}
		s.intakeMu.Lock()
		closing := s.intakeClosing
		s.intakeMu.Unlock()
		if !closing {
			s.processIntakeJob(identifier)
		}
	}()
}

func (s *Server) stopIntakeWorkers() {
	s.intakeMu.Lock()
	if !s.intakeClosing {
		s.intakeClosing = true
		if s.intakeStop != nil {
			close(s.intakeStop)
		}
	}
	s.intakeMu.Unlock()
	s.intakeWG.Wait()
}

func (s *Server) processIntakeJob(identifier string) {
	ctx := context.Background()
	job, found, err := s.intakeStore.ClaimID(ctx, identifier)
	if err != nil || !found {
		return
	}
	if !s.advanceIntake(ctx, &job, "parse", 0) {
		return
	}
	purpose, _ := job.Payload["purpose"].(string)
	sourceRelative, _ := job.Payload["relative_path"].(string)
	if containsSecret(purpose) || containsSecret(sourceRelative) {
		s.failIntake(ctx, job, "SECURITY", errors.New("document metadata contains a high-confidence secret"))
		s.removeIntakeSource(job)
		return
	}
	name, content, err := s.readIntakeSource(job)
	if err != nil {
		s.failIntake(ctx, job, "UNSAFE_SOURCE", err)
		return
	}
	extracted, err := document.ExtractBytes(name, content)
	if err != nil {
		category := classifyIntakeExtractionError(err)
		s.failIntake(ctx, job, category, err)
		if !intakeFailureRetryable(category) {
			s.removeIntakeSource(job)
		}
		return
	}
	pageCount := extractedPageCount(extracted)
	for _, stage := range []string{"extract", "ocr", "chunk", "assess"} {
		if !s.advanceIntake(ctx, &job, stage, pageCount) {
			return
		}
	}
	if !s.intakeJobRunning(ctx, job) {
		return
	}
	relative, candidate, err := s.writeIntakeCandidate(job, extracted)
	if err != nil {
		s.failIntake(ctx, job, "PERSIST_FAILED", err)
		return
	}
	if !s.advanceIntake(ctx, &job, "persist", pageCount) {
		_ = os.Remove(candidate)
		return
	}
	result := intakeSuccessResult(relative, extracted)
	if _, err := s.intakeStore.Succeed(ctx, job.ID, job.Attempt, result); err != nil {
		_ = os.Remove(candidate)
		return
	}
	s.removeIntakeSource(job)
}

func (s *Server) advanceIntake(ctx context.Context, job *document.Job, stage string, pageCount int) bool {
	next, err := s.intakeStore.Advance(ctx, job.ID, job.Attempt, stage, pageCount)
	if err != nil {
		return false
	}
	*job = next
	return true
}

func (s *Server) intakeJobRunning(ctx context.Context, job document.Job) bool {
	current, err := s.intakeStore.Get(ctx, job.ID)
	return err == nil && current.State == "RUNNING" && current.Attempt == job.Attempt
}

func (s *Server) failIntake(ctx context.Context, job document.Job, category string, detail error) {
	if _, err := s.intakeStore.Fail(ctx, job.ID, job.Attempt, category, detail); err != nil && !errors.Is(err, document.ErrLeaseLost) {
		s.logger.Printf("intake job failure could not be persisted: id=%s category=%s", job.ID, category)
	}
}

func (s *Server) readIntakeSource(job document.Job) (string, []byte, error) {
	name, nameOK := job.Payload["name"].(string)
	reference, refOK := job.Payload["staging_ref"].(string)
	expected, digestOK := job.Payload["source_sha256"].(string)
	if !nameOK || !refOK || !digestOK || safeFileName(name) != name || len(expected) != 64 || !strings.HasPrefix(filepath.ToSlash(reference), "service/intake/uploads/") {
		return "", nil, errors.New("stored intake source metadata is invalid")
	}
	path, err := safeDocumentPath(s.KnowledgeRoot, reference)
	if err != nil {
		return "", nil, errors.New("stored intake source path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 1 || info.Size() > document.MaxSourceBytes {
		return "", nil, errors.New("stored intake source is unavailable")
	}
	content, err := os.ReadFile(path)
	if err != nil || int64(len(content)) != info.Size() {
		return "", nil, errors.New("stored intake source could not be read safely")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expected {
		return "", nil, errors.New("stored intake source digest changed")
	}
	return name, content, nil
}

func classifyIntakeExtractionError(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "secret"):
		return "SECURITY"
	case strings.Contains(message, "unsupported document format"):
		return "UNSUPPORTED_FORMAT"
	case strings.Contains(message, "invalid"), strings.Contains(message, "empty"), strings.Contains(message, "produced no auditable content"):
		return "INVALID_DOCUMENT"
	default:
		return "EXTRACT_FAILED"
	}
}

func extractedPageCount(result document.Result) int {
	pages := 0
	for _, chunk := range result.Chunks {
		if chunk.PageEnd > pages {
			pages = chunk.PageEnd
		}
	}
	if pages > 10000 {
		return 10000
	}
	return pages
}

func (s *Server) writeIntakeCandidate(job document.Job, result document.Result) (string, string, error) {
	name := boundedCandidateName(safeFileName(result.SourceName))
	stamp := time.Unix(0, int64(job.CreatedAt*1e9)).UTC().Format("20060102T150405.000000000Z")
	relative := filepath.ToSlash(filepath.Join("personal-experience", "candidates", stamp+"-"+job.ID[:8]+"-"+name+".md"))
	path, err := safeDocumentPath(s.KnowledgeRoot, relative)
	if err != nil {
		return "", "", err
	}
	purpose, _ := job.Payload["purpose"].(string)
	sourceRelative, _ := job.Payload["relative_path"].(string)
	warnings, _ := json.Marshal(result.Warnings)
	reviewReasons, _ := json.Marshal(result.ReviewReasons)
	body := "---\nstatus: CANDIDATE\nsource: dashboard-intake\nsource_name: " + yamlString(result.SourceName) +
		"\nsource_sha256: " + result.SourceSHA256 + "\nsource_format: " + yamlString(result.Format) +
		"\npurpose: " + yamlString(purpose) + "\nrelative_path: " + yamlString(sourceRelative) +
		"\nwarnings: " + string(warnings) + "\nreview_reasons: " + string(reviewReasons) + "\n---\n\n" + strings.TrimSpace(result.Text) + "\n"
	if len(body) > 16*1024*1024 {
		return "", "", errors.New("extracted candidate is too large")
	}
	if err := safeio.AtomicWrite(path, []byte(body), 0o600); err != nil {
		return "", "", err
	}
	return relative, path, nil
}

func boundedCandidateName(value string) string {
	if len(value) <= 120 {
		return value
	}
	extension := filepath.Ext(value)
	if len(extension) > 12 {
		extension = ""
	}
	base := strings.TrimSuffix(value, filepath.Ext(value))
	limit := 120 - len(extension)
	if limit < 1 {
		return value[:120]
	}
	return strings.TrimRight(base[:min(limit, len(base))], "-.") + extension
}

func yamlString(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func intakeSuccessResult(relative string, result document.Result) map[string]any {
	chunks := make([]map[string]any, 0, len(result.Chunks))
	for _, chunk := range result.Chunks {
		item := map[string]any{"id": chunk.ID, "ordinal": chunk.Ordinal}
		if chunk.PageStart > 0 {
			item["page_start"] = chunk.PageStart
		}
		if chunk.PageEnd > 0 {
			item["page_end"] = chunk.PageEnd
		}
		chunks = append(chunks, item)
	}
	status := "READY_FOR_REVIEW"
	if len(result.ReviewReasons) > 0 {
		status = "REVIEW_REQUIRED"
	}
	return map[string]any{
		"status": status, "retryable": false,
		"candidate": map[string]any{
			"state": "CANDIDATE", "path": relative, "name": result.SourceName,
			"source_sha256": result.SourceSHA256, "format": result.Format,
			"chunks": chunks, "warnings": result.Warnings, "review_reasons": result.ReviewReasons,
		},
	}
}

func (s *Server) removeIntakeSource(job document.Job) {
	reference, ok := job.Payload["staging_ref"].(string)
	if !ok || !strings.HasPrefix(filepath.ToSlash(reference), "service/intake/uploads/") {
		return
	}
	path, err := safeDocumentPath(s.KnowledgeRoot, reference)
	if err == nil {
		_ = os.Remove(path)
	}
}

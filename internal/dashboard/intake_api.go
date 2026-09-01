package dashboard

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

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

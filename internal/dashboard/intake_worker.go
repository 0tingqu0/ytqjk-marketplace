package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

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

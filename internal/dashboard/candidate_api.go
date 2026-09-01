package dashboard

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

const (
	maxCandidateBytes = 16 * 1024 * 1024
	documentPreview   = 24_000
)

var (
	errCandidateChanged = errors.New("candidate changed")
	errApprovedExists   = errors.New("approved document already exists")
	evidencePattern     = regexp.MustCompile(`(?i)https?://|\b(?:commit|sha|test|tested|source|evidence)\b|来源|证据|测试|提交|版本|复现`)
	statusCandidateLine = regexp.MustCompile(`(?i)^\s*status\s*:\s*CANDIDATE\s*$`)
)

type stableFile struct {
	path     string
	relative string
	content  []byte
	info     os.FileInfo
}

type approvalAssessment struct {
	Decision string   `json:"decision"`
	Reasons  []string `json:"reasons"`
}

func (s *Server) readDocument(writer http.ResponseWriter, rawRelative string) int {
	relative, err := markdownRelative(rawRelative)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_PATH", "Document path is invalid")
		return http.StatusBadRequest
	}
	snapshot, err := readStableRelative(s.KnowledgeRoot, relative, maxCandidateBytes)
	if err != nil || bytes.IndexByte(snapshot.content, 0) >= 0 || !utf8.Valid(snapshot.content) {
		writeError(writer, http.StatusNotFound, "DOCUMENT_NOT_FOUND", "Document not found")
		return http.StatusNotFound
	}
	preview := snapshot.content
	if !isCandidateRelative(relative) && len(preview) > documentPreview {
		preview = validUTF8Prefix(preview, documentPreview)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "path": relative, "content": string(preview),
		"size": len(snapshot.content), "truncated": len(preview) < len(snapshot.content),
		"version": candidateVersion(snapshot.content),
	})
	return http.StatusOK
}

func (s *Server) updateCandidate(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path            string `json:"path"`
		Content         string `json:"content"`
		ExpectedVersion string `json:"expected_version"`
	}
	if err := readJSON(request, &payload); err != nil || !validCandidateContent(payload.Content) || !validVersion(payload.ExpectedVersion) {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料无效")
		return http.StatusBadRequest
	}
	relative, err := candidateRelative(payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}

	s.candidateMu.Lock()
	defer s.candidateMu.Unlock()
	current, err := readStableRelative(s.KnowledgeRoot, relative, maxCandidateBytes)
	if err != nil {
		writeError(writer, http.StatusNotFound, "CANDIDATE_NOT_FOUND", "候选资料不存在")
		return http.StatusNotFound
	}
	if candidateVersion(current.content) != payload.ExpectedVersion {
		writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
		return http.StatusConflict
	}
	if err := replaceStableFile(s.KnowledgeRoot, current, []byte(payload.Content)); err != nil {
		if errors.Is(err, errCandidateChanged) {
			writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
			return http.StatusConflict
		}
		writeError(writer, http.StatusInternalServerError, "CANDIDATE_WRITE_FAILED", "候选资料写入失败")
		return http.StatusInternalServerError
	}
	written, err := readStableRelative(s.KnowledgeRoot, relative, maxCandidateBytes)
	if err != nil || !bytes.Equal(written.content, []byte(payload.Content)) {
		writeError(writer, http.StatusInternalServerError, "CANDIDATE_WRITE_FAILED", "候选资料写入校验失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "path": relative, "state": "candidate",
		"version": candidateVersion(written.content), "assessment": assessForApproval(payload.Content),
	})
	return http.StatusOK
}

func (s *Server) deleteCandidate(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path            string `json:"path"`
		ExpectedVersion string `json:"expected_version"`
	}
	if err := readJSON(request, &payload); err != nil || payload.ExpectedVersion != "" && !validVersion(payload.ExpectedVersion) {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	relative, err := candidateRelative(payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}

	s.candidateMu.Lock()
	defer s.candidateMu.Unlock()
	current, err := readStableRelative(s.KnowledgeRoot, relative, maxCandidateBytes)
	if err != nil {
		writeError(writer, http.StatusNotFound, "CANDIDATE_NOT_FOUND", "候选资料不存在")
		return http.StatusNotFound
	}
	if payload.ExpectedVersion != "" && candidateVersion(current.content) != payload.ExpectedVersion {
		writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
		return http.StatusConflict
	}
	if err := verifyStableFile(s.KnowledgeRoot, current); err != nil {
		writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
		return http.StatusConflict
	}
	if err := os.Remove(current.path); err != nil {
		writeError(writer, http.StatusInternalServerError, "CANDIDATE_DELETE_FAILED", "候选资料删除失败")
		return http.StatusInternalServerError
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "path": relative, "state": "deleted"})
	return http.StatusOK
}

func (s *Server) approve(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Path            string `json:"path"`
		ExpectedVersion string `json:"expected_version"`
	}
	if err := readJSON(request, &payload); err != nil || payload.ExpectedVersion != "" && !validVersion(payload.ExpectedVersion) {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	relative, err := candidateRelative(payload.Path)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}

	s.candidateMu.Lock()
	current, err := readStableRelative(s.KnowledgeRoot, relative, maxCandidateBytes)
	if err != nil {
		s.candidateMu.Unlock()
		writeError(writer, http.StatusNotFound, "CANDIDATE_NOT_FOUND", "候选资料不存在")
		return http.StatusNotFound
	}
	if payload.ExpectedVersion != "" && candidateVersion(current.content) != payload.ExpectedVersion {
		s.candidateMu.Unlock()
		writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
		return http.StatusConflict
	}
	content := string(current.content)
	if !validCandidateContent(content) {
		s.candidateMu.Unlock()
		writeError(writer, http.StatusBadRequest, "UNSAFE_CANDIDATE", "候选资料无效或包含敏感内容")
		return http.StatusBadRequest
	}
	approvedRelative, target, err := approvedTarget(s.KnowledgeRoot, relative)
	if err != nil {
		s.candidateMu.Unlock()
		writeError(writer, http.StatusBadRequest, "INVALID_CANDIDATE", "候选资料路径无效")
		return http.StatusBadRequest
	}
	assessment := assessForApproval(content)
	approved := approvedContent(content, time.Now().UTC().Format(time.RFC3339Nano))
	if err := publishApproved(s.KnowledgeRoot, current, target, []byte(approved)); err != nil {
		s.candidateMu.Unlock()
		if errors.Is(err, errApprovedExists) {
			writeError(writer, http.StatusConflict, "APPROVED_DOCUMENT_EXISTS", "同名已批准资料已经存在")
			return http.StatusConflict
		}
		if errors.Is(err, errCandidateChanged) {
			writeError(writer, http.StatusConflict, "CANDIDATE_VERSION_CONFLICT", "候选资料已被其他会话更新")
			return http.StatusConflict
		}
		writeError(writer, http.StatusInternalServerError, "APPROVAL_FAILED", "候选资料批准失败")
		return http.StatusInternalServerError
	}
	s.candidateMu.Unlock()

	indexStatus := "REBUILT"
	s.globalIndexMu.Lock()
	_, indexErr := rag.BuildGlobal(s.KnowledgeRoot, "auto")
	s.globalIndexMu.Unlock()
	if indexErr != nil {
		indexStatus = "STALE"
		s.logger.Printf("candidate approval completed but global index refresh failed")
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "path": approvedRelative, "state": "approved",
		"assessment": assessment, "index_status": indexStatus,
	})
	return http.StatusOK
}

package dashboard

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
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

func markdownRelative(raw string) (string, error) {
	value := strings.ReplaceAll(raw, "\\", "/")
	if value == "" || strings.ContainsRune(value, 0) {
		return "", errors.New("invalid path")
	}
	native := filepath.FromSlash(value)
	clean := filepath.ToSlash(filepath.Clean(native))
	if clean != value || filepath.IsAbs(native) || filepath.VolumeName(native) != "" || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.Ext(clean) != ".md" {
		return "", errors.New("invalid path")
	}
	return clean, nil
}

func candidateRelative(raw string) (string, error) {
	relative, err := markdownRelative(raw)
	if err != nil || !isCandidateRelative(relative) {
		return "", errors.New("invalid candidate path")
	}
	parts := strings.Split(relative, "/")
	for _, part := range parts[2 : len(parts)-1] {
		if part == "chunks" || part == "originals" {
			return "", errors.New("internal candidate path")
		}
	}
	return relative, nil
}

func isCandidateRelative(relative string) bool {
	return strings.HasPrefix(relative, "personal-experience/candidates/") || strings.HasPrefix(relative, "error-experience/candidates/")
}

func readStableRelative(root, relative string, maximum int64) (stableFile, error) {
	path, err := safeDocumentPath(root, relative)
	if err != nil {
		return stableFile{}, err
	}
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() < 0 || before.Size() > maximum {
		return stableFile{}, errors.New("unsafe document")
	}
	handle, err := os.Open(path)
	if err != nil {
		return stableFile{}, err
	}
	defer handle.Close()
	opened, err := handle.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) || !candidateSingleLink(handle) {
		return stableFile{}, errors.New("document changed")
	}
	content, err := io.ReadAll(io.LimitReader(handle, maximum+1))
	if err != nil || int64(len(content)) > maximum {
		return stableFile{}, errors.New("document too large")
	}
	afterOpen, openErr := handle.Stat()
	afterPath, pathErr := os.Lstat(path)
	if openErr != nil || pathErr != nil || !afterPath.Mode().IsRegular() || afterPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, afterOpen) || !os.SameFile(before, afterPath) ||
		before.Size() != int64(len(content)) || afterOpen.Size() != before.Size() || afterPath.Size() != before.Size() ||
		!afterOpen.ModTime().Equal(before.ModTime()) || !afterPath.ModTime().Equal(before.ModTime()) {
		return stableFile{}, errors.New("document changed")
	}
	return stableFile{path: path, relative: relative, content: content, info: before}, nil
}

func verifyStableFile(root string, expected stableFile) error {
	current, err := readStableRelative(root, expected.relative, int64(max(len(expected.content), 1)))
	if err != nil || !os.SameFile(expected.info, current.info) || !bytes.Equal(expected.content, current.content) ||
		expected.info.Size() != current.info.Size() || !expected.info.ModTime().Equal(current.info.ModTime()) {
		return errCandidateChanged
	}
	return nil
}

func replaceStableFile(root string, expected stableFile, content []byte) error {
	if err := verifyStableFile(root, expected); err != nil {
		return err
	}
	temporary, err := stagedFile(filepath.Dir(expected.path), content)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := verifyStableFile(root, expected); err != nil {
		return err
	}
	return replaceFile(temporary, expected.path)
}

func stagedFile(directory string, content []byte) (string, error) {
	file, err := os.CreateTemp(directory, ".ytqjk-candidate-*.tmp")
	if err != nil {
		return "", err
	}
	path := file.Name()
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return "", err
	}
	if _, err := file.Write(content); err != nil {
		return "", err
	}
	if err := file.Sync(); err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func approvedTarget(root, relative string) (string, string, error) {
	var approved string
	switch {
	case strings.HasPrefix(relative, "personal-experience/candidates/"):
		approved = "personal-experience/approved/" + strings.TrimPrefix(relative, "personal-experience/candidates/")
	case strings.HasPrefix(relative, "error-experience/candidates/"):
		approved = "error-experience/approved/" + strings.TrimPrefix(relative, "error-experience/candidates/")
	default:
		return "", "", errors.New("invalid candidate path")
	}
	target, err := safeDocumentPath(root, approved)
	return approved, target, err
}

func publishApproved(root string, source stableFile, target string, content []byte) error {
	if _, err := os.Lstat(target); err == nil {
		return errApprovedExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	if _, err := safeio.Contained(root, target); err != nil {
		return err
	}
	if err := verifyStableFile(root, source); err != nil {
		return err
	}
	staged, err := stagedFile(filepath.Dir(target), content)
	if err != nil {
		return err
	}
	defer os.Remove(staged)
	if err := os.Link(staged, target); err != nil {
		if _, statErr := os.Lstat(target); statErr == nil {
			return errApprovedExists
		}
		if err := writeNewFile(target, content); err != nil {
			return err
		}
	}
	rollback := true
	defer func() {
		if rollback {
			_ = os.Remove(target)
		}
	}()
	if err := verifyStableFile(root, source); err != nil {
		return err
	}
	if err := os.Remove(source.path); err != nil {
		return err
	}
	rollback = false
	return nil
}

func writeNewFile(path string, content []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return errApprovedExists
		}
		return err
	}
	ok := false
	defer func() {
		_ = file.Close()
		if !ok {
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(content); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

func validCandidateContent(content string) bool {
	return strings.TrimSpace(content) != "" && len([]byte(content)) <= maxCandidateBytes && !strings.ContainsRune(content, 0) && !containsSecret(content)
}

func candidateVersion(content []byte) string {
	normalized := bytes.ReplaceAll(content, []byte("\r\n"), []byte("\n"))
	normalized = bytes.ReplaceAll(normalized, []byte("\r"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:])
}

func validVersion(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func assessForApproval(content string) approvalAssessment {
	content = assessmentContent(content)
	reasons := make([]string, 0, 2)
	if utf8.RuneCountInString(strings.TrimSpace(content)) < 200 {
		reasons = append(reasons, "有效文本不足 200 字符")
	}
	if !evidencePattern.MatchString(content) {
		reasons = append(reasons, "缺少可追溯的来源、证据或验证线索")
	}
	if len(reasons) == 0 {
		return approvalAssessment{Decision: "READY_FOR_REVIEW", Reasons: []string{"满足完整性与可追溯性要求，可进入人工复审"}}
	}
	return approvalAssessment{Decision: "NOT_READY", Reasons: reasons}
}

func assessmentContent(content string) string {
	const marker = "## 原始资料\n\n"
	if _, after, found := strings.Cut(strings.ReplaceAll(content, "\r\n", "\n"), marker); found {
		return after
	}
	return content
}

func approvedContent(content, approvedAt string) string {
	normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) > 2 && lines[0] == "---" {
		closing := -1
		status := -1
		for index := 1; index < len(lines); index++ {
			if lines[index] == "---" {
				closing = index
				break
			}
			if statusCandidateLine.MatchString(lines[index]) {
				status = index
			}
		}
		if closing > 0 && status > 0 {
			lines[status] = "status: APPROVED"
			metadata := []string{"approved_at: " + approvedAt, "approval: manual-dashboard"}
			lines = append(lines[:closing], append(metadata, lines[closing:]...)...)
			return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
		}
	}
	return "---\nstatus: APPROVED\napproved_at: " + approvedAt + "\napproval: manual-dashboard\n---\n\n" + strings.TrimSpace(normalized) + "\n"
}

func validUTF8Prefix(value []byte, maximum int) []byte {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.Valid(value[:end]) {
		end--
	}
	return value[:end]
}

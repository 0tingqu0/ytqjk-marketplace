package dashboard

import (
	"context"
	"net/http"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	groupIndexPreviewTTL = 15 * time.Minute
	maxGroupIndexPreview = 256
	groupPreviewActive   = "ACTIVE"
	groupPreviewRunning  = "IN_PROGRESS"
	groupPreviewConsumed = "CONSUMED"
)

type issuedGroupIndexPreview struct {
	NodeID           string
	DocumentIDs      []string
	ExpectedRevision int64
	LibraryDigest    string
	CreatedAt        time.Time
	State            string
}

type groupIndexPreviewRequest struct {
	NodeID      string   `json:"node_id"`
	DocumentIDs []string `json:"document_ids"`
}

func (s *Server) groupIndexPreview(writer http.ResponseWriter, request *http.Request) int {
	body, err := readRequestBody(request)
	if err != nil {
		return writeGroupPreviewError(writer, "INVALID_REQUEST_FIELDS", http.StatusBadRequest)
	}
	input, code := decodeGroupIndexPreviewRequest(body)
	if code != "" {
		return writeGroupPreviewError(writer, code, http.StatusBadRequest)
	}
	if err := rag.ValidateGroupSelection(input.DocumentIDs); err != nil {
		return writeGroupIndexError(writer, err)
	}
	store, err := s.openLibraryStore()
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	defer store.Close()
	snapshot, err := store.Snapshot(nil)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	if status, code := groupNodeStatus(snapshot, input.NodeID); code != "" {
		return writeGroupPreviewError(writer, code, status)
	}
	digest, err := safeio.RandomHex(32)
	if err != nil {
		return writeGroupPreviewError(writer, "PREVIEW_CREATE_FAILED", http.StatusInternalServerError)
	}
	record := issuedGroupIndexPreview{
		NodeID: input.NodeID, DocumentIDs: append([]string(nil), input.DocumentIDs...),
		ExpectedRevision: snapshot.Revision, LibraryDigest: snapshot.Digest, CreatedAt: time.Now().UTC(),
		State: groupPreviewActive,
	}
	if !s.storeGroupIndexPreview(digest, record) {
		return writeGroupPreviewError(writer, "PREVIEW_CAPACITY_EXCEEDED", http.StatusServiceUnavailable)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true,
		"preview": map[string]any{
			"digest": digest, "expected_revision": snapshot.Revision,
			"library_digest": snapshot.Digest, "node_id": input.NodeID,
			"document_count": len(input.DocumentIDs), "source_scope": "approved-verified-only",
		},
	})
	return http.StatusOK
}

func (s *Server) groupIndexRebuild(writer http.ResponseWriter, request *http.Request) int {
	body, err := readRequestBody(request)
	if err != nil {
		return writeGroupPreviewError(writer, "INVALID_REQUEST_FIELDS", http.StatusBadRequest)
	}
	input, err := library.DecodeCommitRequest(body)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	record, status, code := s.claimGroupIndexPreview(input)
	if code != "" {
		return writeGroupPreviewError(writer, code, status)
	}
	consumePreview := false
	previewFinished := false
	defer func() {
		if !previewFinished {
			s.finishGroupIndexPreview(input.Digest, consumePreview)
		}
	}()

	s.treeCommitMu.Lock()
	defer s.treeCommitMu.Unlock()
	store, err := s.openLibraryStore()
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	defer store.Close()
	guard, err := store.BeginSnapshotGuard(context.WithoutCancel(request.Context()))
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	guardClosed := false
	defer func() {
		if !guardClosed {
			_ = guard.Close()
		}
	}()
	before := guard.Snapshot()
	if before.Revision != record.ExpectedRevision || before.Digest != record.LibraryDigest {
		return writeGroupPreviewError(writer, "REVISION_CONFLICT", http.StatusConflict)
	}
	if status, code := groupNodeStatus(before, record.NodeID); code != "" {
		return writeGroupPreviewError(writer, code, status)
	}
	consumePreview = true
	s.groupIndexMu.Lock()
	materialization, buildErr := rag.BuildGroup(s.KnowledgeRoot, record.NodeID, record.DocumentIDs)
	s.groupIndexMu.Unlock()
	if buildErr != nil {
		return writeGroupIndexError(writer, buildErr)
	}
	guardRelease := "RELEASED"
	if err := guard.Close(); err != nil {
		guardRelease = "RELEASE_UNCONFIRMED"
	}
	guardClosed = true
	s.finishGroupIndexPreview(input.Digest, true)
	previewFinished = true
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "action": "rebuild", "node_id": record.NodeID,
		"library_revision": before.Revision, "library_digest": before.Digest,
		"topology_guard": guardRelease, "materialization": materialization,
	})
	return http.StatusOK
}

func (s *Server) storeGroupIndexPreview(digest string, record issuedGroupIndexPreview) bool {
	s.groupPreviewMu.Lock()
	defer s.groupPreviewMu.Unlock()
	if s.groupPreviews == nil {
		s.groupPreviews = make(map[string]issuedGroupIndexPreview)
	}
	cutoff := record.CreatedAt.Add(-groupIndexPreviewTTL)
	for key, candidate := range s.groupPreviews {
		if candidate.State != groupPreviewRunning && candidate.CreatedAt.Before(cutoff) {
			delete(s.groupPreviews, key)
		}
	}
	if len(s.groupPreviews) >= maxGroupIndexPreview {
		oldestKey := ""
		var oldestTime time.Time
		for key, candidate := range s.groupPreviews {
			if candidate.State == groupPreviewRunning {
				continue
			}
			if oldestKey == "" || candidate.CreatedAt.Before(oldestTime) ||
				(candidate.CreatedAt.Equal(oldestTime) && key < oldestKey) {
				oldestKey, oldestTime = key, candidate.CreatedAt
			}
		}
		if oldestKey == "" {
			return false
		}
		delete(s.groupPreviews, oldestKey)
	}
	s.groupPreviews[digest] = record
	return true
}

func (s *Server) claimGroupIndexPreview(input library.CommitRequest) (issuedGroupIndexPreview, int, string) {
	s.groupPreviewMu.Lock()
	defer s.groupPreviewMu.Unlock()
	record, found := s.groupPreviews[input.Digest]
	if !found {
		return record, http.StatusNotFound, "PREVIEW_NOT_FOUND"
	}
	switch record.State {
	case groupPreviewConsumed:
		return record, http.StatusConflict, "PREVIEW_REPLAYED"
	case groupPreviewRunning:
		return record, http.StatusConflict, "PREVIEW_IN_PROGRESS"
	case groupPreviewActive:
	default:
		return record, http.StatusConflict, "PREVIEW_STATE_INVALID"
	}
	if time.Since(record.CreatedAt) > groupIndexPreviewTTL {
		return record, http.StatusConflict, "PREVIEW_EXPIRED"
	}
	if input.ExpectedRevision != record.ExpectedRevision {
		return record, http.StatusConflict, "PREVIEW_MISMATCH"
	}
	record.State = groupPreviewRunning
	s.groupPreviews[input.Digest] = record
	return record, 0, ""
}

func (s *Server) finishGroupIndexPreview(digest string, consume bool) {
	s.groupPreviewMu.Lock()
	defer s.groupPreviewMu.Unlock()
	record, found := s.groupPreviews[digest]
	if !found || record.State != groupPreviewRunning {
		return
	}
	if consume {
		record.State = groupPreviewConsumed
	} else {
		record.State = groupPreviewActive
	}
	s.groupPreviews[digest] = record
}

func groupNodeStatus(snapshot library.Snapshot, nodeID string) (int, string) {
	for _, node := range snapshot.Nodes {
		if node.ID != nodeID {
			continue
		}
		if node.Type != library.TypeGroup {
			return http.StatusBadRequest, "GROUP_NODE_REQUIRED"
		}
		return 0, ""
	}
	return http.StatusNotFound, "UNKNOWN_NODE"
}

func writeGroupPreviewError(writer http.ResponseWriter, code string, status int) int {
	if code == "" {
		code = "GROUP_INDEX_OPERATION_FAILED"
	}
	writeError(writer, status, code, code)
	return status
}

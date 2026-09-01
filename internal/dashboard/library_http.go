package dashboard

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/library"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	defaultLibraryCapacity int64 = 1024 * 1024 * 1024
	initialLibraryRevision int64 = 1
)

func (s *Server) tree(writer http.ResponseWriter) int {
	store, err := s.openLibraryStore()
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	defer store.Close()
	snapshot, err := s.libraryStoreSnapshot(store)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "tree": snapshot})
	return http.StatusOK
}

func (s *Server) treePreview(writer http.ResponseWriter, request *http.Request) int {
	body, err := readRequestBody(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST_FIELDS", "Library preview request is invalid")
		return http.StatusBadRequest
	}
	input, err := library.DecodePreviewRequest(body)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	store, err := s.openLibraryStore()
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	defer store.Close()
	preview, err := store.Preview(input.Action, input.Payload)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "preview": preview})
	return http.StatusOK
}

func (s *Server) treeCommit(writer http.ResponseWriter, request *http.Request, action string) int {
	body, err := readRequestBody(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "INVALID_REQUEST_FIELDS", "Library commit request is invalid")
		return http.StatusBadRequest
	}
	input, err := library.DecodeCommitRequest(body)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	s.treeCommitMu.Lock()
	defer s.treeCommitMu.Unlock()
	store, err := s.openLibraryStore()
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	defer store.Close()
	if _, err := store.Commit(action, input.Digest, input.ExpectedRevision); err != nil {
		return writeLibraryFailure(writer, err)
	}
	snapshot, err := s.libraryStoreSnapshot(store)
	if err != nil {
		return writeLibraryFailure(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "tree": snapshot})
	return http.StatusOK
}

func (s *Server) openLibraryStore() (*library.Store, error) {
	nodes, err := s.librarySeedNodes()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(s.KnowledgeRoot, "service", "library-v1.sqlite3")
	store, err := library.OpenStore(path, nodes, initialLibraryRevision)
	if err != nil {
		return nil, err
	}
	if _, err := store.ReconcileSeedNodes(nodes); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func (s *Server) librarySeedNodes() ([]library.Node, error) {
	var catalog rag.Catalog
	err := safeio.ReadJSON(filepath.Join(s.KnowledgeRoot, "catalog.json"), &catalog)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	nodes := make([]library.Node, 0, len(catalog.Projects)+1)
	nodes = append(nodes, library.Node{
		ID: "global", Title: "个人总知识库", Type: library.TypeGlobal,
		CapacityBytes: defaultLibraryCapacity, Metadata: map[string]string{},
	})
	for identifier, project := range catalog.Projects {
		title := project.Name
		if title == "" {
			title = identifier
		}
		parentID := "global"
		nodes = append(nodes, library.Node{
			ID: identifier, Title: title, Type: library.TypeProject,
			ParentID: &parentID, CapacityBytes: defaultLibraryCapacity,
			Metadata: map[string]string{},
		})
	}
	return nodes, nil
}

func (s *Server) libraryStoreSnapshot(store *library.Store) (library.Snapshot, error) {
	configured, err := store.Snapshot(nil)
	if err != nil {
		return library.Snapshot{}, err
	}
	statistics := make(map[string]library.Statistics)
	for _, node := range configured.Nodes {
		var directory string
		switch node.Type {
		case library.TypeGlobal:
			directory = filepath.Join(s.KnowledgeRoot, "global-cache")
		case library.TypeGroup:
			stats, statsErr := s.groupLibraryStatistics(node.ID)
			if statsErr != nil {
				return library.Snapshot{}, statsErr
			}
			statistics[node.ID] = stats
			continue
		case library.TypeProject:
			directory = filepath.Join(s.KnowledgeRoot, "projects", node.ID)
		default:
			continue
		}
		stats, statsErr := libraryStatistics(directory)
		if statsErr != nil {
			return library.Snapshot{}, statsErr
		}
		statistics[node.ID] = stats
	}
	return store.Snapshot(statistics)
}

func (s *Server) groupLibraryStatistics(nodeID string) (library.Statistics, error) {
	return s.withGroupIndexReadLock(func() (library.Statistics, error) {
		directory := filepath.Join(s.KnowledgeRoot, "libraries", nodeID)
		used, err := directoryUsage(directory)
		if err != nil {
			return library.Statistics{}, err
		}
		statistics := library.Statistics{UsedBytes: used}
		status := rag.ReadGroupStatus(s.KnowledgeRoot, nodeID)
		if status.Status == "READY" || status.Status == "STALE" {
			statistics.IndexedDocuments = int64(status.Documents)
			statistics.TotalDocuments = int64(status.Documents)
			statistics.IndexedChunks = int64(status.Chunks)
			statistics.TotalChunks = int64(status.Chunks)
		}
		return statistics, nil
	})
}

func (s *Server) withGroupIndexReadLock(read func() (library.Statistics, error)) (library.Statistics, error) {
	s.groupIndexMu.RLock()
	defer s.groupIndexMu.RUnlock()
	return read()
}

func libraryStatistics(directory string) (library.Statistics, error) {
	var manifest rag.Manifest
	err := safeio.ReadJSON(filepath.Join(directory, "manifest.json"), &manifest)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return library.Statistics{}, err
	}
	used, err := directoryUsage(directory)
	if err != nil {
		return library.Statistics{}, err
	}
	return library.Statistics{
		UsedBytes: int64(used), IndexedDocuments: int64(manifest.Stats.Files),
		TotalDocuments: int64(manifest.Stats.Files), IndexedChunks: int64(manifest.Stats.Chunks),
		TotalChunks: int64(manifest.Stats.Chunks),
	}, nil
}

func directoryUsage(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			if path == root && errors.Is(walkErr, os.ErrNotExist) {
				return nil
			}
			return walkErr
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	return total, err
}

func writeLibraryFailure(writer http.ResponseWriter, err error) int {
	var contractErr *library.Error
	if !errors.As(err, &contractErr) {
		writeError(writer, http.StatusInternalServerError, "LIBRARY_STORE_FAILED", "Library operation failed")
		return http.StatusInternalServerError
	}
	if contractErr.IsServerFault() {
		writeError(writer, http.StatusServiceUnavailable, contractErr.Code, "Library storage is unavailable")
		return http.StatusServiceUnavailable
	}
	status := libraryErrorStatus(contractErr.Code)
	writeError(writer, status, contractErr.Code, "Library operation was rejected")
	return status
}

func libraryErrorStatus(code string) int {
	switch code {
	case "UNKNOWN_NODE", "PREVIEW_NOT_FOUND":
		return http.StatusNotFound
	case "REVISION_CONFLICT", "REVISION_EXHAUSTED", "PREVIEW_MISMATCH",
		"PREVIEW_REPLAYED", "PREVIEW_ACTION_MISMATCH", "DUPLICATE_NODE",
		"DUPLICATE_EDGE", "MULTIPLE_PARENTS", "NODE_ALREADY_ROOT",
		"NODE_IS_ROOT", "EDGE_NOT_FOUND", "CYCLE_DETECTED", "SELF_PARENT":
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

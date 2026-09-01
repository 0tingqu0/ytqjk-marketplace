package dashboard

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

const maxLibraryChunks = 500

type libraryChunk struct {
	Path         string `json:"path"`
	LineStart    int    `json:"line_start"`
	LineEnd      int    `json:"line_end"`
	Content      string `json:"content"`
	SourceSHA256 string `json:"source_sha256"`
}

func (s *Server) writeLibrary(writer http.ResponseWriter, identifier string) int {
	global := identifier == "" || identifier == "global"
	directory := filepath.Join(s.KnowledgeRoot, "global-cache")
	if !global {
		if !safeIdentifier(identifier) {
			writeError(writer, http.StatusBadRequest, "INVALID_PROJECT", "Project identifier is invalid")
			return http.StatusBadRequest
		}
		directory = filepath.Join(s.KnowledgeRoot, "projects", identifier)
		projectsRoot := filepath.Join(s.KnowledgeRoot, "projects")
		if _, err := os.Lstat(projectsRoot); err != nil {
			writeError(writer, http.StatusNotFound, "LIBRARY_NOT_FOUND", "Library not found")
			return http.StatusNotFound
		}
		if _, err := safeio.Contained(projectsRoot, directory); err != nil {
			writeError(writer, http.StatusBadRequest, "INVALID_PROJECT", "Project identifier is invalid")
			return http.StatusBadRequest
		}
	}
	var index rag.Index
	var manifest rag.Manifest
	if err := safeio.ReadJSON(filepath.Join(directory, "index.json"), &index); err != nil || index.SchemaVersion != rag.SchemaVersion {
		writeError(writer, http.StatusNotFound, "LIBRARY_NOT_FOUND", "Library not found")
		return http.StatusNotFound
	}
	if err := safeio.ReadJSON(filepath.Join(directory, "manifest.json"), &manifest); err != nil || manifest.SchemaVersion != rag.SchemaVersion {
		writeError(writer, http.StatusNotFound, "LIBRARY_NOT_FOUND", "Library manifest not found")
		return http.StatusNotFound
	}
	files, chunkCount := groupedLibraryChunks(index.Chunks, global)
	name := manifest.Identity.Name
	if name == "" {
		name = index.ProjectID
	}
	response := map[string]any{
		"ok": true, "id": index.ProjectID, "name": name,
		"indexed_at": manifest.IndexedAt, "files": files,
		"file_count": len(files), "chunk_count": chunkCount,
		"expected_files": manifest.Stats.Files, "expected_chunks": manifest.Stats.Chunks,
	}
	if !global {
		prefetch, cache := readProjectPrefetch(directory)
		response["prefetch"] = prefetch
		response["cache"] = cache
	}
	writeJSON(writer, http.StatusOK, response)
	return http.StatusOK
}

func groupedLibraryChunks(chunks []rag.Chunk, global bool) ([][]libraryChunk, int) {
	valid := make([]rag.Chunk, 0, min(len(chunks), maxLibraryChunks))
	for _, chunk := range chunks {
		if len(valid) >= maxLibraryChunks {
			break
		}
		if !validLibraryChunk(chunk, global) {
			continue
		}
		valid = append(valid, chunk)
	}
	sort.Slice(valid, func(i, j int) bool {
		if valid[i].Path == valid[j].Path {
			return valid[i].Start < valid[j].Start
		}
		return valid[i].Path < valid[j].Path
	})
	groups := make([][]libraryChunk, 0)
	for _, chunk := range valid {
		lineStart, lineEnd := chunk.LineStart, chunk.LineEnd
		if lineStart < 1 {
			lineStart = 1
		}
		if lineEnd < lineStart {
			lineEnd = lineStart + strings.Count(chunk.Content, "\n")
		}
		row := libraryChunk{
			Path: chunk.Path, LineStart: lineStart, LineEnd: lineEnd,
			Content: chunk.Content, SourceSHA256: chunk.Digest,
		}
		if len(groups) == 0 || groups[len(groups)-1][0].Path != chunk.Path {
			groups = append(groups, []libraryChunk{row})
		} else {
			groups[len(groups)-1] = append(groups[len(groups)-1], row)
		}
	}
	return groups, len(valid)
}

func validLibraryChunk(chunk rag.Chunk, global bool) bool {
	if !validVersion(chunk.ID) || !validVersion(chunk.Digest) || chunk.Path == "" || len(chunk.Path) > 4096 ||
		chunk.Start < 0 || chunk.End <= chunk.Start || strings.TrimSpace(chunk.Content) == "" ||
		!utf8.ValidString(chunk.Content) || len(chunk.Content) > maxCandidateBytes ||
		safeio.SHA256([]byte(chunk.Content)) != chunk.Digest || securitycheck.IsSensitivePath(chunk.Path) || containsSecret(chunk.Content) {
		return false
	}
	path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(chunk.Path)))
	if path != chunk.Path || filepath.IsAbs(filepath.FromSlash(path)) || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return false
	}
	return !global || governedGlobalPath(path)
}

func governedGlobalPath(path string) bool {
	for _, prefix := range []string{
		"global/", "verified/", "personal-experience/approved/", "error-experience/approved/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func readProjectPrefetch(projectDirectory string) ([]rag.PrefetchEntry, map[string]any) {
	knowledgeRoot := filepath.Dir(filepath.Dir(projectDirectory))
	rows, stats, err := rag.ListPrefetch(projectDirectory, knowledgeRoot, maxLibraryChunks)
	if err != nil {
		return []rag.PrefetchEntry{}, emptyProjectCache()
	}
	return rows, map[string]any{
		"entries": stats.Entries, "used_bytes": stats.UsedBytes,
		"project_used_bytes": stats.ProjectUsedBytes,
		"capacity_bytes":     stats.CapacityBytes,
		"capacity_exceeded":  stats.CapacityExceeded,
		"policy":             stats.Policy,
	}
}

func emptyProjectCache() map[string]any {
	return map[string]any{
		"entries": 0, "used_bytes": 0, "project_used_bytes": 0,
		"capacity_bytes": 1024 * 1024 * 1024, "capacity_exceeded": false,
		"policy": "LFU_LRU",
	}
}

package peer

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

const maxPeerContentRunes = 24_000

type QueryRow struct {
	MaterialID  string  `json:"material_id"`
	LibraryNode string  `json:"library_node"`
	Path        string  `json:"path"`
	LineStart   int     `json:"line_start"`
	LineEnd     int     `json:"line_end"`
	Content     string  `json:"content"`
	SourceSHA   string  `json:"source_sha256"`
	Scope       string  `json:"scope"`
	Score       float64 `json:"score"`
}

type QueryResponse struct {
	Status     string     `json:"status"`
	ProjectID  string     `json:"project_id"`
	NodeID     string     `json:"node_id"`
	Generation string     `json:"generation"`
	Results    []QueryRow `json:"results"`
}

func QueryLibrarySubtree(knowledgeRoot, projectID, exportNodeID, query string, limit int, value *tree.Tree) (QueryResponse, error) {
	if !utf8.ValidString(query) || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 2000 {
		return QueryResponse{}, errors.New("INVALID_PEER_QUERY")
	}
	if limit < 1 || limit > 20 {
		return QueryResponse{}, errors.New("INVALID_PEER_LIMIT")
	}
	libraries, err := ExportedLibraries(knowledgeRoot, projectID, exportNodeID, value)
	if err != nil {
		return QueryResponse{}, err
	}
	results := make([]QueryRow, 0)
	generations := make([]string, 0)
	for _, library := range libraries {
		indexPath := filepath.Join(library.Directory, "index.json")
		rows, manifest, err := rag.SearchIndex(indexPath, query, limit, peerScope(library))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return QueryResponse{}, errors.New("PEER_LIBRARY_INDEX_INVALID")
		}
		if manifest.SourceFingerprint != "" {
			generations = append(generations, library.NodeID+":"+manifest.SourceFingerprint)
		}
		for _, row := range rows {
			public, err := publicRow(library, row)
			if err != nil {
				return QueryResponse{}, err
			}
			results = append(results, public)
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].MaterialID < results[j].MaterialID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	status := "PEER_MISS"
	if len(results) > 0 {
		status = "PEER_HIT"
	}
	return QueryResponse{
		Status: status, ProjectID: projectID, NodeID: exportNodeID,
		Generation: strings.Join(generations, "|"), Results: results,
	}, nil
}

func FetchSubtreeMaterial(knowledgeRoot, projectID, exportNodeID, libraryNode, materialID string, value *tree.Tree) (QueryRow, error) {
	library, err := RequireExportedLibrary(knowledgeRoot, projectID, exportNodeID, libraryNode, value)
	if err != nil {
		return QueryRow{}, err
	}
	prefix, identifier, err := parseMaterialID(materialID)
	if err != nil {
		return QueryRow{}, err
	}
	expected := "library"
	if library.Kind == "project" {
		expected = "project"
	}
	if prefix != expected {
		return QueryRow{}, errors.New("INVALID_MATERIAL_ID")
	}
	var index rag.Index
	if err := safeio.ReadJSON(filepath.Join(library.Directory, "index.json"), &index); err != nil || index.SchemaVersion != rag.SchemaVersion {
		return QueryRow{}, errors.New("PEER_LIBRARY_INDEX_INVALID")
	}
	for _, chunk := range index.Chunks {
		if chunk.ID != identifier {
			continue
		}
		return publicRow(library, rag.QueryResult{
			ID: chunk.ID, Path: chunk.Path, Start: chunk.Start, End: chunk.End,
			LineStart: chunk.LineStart, LineEnd: chunk.LineEnd,
			Content: chunk.Content, Digest: chunk.Digest, Scope: peerScope(library),
		})
	}
	return QueryRow{}, errors.New("PEER_MATERIAL_NOT_FOUND")
}

func publicRow(library Library, row rag.QueryResult) (QueryRow, error) {
	if !signaturePattern.MatchString(row.ID) || !signaturePattern.MatchString(row.Digest) || !utf8.ValidString(row.Content) || utf8.RuneCountInString(row.Content) > maxPeerContentRunes || row.Path == "" || len(row.Path) > 4096 {
		return QueryRow{}, errors.New("PEER_RESULT_INVALID")
	}
	start, end := row.LineStart, row.LineEnd
	if start < 1 {
		start = row.Start + 1
	}
	if end < start {
		end = max(start, row.End)
	}
	prefix := "library"
	if library.Kind == "project" {
		prefix = "project"
	}
	return QueryRow{
		MaterialID: prefix + ":" + row.ID, LibraryNode: library.NodeID,
		Path: filepath.ToSlash(row.Path), LineStart: start, LineEnd: end,
		Content: row.Content, SourceSHA: row.Digest, Scope: peerScope(library), Score: row.Score,
	}, nil
}

func peerScope(library Library) string {
	if library.Kind == "project" {
		return "peer-project-source"
	}
	return "peer-" + library.Kind + "-descendant"
}

func parseMaterialID(value string) (string, string, error) {
	parts := strings.SplitN(value, ":", 2)
	if len(parts) != 2 || (parts[0] != "project" && parts[0] != "library") || !signaturePattern.MatchString(parts[1]) {
		return "", "", errors.New("INVALID_MATERIAL_ID")
	}
	return parts[0], parts[1], nil
}

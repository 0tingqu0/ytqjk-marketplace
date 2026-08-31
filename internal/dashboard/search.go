package dashboard

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func searchAll(root, query string, limit int) ([]rag.QueryResult, error) {
	type target struct {
		path  string
		scope string
	}
	indexes := []target{{path: filepath.Join(root, "global-cache", "index.json"), scope: "global"}}
	projects := filepath.Join(root, "projects")
	entries, _ := os.ReadDir(projects)
	for _, entry := range entries {
		if entry.IsDir() && safeIdentifier(entry.Name()) {
			indexes = append(indexes, target{path: filepath.Join(projects, entry.Name(), "index.json"), scope: "project:" + entry.Name()})
		}
	}
	var results []rag.QueryResult
	for _, current := range indexes {
		found, _, err := rag.SearchIndex(current.path, query, limit, current.scope)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		results = append(results, found...)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			if results[i].ID == results[j].ID {
				return results[i].Scope < results[j].Scope
			}
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func allChunks(root string, limit int) ([]rag.Chunk, error) {
	var result []rag.Chunk
	for _, path := range []string{filepath.Join(root, "global-cache", "index.json")} {
		var index rag.Index
		if err := safeio.ReadJSON(path, &index); err == nil {
			result = append(result, index.Chunks...)
		}
	}
	entries, _ := os.ReadDir(filepath.Join(root, "projects"))
	for _, entry := range entries {
		if !entry.IsDir() || !safeIdentifier(entry.Name()) {
			continue
		}
		var index rag.Index
		if err := safeio.ReadJSON(filepath.Join(root, "projects", entry.Name(), "index.json"), &index); err == nil {
			result = append(result, index.Chunks...)
		}
		if len(result) >= limit {
			break
		}
	}
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

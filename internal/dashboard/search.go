package dashboard

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func searchAll(root, query string, limit int) ([]rag.QueryResult, error) {
	var indexes []string
	indexes = append(indexes, filepath.Join(root, "global-cache", "index.json"))
	projects := filepath.Join(root, "projects")
	entries, _ := os.ReadDir(projects)
	for _, entry := range entries {
		if entry.IsDir() && safeIdentifier(entry.Name()) {
			indexes = append(indexes, filepath.Join(projects, entry.Name(), "index.json"))
		}
	}
	terms := tokenCounts(query)
	var results []rag.QueryResult
	for _, path := range indexes {
		var index rag.Index
		if err := safeio.ReadJSON(path, &index); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return nil, err
		}
		for _, chunk := range index.Chunks {
			score := simpleScore(terms, tokenCounts(chunk.Path+" "+chunk.Content))
			if score > 0 {
				results = append(results, rag.QueryResult{ID: chunk.ID, Path: chunk.Path, Start: chunk.Start, End: chunk.End, Content: chunk.Content, Score: score, Scope: index.ProjectID})
			}
		}
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
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

func tokenCounts(value string) map[string]int {
	result := map[string]int{}
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= '\u4e00' && r <= '\u9fff' || r == '_' || r == '-')
	}) {
		result[token]++
	}
	return result
}

func simpleScore(query, document map[string]int) float64 {
	matched := 0
	for term := range query {
		if document[term] > 0 {
			matched++
		}
	}
	if len(query) == 0 {
		return 0
	}
	return float64(matched) / float64(len(query))
}

package rag

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

func approvedPrefetchChunks(knowledgeRoot string) (map[string]Chunk, error) {
	var index Index
	if err := safeio.ReadJSON(filepath.Join(knowledgeRoot, "global-cache", "index.json"), &index); err != nil {
		return nil, err
	}
	if index.SchemaVersion != SchemaVersion || index.ProjectID != "global" {
		return nil, errors.New("global index schema is invalid")
	}
	result := make(map[string]Chunk, len(index.Chunks))
	for _, chunk := range index.Chunks {
		lineStart, lineEnd := normalizedChunkLines(chunk)
		if !validApprovedPrefetchChunk(chunk, lineStart, lineEnd) {
			continue
		}
		result[prefetchSourceKey(chunk.Path, lineStart, lineEnd)] = chunk
	}
	return result, nil
}

func validApprovedPrefetchChunk(chunk Chunk, lineStart, lineEnd int) bool {
	if !governedGlobalIndexPath(chunk.Path) || lineStart < 1 || lineEnd < lineStart ||
		strings.TrimSpace(chunk.Content) == "" || !utf8.ValidString(chunk.Content) ||
		securitycheck.IsSensitivePath(chunk.Path) || securitycheck.ContainsHighConfidenceSecret(chunk.Content) ||
		len(chunk.Digest) != 64 || safeio.SHA256([]byte(chunk.Content)) != chunk.Digest {
		return false
	}
	_, err := hex.DecodeString(chunk.Digest)
	return err == nil
}

func governedGlobalIndexPath(path string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(path)))
	if clean != path || filepath.IsAbs(filepath.FromSlash(path)) || strings.HasPrefix(path, "../") {
		return false
	}
	for _, prefix := range []string{"global/", "verified/", "personal-experience/approved/", "error-experience/approved/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func normalizedChunkLines(chunk Chunk) (int, int) {
	lineStart, lineEnd := chunk.LineStart, chunk.LineEnd
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd < lineStart {
		lineEnd = lineStart + strings.Count(chunk.Content, "\n")
	}
	return lineStart, lineEnd
}

func purgeInvalidPrefetch(transaction *sql.Tx, approved map[string]Chunk) error {
	rows, err := transaction.Query(
		"SELECT id, path, line_start, line_end, content, source_sha256, query, cached_at, last_accessed, hit_count, size_bytes FROM entries",
	)
	if err != nil {
		return err
	}
	stored, err := scanPrefetchRows(rows)
	rows.Close()
	if err != nil {
		return err
	}
	for _, entry := range stored {
		if currentPrefetchEntry(entry, approved) {
			continue
		}
		if _, err := transaction.Exec("DELETE FROM entries WHERE id = ?", entry.ID); err != nil {
			return err
		}
	}
	return nil
}

func currentPrefetchEntry(entry storedPrefetchEntry, approved map[string]Chunk) bool {
	chunk, found := approved[prefetchSourceKey(entry.Path, entry.LineStart, entry.LineEnd)]
	expectedSize := int64(len(entry.Path) + len(entry.Content) + len(entry.SourceSHA256))
	return found && entry.ID == prefetchEntryID(entry.Path, entry.LineStart, entry.LineEnd) &&
		entry.SourceSHA256 == chunk.Digest && entry.Content == chunk.Content &&
		entry.HitCount >= 0 && entry.SizeBytes == expectedSize
}

func prefetchEntryFromResult(result QueryResult, query, now string, approved map[string]Chunk) (storedPrefetchEntry, bool) {
	lineStart, lineEnd := result.LineStart, result.LineEnd
	if lineStart < 1 {
		lineStart = 1
	}
	if lineEnd < lineStart {
		lineEnd = lineStart + strings.Count(result.Content, "\n")
	}
	chunk, found := approved[prefetchSourceKey(result.Path, lineStart, lineEnd)]
	if !found || chunk.Digest != result.Digest || chunk.Content != result.Content {
		return storedPrefetchEntry{}, false
	}
	entry := storedPrefetchEntry{
		ID: prefetchEntryID(result.Path, lineStart, lineEnd), Path: result.Path,
		LineStart: lineStart, LineEnd: lineEnd, Content: result.Content, SourceSHA256: result.Digest,
		Query: strings.TrimSpace(query), CachedAt: now, LastAccessed: now, HitCount: 1,
	}
	entry.SizeBytes = int64(len(entry.Path) + len(entry.Content) + len(entry.SourceSHA256))
	return entry, true
}

func prefetchEntryFromPublic(row PrefetchEntry, now string, approved map[string]Chunk) (storedPrefetchEntry, bool) {
	entry := storedPrefetchEntry{
		ID: prefetchEntryID(row.Path, row.LineStart, row.LineEnd), Path: row.Path,
		LineStart: row.LineStart, LineEnd: row.LineEnd, Content: row.Content, SourceSHA256: row.SourceSHA256,
		Query: row.Query, CachedAt: row.CachedAt, LastAccessed: row.LastAccessed, HitCount: row.HitCount,
	}
	if entry.CachedAt == "" {
		entry.CachedAt = now
	}
	if entry.LastAccessed == "" {
		entry.LastAccessed = entry.CachedAt
	}
	if entry.HitCount < 1 {
		entry.HitCount = 1
	}
	entry.SizeBytes = int64(len(entry.Path) + len(entry.Content) + len(entry.SourceSHA256))
	return entry, currentPrefetchEntry(entry, approved)
}

func prefetchEntryID(path string, lineStart, lineEnd int) string {
	return safeio.SHA256([]byte(path + ":" + fmt.Sprint(lineStart) + ":" + fmt.Sprint(lineEnd)))
}

func prefetchSourceKey(path string, lineStart, lineEnd int) string {
	return path + "\x00" + fmt.Sprint(lineStart) + "\x00" + fmt.Sprint(lineEnd)
}

func scanPrefetchRows(rows *sql.Rows) ([]storedPrefetchEntry, error) {
	result := make([]storedPrefetchEntry, 0)
	for rows.Next() {
		var entry storedPrefetchEntry
		if err := rows.Scan(
			&entry.ID, &entry.Path, &entry.LineStart, &entry.LineEnd, &entry.Content,
			&entry.SourceSHA256, &entry.Query, &entry.CachedAt, &entry.LastAccessed,
			&entry.HitCount, &entry.SizeBytes,
		); err != nil {
			return nil, err
		}
		result = append(result, entry)
	}
	return result, rows.Err()
}

func publicPrefetchEntry(entry storedPrefetchEntry) PrefetchEntry {
	return PrefetchEntry{
		Path: entry.Path, LineStart: entry.LineStart, LineEnd: entry.LineEnd,
		Content: entry.Content, SourceSHA256: entry.SourceSHA256, Query: entry.Query,
		CachedAt: entry.CachedAt, LastAccessed: entry.LastAccessed,
		HitCount: entry.HitCount, Scope: "project-prefetch-cache",
	}
}

func prefetchQueryTerms(query string) []string {
	normalized := strings.TrimSpace(query)
	result := []string{normalized}
	seen := map[string]struct{}{strings.ToLower(normalized): {}}
	for _, term := range strings.Fields(normalized) {
		key := strings.ToLower(term)
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, term)
		if len(result) >= 8 {
			break
		}
	}
	return result
}

func prefetchCounts(queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}) (int, int64, error) {
	var entries int
	var used int64
	err := queryer.QueryRow("SELECT COUNT(*), COALESCE(SUM(size_bytes), 0) FROM entries").Scan(&entries, &used)
	return entries, used, err
}

func enforcePrefetchCapacity(transaction *sql.Tx, projectDirectory string) (bool, error) {
	entries, used, err := prefetchCounts(transaction)
	if err != nil || entries == 0 {
		return false, err
	}
	databasePath := filepath.Join(projectDirectory, "cache", prefetchDatabaseName)
	databaseSize := int64(0)
	if info, statErr := os.Stat(databasePath); statErr == nil {
		databaseSize = info.Size()
	}
	otherBytes := directorySize(projectDirectory) - databaseSize
	allowed := PrefetchCapacity - max(int64(0), otherBytes)
	if used <= allowed {
		return false, nil
	}
	rows, err := transaction.Query("SELECT id, size_bytes FROM entries ORDER BY hit_count ASC, last_accessed ASC, id ASC")
	if err != nil {
		return false, err
	}
	type victim struct {
		id   string
		size int64
	}
	victims := make([]victim, 0)
	for rows.Next() {
		var row victim
		if err := rows.Scan(&row.id, &row.size); err != nil {
			rows.Close()
			return false, err
		}
		victims = append(victims, row)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return false, err
	}
	for _, victim := range victims {
		if used <= allowed {
			break
		}
		if _, err := transaction.Exec("DELETE FROM entries WHERE id = ?", victim.id); err != nil {
			return false, err
		}
		used -= victim.size
	}
	return true, nil
}

func makePrefetchStats(projectDirectory string, entries int, used int64) PrefetchStats {
	projectUsed := directorySize(projectDirectory)
	return PrefetchStats{
		Entries: entries, UsedBytes: used, ProjectUsedBytes: projectUsed,
		CapacityBytes: PrefetchCapacity, CapacityExceeded: projectUsed > PrefetchCapacity,
		Policy: "LFU_LRU",
	}
}

func emptyPrefetchStats(projectDirectory string) PrefetchStats {
	return makePrefetchStats(projectDirectory, 0, 0)
}

func prefetchStatsMap(stats PrefetchStats, generation string) map[string]any {
	return map[string]any{
		"state": "READY", "generation": generation,
		"entries": stats.Entries, "used_bytes": stats.UsedBytes,
		"project_used_bytes": stats.ProjectUsedBytes,
		"capacity_bytes":     stats.CapacityBytes,
		"capacity_exceeded":  stats.CapacityExceeded,
		"policy":             stats.Policy,
	}
}

func directorySize(directory string) int64 {
	var total int64
	_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, path)
		if err != nil {
			return nil
		}
		first := strings.SplitN(filepath.ToSlash(relative), "/", 2)[0]
		if entry.IsDir() && (first == "handoffs" || first == "errors") {
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return nil
		}
		if info, err := entry.Info(); err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
}

func globalPrefetchGeneration(knowledgeRoot string) string {
	var manifest Manifest
	if safeio.ReadJSON(filepath.Join(knowledgeRoot, "global-cache", "manifest.json"), &manifest) != nil ||
		manifest.SchemaVersion != SchemaVersion || manifest.Identity.ID != "global" {
		return ""
	}
	return manifest.SourceFingerprint
}

package rag

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	prefetchDatabaseName = "global-knowledge.sqlite3"
	prefetchLegacyName   = "global-knowledge.json"
	PrefetchCapacity     = int64(1024 * 1024 * 1024)
)

type PrefetchEntry struct {
	Path         string `json:"path"`
	LineStart    int    `json:"line_start"`
	LineEnd      int    `json:"line_end"`
	Content      string `json:"content"`
	SourceSHA256 string `json:"source_sha256"`
	Query        string `json:"query"`
	CachedAt     string `json:"cached_at"`
	LastAccessed string `json:"last_accessed"`
	HitCount     int    `json:"hit_count"`
	Scope        string `json:"scope"`
}

type PrefetchStats struct {
	Entries          int    `json:"entries"`
	UsedBytes        int64  `json:"used_bytes"`
	ProjectUsedBytes int64  `json:"project_used_bytes"`
	CapacityBytes    int64  `json:"capacity_bytes"`
	CapacityExceeded bool   `json:"capacity_exceeded"`
	Policy           string `json:"policy"`
}

type storedPrefetchEntry struct {
	ID           string
	Path         string
	LineStart    int
	LineEnd      int
	Content      string
	SourceSHA256 string
	Query        string
	CachedAt     string
	LastAccessed string
	HitCount     int
	SizeBytes    int64
}

// QueryPrefetch searches the project-local copy of previously approved global
// hits. A changed global generation clears the rebuildable cache before it can
// be read, so stale or revoked material is never returned.
func QueryPrefetch(projectDirectory, knowledgeRoot, query, generation string, limit int) ([]QueryResult, PrefetchStats, error) {
	if strings.TrimSpace(query) == "" {
		return nil, emptyPrefetchStats(projectDirectory), errors.New("prefetch query is required")
	}
	if limit < 1 || limit > 20 {
		return nil, emptyPrefetchStats(projectDirectory), errors.New("prefetch limit must be 1..20")
	}
	approved, err := approvedPrefetchChunks(knowledgeRoot)
	if err != nil || generation == "" {
		return []QueryResult{}, emptyPrefetchStats(projectDirectory), nil
	}
	database, err := openPrefetchDatabase(projectDirectory, knowledgeRoot, true, approved)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	defer transaction.Rollback()
	if err := ensurePrefetchGeneration(transaction, generation); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	if err := purgeInvalidPrefetch(transaction, approved); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	terms := prefetchQueryTerms(query)
	where := make([]string, len(terms))
	arguments := make([]any, 0, len(terms)+1)
	for index, term := range terms {
		where[index] = "instr(lower(content), lower(?)) > 0"
		arguments = append(arguments, term)
	}
	arguments = append(arguments, limit)
	rows, err := transaction.Query(
		"SELECT id, path, line_start, line_end, content, source_sha256, query, cached_at, last_accessed, hit_count, size_bytes "+
			"FROM entries WHERE ("+strings.Join(where, " OR ")+") "+
			"ORDER BY hit_count DESC, last_accessed DESC, id ASC LIMIT ?",
		arguments...,
	)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	stored, err := scanPrefetchRows(rows)
	rows.Close()
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	now := nowText()
	results := make([]QueryResult, 0, len(stored))
	for _, entry := range stored {
		if _, err := transaction.Exec(
			"UPDATE entries SET hit_count = hit_count + 1, last_accessed = ? WHERE id = ?",
			now, entry.ID,
		); err != nil {
			return nil, emptyPrefetchStats(projectDirectory), err
		}
		results = append(results, QueryResult{
			ID: entry.ID, Path: entry.Path, Start: 0, End: utf8.RuneCountInString(entry.Content),
			LineStart: entry.LineStart, LineEnd: entry.LineEnd, Content: entry.Content,
			Score: 1, LexicalScore: 1, Mode: "PREFETCH", Scope: "project-prefetch-cache",
			Digest: entry.SourceSHA256,
		})
	}
	entries, used, err := prefetchCounts(transaction)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	if err := transaction.Commit(); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	return results, makePrefetchStats(projectDirectory, entries, used), nil
}

// UpdatePrefetch records current, approved global results for a project. Rows
// are revalidated against the current global index rather than trusted from the
// caller, preserving the approval boundary.
func UpdatePrefetch(projectDirectory, knowledgeRoot, query, generation string, results []QueryResult) (PrefetchStats, error) {
	approved, err := approvedPrefetchChunks(knowledgeRoot)
	if err != nil || generation == "" {
		return emptyPrefetchStats(projectDirectory), err
	}
	database, err := openPrefetchDatabase(projectDirectory, knowledgeRoot, true, approved)
	if err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	defer transaction.Rollback()
	if err := ensurePrefetchGeneration(transaction, generation); err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	if err := purgeInvalidPrefetch(transaction, approved); err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	now := nowText()
	for _, result := range results {
		entry, valid := prefetchEntryFromResult(result, query, now, approved)
		if !valid {
			continue
		}
		if _, err := transaction.Exec(
			"INSERT INTO entries (id, path, line_start, line_end, content, source_sha256, query, cached_at, last_accessed, hit_count, size_bytes) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) "+
				"ON CONFLICT(id) DO UPDATE SET content=excluded.content, source_sha256=excluded.source_sha256, query=excluded.query, "+
				"cached_at=excluded.cached_at, last_accessed=excluded.last_accessed, hit_count=entries.hit_count + 1, size_bytes=excluded.size_bytes",
			entry.ID, entry.Path, entry.LineStart, entry.LineEnd, entry.Content, entry.SourceSHA256,
			entry.Query, entry.CachedAt, entry.LastAccessed, entry.HitCount, entry.SizeBytes,
		); err != nil {
			return emptyPrefetchStats(projectDirectory), err
		}
	}
	evicted, err := enforcePrefetchCapacity(transaction, projectDirectory)
	if err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	entries, used, err := prefetchCounts(transaction)
	if err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	if err := transaction.Commit(); err != nil {
		return emptyPrefetchStats(projectDirectory), err
	}
	if evicted {
		_, _ = database.Exec("VACUUM")
	}
	stats := makePrefetchStats(projectDirectory, entries, used)
	if stats.CapacityExceeded {
		return stats, errors.New("project cache exceeds 1 GiB after prefetch eviction")
	}
	return stats, nil
}

// ListPrefetch returns cache rows for the Dashboard without incrementing usage.
func ListPrefetch(projectDirectory, knowledgeRoot string, limit int) ([]PrefetchEntry, PrefetchStats, error) {
	limit = max(1, min(limit, 500))
	databasePath := filepath.Join(projectDirectory, "cache", prefetchDatabaseName)
	legacyPath := filepath.Join(projectDirectory, "cache", prefetchLegacyName)
	if _, databaseErr := os.Lstat(databasePath); errors.Is(databaseErr, os.ErrNotExist) {
		if _, legacyErr := os.Lstat(legacyPath); errors.Is(legacyErr, os.ErrNotExist) {
			return []PrefetchEntry{}, emptyPrefetchStats(projectDirectory), nil
		}
	}
	approved, approvedErr := approvedPrefetchChunks(knowledgeRoot)
	if approvedErr != nil {
		approved = map[string]Chunk{}
	}
	database, err := openPrefetchDatabase(projectDirectory, knowledgeRoot, true, approved)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	defer database.Close()
	transaction, err := database.Begin()
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	defer transaction.Rollback()
	generation := globalPrefetchGeneration(knowledgeRoot)
	if generation == "" {
		if _, err := transaction.Exec("DELETE FROM entries"); err != nil {
			return nil, emptyPrefetchStats(projectDirectory), err
		}
	} else if err := ensurePrefetchGeneration(transaction, generation); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	if err := purgeInvalidPrefetch(transaction, approved); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	rows, err := transaction.Query(
		"SELECT id, path, line_start, line_end, content, source_sha256, query, cached_at, last_accessed, hit_count, size_bytes "+
			"FROM entries ORDER BY hit_count DESC, last_accessed DESC, id ASC LIMIT ?", limit,
	)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	stored, err := scanPrefetchRows(rows)
	rows.Close()
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	entries, used, err := prefetchCounts(transaction)
	if err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	if err := transaction.Commit(); err != nil {
		return nil, emptyPrefetchStats(projectDirectory), err
	}
	result := make([]PrefetchEntry, 0, len(stored))
	for _, entry := range stored {
		result = append(result, publicPrefetchEntry(entry))
	}
	return result, makePrefetchStats(projectDirectory, entries, used), nil
}

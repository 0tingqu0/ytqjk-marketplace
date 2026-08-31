package rag

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
	_ "modernc.org/sqlite"
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

func openPrefetchDatabase(projectDirectory, knowledgeRoot string, create bool, approved map[string]Chunk) (*sql.DB, error) {
	projectInfo, err := os.Lstat(projectDirectory)
	if err != nil || !projectInfo.IsDir() || projectInfo.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("project cache root is not a regular directory")
	}
	cacheDirectory := filepath.Join(projectDirectory, "cache")
	info, err := os.Lstat(cacheDirectory)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(cacheDirectory, 0o700); err != nil {
			return nil, err
		}
		info, err = os.Lstat(cacheDirectory)
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("project cache path is not a regular directory")
	}
	databasePath := filepath.Join(cacheDirectory, prefetchDatabaseName)
	if databaseInfo, databaseErr := os.Lstat(databasePath); databaseErr == nil {
		if !databaseInfo.Mode().IsRegular() || databaseInfo.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("project prefetch database is not a regular file")
		}
	} else if !errors.Is(databaseErr, os.ErrNotExist) {
		return nil, databaseErr
	}
	absolute, err := filepath.Abs(databasePath)
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(absolute)+"?_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	if err := initializePrefetchDatabase(
		database, filepath.Join(cacheDirectory, prefetchLegacyName), approved,
		globalPrefetchGeneration(knowledgeRoot),
	); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

func initializePrefetchDatabase(database *sql.DB, legacyPath string, approved map[string]Chunk, generation string) error {
	statements := []string{
		"CREATE TABLE IF NOT EXISTS entries (id TEXT PRIMARY KEY, path TEXT NOT NULL, line_start INTEGER NOT NULL, line_end INTEGER NOT NULL, content TEXT NOT NULL, source_sha256 TEXT NOT NULL, query TEXT NOT NULL, cached_at TEXT NOT NULL, last_accessed TEXT NOT NULL, hit_count INTEGER NOT NULL, size_bytes INTEGER NOT NULL)",
		"CREATE TABLE IF NOT EXISTS metadata (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			return err
		}
	}
	columns, err := prefetchColumns(database)
	if err != nil {
		return err
	}
	for _, required := range []string{"id", "path", "line_start", "line_end", "content"} {
		if !columns[required] {
			return fmt.Errorf("legacy prefetch database is missing %s", required)
		}
	}
	additions := map[string]string{
		"source_sha256": "TEXT NOT NULL DEFAULT ''",
		"query":         "TEXT NOT NULL DEFAULT ''",
		"cached_at":     "TEXT NOT NULL DEFAULT ''",
		"last_accessed": "TEXT NOT NULL DEFAULT ''",
		"hit_count":     "INTEGER NOT NULL DEFAULT 0",
		"size_bytes":    "INTEGER NOT NULL DEFAULT 0",
	}
	names := make([]string, 0, len(additions))
	for name := range additions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !columns[name] {
			if _, err := database.Exec("ALTER TABLE entries ADD COLUMN " + name + " " + additions[name]); err != nil {
				return err
			}
		}
	}
	if _, err := database.Exec("CREATE INDEX IF NOT EXISTS entries_usage ON entries(hit_count, last_accessed)"); err != nil {
		return err
	}
	return migrateLegacyPrefetch(database, legacyPath, approved, generation)
}

func prefetchColumns(database *sql.DB) (map[string]bool, error) {
	rows, err := database.Query("PRAGMA table_info(entries)")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]bool{}
	for rows.Next() {
		var sequence int
		var name, dataType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&sequence, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		result[name] = true
	}
	return result, rows.Err()
}

func migrateLegacyPrefetch(database *sql.DB, legacyPath string, approved map[string]Chunk, generation string) error {
	var migrated string
	err := database.QueryRow("SELECT value FROM metadata WHERE key = 'legacy_migrated'").Scan(&migrated)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var legacy struct {
		Entries []PrefetchEntry `json:"entries"`
	}
	if legacyInfo, legacyErr := os.Lstat(legacyPath); legacyErr == nil && legacyInfo.Mode().IsRegular() && legacyInfo.Mode()&os.ModeSymlink == 0 {
		_ = safeio.ReadJSON(legacyPath, &legacy)
	}
	now := nowText()
	transaction, err := database.Begin()
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	for _, row := range legacy.Entries {
		stored, valid := prefetchEntryFromPublic(row, now, approved)
		if !valid {
			continue
		}
		if _, err := transaction.Exec(
			"INSERT OR IGNORE INTO entries (id, path, line_start, line_end, content, source_sha256, query, cached_at, last_accessed, hit_count, size_bytes) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			stored.ID, stored.Path, stored.LineStart, stored.LineEnd, stored.Content, stored.SourceSHA256,
			stored.Query, stored.CachedAt, stored.LastAccessed, stored.HitCount, stored.SizeBytes,
		); err != nil {
			return err
		}
	}
	if _, err := transaction.Exec("INSERT OR REPLACE INTO metadata(key, value) VALUES ('legacy_migrated', '1')"); err != nil {
		return err
	}
	if generation != "" {
		if _, err := transaction.Exec("INSERT OR IGNORE INTO metadata(key, value) VALUES ('global_generation', ?)", generation); err != nil {
			return err
		}
	}
	return transaction.Commit()
}

func ensurePrefetchGeneration(transaction *sql.Tx, generation string) error {
	var current string
	err := transaction.QueryRow("SELECT value FROM metadata WHERE key = 'global_generation'").Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || current != generation {
		if _, err := transaction.Exec("DELETE FROM entries"); err != nil {
			return err
		}
		if _, err := transaction.Exec("INSERT OR REPLACE INTO metadata(key, value) VALUES ('global_generation', ?)", generation); err != nil {
			return err
		}
	}
	return nil
}

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

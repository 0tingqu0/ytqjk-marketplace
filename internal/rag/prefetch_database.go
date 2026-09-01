package rag

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	_ "modernc.org/sqlite"
)

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

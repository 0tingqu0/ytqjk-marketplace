package knowledge

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const LatestSchema = 4

func openDatabase(path string) (*sql.DB, error) {
	database, _, err := openDatabaseWithFeedbackRoute(path)
	return database, err
}

func openDatabaseWithFeedbackRoute(path string) (*sql.DB, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, "", err
	}
	dsn, err := sqliteFileURI(absolute, url.Values{
		"_pragma": {"foreign_keys(1)", "busy_timeout(15000)", "journal_mode(WAL)"},
		"_txlock": {"immediate"},
	})
	if err != nil {
		return nil, "", err
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, "", err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.Ping(); err != nil {
		return nil, "", errors.Join(err, database.Close())
	}
	feedbackJobs, err := migrate(database)
	if err != nil {
		return nil, "", errors.Join(err, closeDatabase(database))
	}
	return database, feedbackJobs, nil
}

func closeDatabase(database *sql.DB) error {
	var journalMode string
	modeErr := database.QueryRow("PRAGMA journal_mode=DELETE").Scan(&journalMode)
	if modeErr == nil && !strings.EqualFold(journalMode, "delete") {
		modeErr = fmt.Errorf("unexpected SQLite journal mode %q", journalMode)
	}
	return errors.Join(modeErr, database.Close())
}

func tableColumns(database *sql.DB, table string) (map[string]bool, error) {
	if !validTable(table) {
		return nil, errors.New("unsupported table")
	}
	rows, err := database.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, err
		}
		columns[name] = true
	}
	return columns, rows.Err()
}

func validTable(table string) bool {
	for _, value := range []string{
		"projects", "originals", "documents", "versions", "chunks", "sources", "governance", "audit", "jobs",
		"snapshots", "snapshot_versions", "active_snapshots", "import_documents", "import_provenance", "import_receipts",
		"feedback_jobs", "feedback_events", "global_sync",
	} {
		if table == value {
			return true
		}
	}
	return false
}

func splitStatements(source string) []string {
	// Trigger bodies contain semicolons. Split only after END; or ordinary
	// statement terminators, retaining compact migration source.
	var result []string
	var current strings.Builder
	inTrigger := false
	for _, line := range strings.Split(source, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TRIGGER") {
			inTrigger = true
		}
		current.WriteString(line)
		current.WriteByte('\n')
		if (!inTrigger && strings.HasSuffix(trimmed, ";")) || (inTrigger && strings.HasSuffix(strings.ToUpper(trimmed), "END;")) {
			statement := strings.TrimSpace(current.String())
			statement = strings.TrimSuffix(statement, ";")
			result = append(result, statement)
			current.Reset()
			inTrigger = false
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		result = append(result, strings.TrimSpace(current.String()))
	}
	return result
}

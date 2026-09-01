package library

import (
	"path/filepath"
	"testing"
)

func TestVersionZeroIncompatiblePreviewSchemaRollsBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	database := openRawLibraryDatabase(t, path)
	if _, err := database.Exec(`
		CREATE TABLE library_previews (
			digest TEXT NOT NULL,
			action TEXT NOT NULL,
			payload_json BLOB NOT NULL,
			base_revision INTEGER NOT NULL,
			base_digest TEXT NOT NULL,
			target_digest TEXT NOT NULL,
			preview_json BLOB NOT NULL,
			state TEXT NOT NULL,
			created_at TEXT NOT NULL,
			consumed_revision INTEGER
		);
		INSERT INTO library_previews(
			digest, action, payload_json, base_revision, base_digest,
			target_digest, preview_json, state, created_at
		) VALUES ('sentinel', 'attach', '{}', 0, '', '', '{}', 'ACTIVE', '2026-08-31T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := OpenStore(path, testNodes(), 0)
	assertServerCode(t, err, "LIBRARY_STORE_CORRUPT")

	database = openRawLibraryDatabase(t, path)
	var version, stateTables, generatedIndexes, sentinelRows int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'library_state'`).Scan(&stateTables); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'index' AND name = 'library_previews_base_revision'`).Scan(&generatedIndexes); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`
		SELECT COUNT(*) FROM library_previews WHERE digest = 'sentinel'`).Scan(&sentinelRows); err != nil {
		t.Fatal(err)
	}
	if version != 0 || stateTables != 0 || generatedIndexes != 0 || sentinelRows != 1 {
		t.Fatalf(
			"failed bootstrap leaked: version=%d state=%d index=%d sentinel=%d",
			version, stateTables, generatedIndexes, sentinelRows,
		)
	}
}

func TestStoreRejectsWrongPreviewIndexShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.sqlite3")
	store := openTestStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	database := openRawLibraryDatabase(t, path)
	if _, err := database.Exec(`
		DROP INDEX library_previews_base_revision;
		CREATE INDEX library_previews_base_revision
			ON library_previews(state, base_revision)`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	_, err := OpenStore(path, nil, 0)
	assertServerCode(t, err, "LIBRARY_STORE_CORRUPT")
}

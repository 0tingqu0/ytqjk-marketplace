package tree

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ database *sql.DB }

func OpenStore(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(absolute)+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(4)
	store := &Store{database: database}
	if _, err := database.Exec(storeSchema); err != nil {
		database.Close()
		return nil, err
	}
	encoded, _ := json.Marshal(Default().Snapshot())
	if _, err := database.Exec("INSERT OR IGNORE INTO knowledge_tree_state(singleton,revision,document,updated_at) VALUES (1,0,?,?)", string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error { return s.database.Close() }

func (s *Store) Load(ctx context.Context) (*Tree, error) {
	var revision int64
	var document string
	if err := s.database.QueryRowContext(ctx, "SELECT revision,document FROM knowledge_tree_state WHERE singleton=1").Scan(&revision, &document); err != nil {
		return nil, err
	}
	var snapshot Snapshot
	if err := json.Unmarshal([]byte(document), &snapshot); err != nil || snapshot.Revision != revision {
		return nil, errors.New("knowledge tree storage is corrupt")
	}
	return FromSnapshot(snapshot)
}

func (s *Store) Save(ctx context.Context, value *Tree, expectedRevision int64) error {
	if value == nil || value.Revision() != expectedRevision+1 {
		return ErrRevisionConflict
	}
	encoded, err := json.Marshal(value.Snapshot())
	if err != nil {
		return err
	}
	tx, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current int64
	if err := tx.QueryRowContext(ctx, "SELECT revision FROM knowledge_tree_state WHERE singleton=1").Scan(&current); err != nil {
		return err
	}
	if current != expectedRevision {
		return ErrRevisionConflict
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := tx.ExecContext(ctx, "UPDATE knowledge_tree_state SET revision=?,document=?,updated_at=? WHERE singleton=1 AND revision=?", value.Revision(), string(encoded), now, expectedRevision)
	if err != nil {
		return err
	}
	rows, _ := result.RowsAffected()
	if rows != 1 {
		return ErrRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_tree_events(revision,document,created_at) VALUES (?,?,?)", value.Revision(), string(encoded), now); err != nil {
		return err
	}
	var readback string
	if err := tx.QueryRowContext(ctx, "SELECT document FROM knowledge_tree_state WHERE singleton=1").Scan(&readback); err != nil || readback != string(encoded) {
		return errors.New("knowledge tree transactional readback failed")
	}
	return tx.Commit()
}

func (s *Store) BootstrapProjects(ctx context.Context, projects []Node) (*Tree, error) {
	value, err := s.Load(ctx)
	if err != nil {
		return nil, err
	}
	base := value.Revision()
	changed := false
	for _, project := range projects {
		if _, exists := value.nodes[project.NodeID]; exists {
			continue
		}
		if project.Kind != "project" {
			return nil, errors.New("bootstrap accepts project nodes only")
		}
		if err := value.AddNode(project, "global"); err != nil {
			return nil, err
		}
		changed = true
	}
	if !changed {
		return value, nil
	}
	if err := value.IncrementRevision(base); err != nil {
		return nil, err
	}
	if err := s.Save(ctx, value, base); err != nil {
		return nil, err
	}
	return value, nil
}

const storeSchema = `
CREATE TABLE IF NOT EXISTS knowledge_tree_state (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1), revision INTEGER NOT NULL CHECK(revision>=0),
 document TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS knowledge_tree_events (
 revision INTEGER PRIMARY KEY, document TEXT NOT NULL, created_at TEXT NOT NULL
);
CREATE TRIGGER IF NOT EXISTS knowledge_tree_events_no_update BEFORE UPDATE ON knowledge_tree_events
BEGIN SELECT RAISE(ABORT,'knowledge tree events are append-only'); END;
CREATE TRIGGER IF NOT EXISTS knowledge_tree_events_no_delete BEFORE DELETE ON knowledge_tree_events
BEGIN SELECT RAISE(ABORT,'knowledge tree events are append-only'); END;
`

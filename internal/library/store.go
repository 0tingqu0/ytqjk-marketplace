package library

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Store persists the canonical Library configuration and mutation previews.
type Store struct {
	database *sql.DB
	now      func() time.Time
}

// OpenStore opens a local SQLite authority and initializes it exactly once.
func OpenStore(path string, initialNodes []Node, initialRevision int64) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	dsn := "file:" + url.PathEscape(filepath.ToSlash(absolute)) +
		"?_pragma=foreign_keys(1)&_pragma=busy_timeout(15000)&_txlock=immediate"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	database.SetMaxOpenConns(4)
	store := &Store{database: database, now: time.Now}
	if err := store.initialize(initialNodes, initialRevision); err != nil {
		database.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the SQLite connection pool.
func (s *Store) Close() error {
	return s.database.Close()
}

// Snapshot returns persisted configuration with caller-supplied runtime stats.
func (s *Store) Snapshot(statistics map[string]Statistics) (Snapshot, error) {
	revision, _, nodes, err := loadState(s.database.QueryRow)
	if err != nil {
		return Snapshot{}, err
	}
	for index := range nodes {
		nodes[index].Stats = statistics[nodes[index].ID]
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return Snapshot{}, fmt.Errorf("validate stored library tree: %w", err)
	}
	return tree.Snapshot()
}

// Preview validates and durably binds a mutation without changing the tree.
func (s *Store) Preview(action string, payload []byte) (MutationPreview, error) {
	transaction, err := s.database.BeginTx(context.Background(), nil)
	if err != nil {
		return MutationPreview{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	defer transaction.Rollback()
	revision, baseDigest, nodes, err := loadState(transaction.QueryRow)
	if err != nil {
		return MutationPreview{}, err
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return MutationPreview{}, fmt.Errorf("validate stored library tree: %w", err)
	}
	planned, err := planMutation(tree, action, payload)
	if err != nil {
		return MutationPreview{}, err
	}
	targetDigest := planned.targetDigest
	response := mutationPreview(action, planned)
	canonicalPayload, err := marshalMutationPayload(payload)
	if err != nil {
		return MutationPreview{}, err
	}
	previewJSON, err := json.Marshal(response)
	if err != nil {
		return MutationPreview{}, fmt.Errorf("encode library preview: %w", err)
	}
	now := s.now().UTC()
	if err := prunePreviews(transaction, now); err != nil {
		return MutationPreview{}, err
	}
	_, err = transaction.Exec(`
		INSERT INTO library_previews(
			digest, action, payload_json, base_revision, base_digest,
			target_digest, preview_json, state, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, 'ACTIVE', ?)
		ON CONFLICT(digest) DO NOTHING`,
		response.Digest, action, canonicalPayload, revision, baseDigest,
		targetDigest, previewJSON, now.Format(time.RFC3339Nano),
	)
	if err != nil {
		return MutationPreview{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	if err := trimPreviews(transaction, response.Digest); err != nil {
		return MutationPreview{}, err
	}
	stored, err := loadMatchingPreview(transaction, response, canonicalPayload)
	if err != nil {
		return MutationPreview{}, err
	}
	if err := transaction.Commit(); err != nil {
		return MutationPreview{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return stored, nil
}

// Commit consumes a persisted preview under a SQLite revision-and-digest CAS.
func (s *Store) Commit(action, digest string, expectedRevision int64) (Snapshot, error) {
	if !validDigest(digest) {
		return Snapshot{}, contractError("INVALID_PREVIEW_DIGEST")
	}
	if expectedRevision < 0 {
		return Snapshot{}, contractError("INVALID_EXPECTED_REVISION")
	}
	transaction, err := s.database.BeginTx(context.Background(), nil)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	defer transaction.Rollback()

	record, err := loadPreviewRecord(transaction, digest)
	if err != nil {
		return Snapshot{}, err
	}
	expired, err := record.expired(s.now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	if expired {
		return Snapshot{}, contractError("PREVIEW_EXPIRED")
	}
	if record.State == "CONSUMED" {
		return Snapshot{}, contractError("PREVIEW_REPLAYED")
	}
	if record.Action != action {
		return Snapshot{}, contractError("PREVIEW_ACTION_MISMATCH")
	}
	if record.BaseRevision != expectedRevision {
		return Snapshot{}, contractError("PREVIEW_MISMATCH")
	}
	revision, stateDigest, nodes, err := loadState(transaction.QueryRow)
	if err != nil {
		return Snapshot{}, err
	}
	if revision != expectedRevision {
		return Snapshot{}, contractError("REVISION_CONFLICT")
	}
	if stateDigest != record.BaseDigest {
		return Snapshot{}, contractError("PREVIEW_MISMATCH")
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return Snapshot{}, fmt.Errorf("validate stored library tree: %w", err)
	}
	planned, err := planMutation(tree, action, record.Payload)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if planned.PreviewDigest != digest || planned.targetDigest != record.TargetDigest {
		return Snapshot{}, contractError("PREVIEW_MISMATCH")
	}
	if !record.matchesPlanned(action, planned) {
		return Snapshot{}, storeError("LIBRARY_STORE_CORRUPT", fmt.Errorf("stored preview summary mismatch"))
	}
	if err := commitMutation(tree, action, record.Payload, planned, revision); err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		return Snapshot{}, err
	}
	nodesJSON, err := configurationJSON(snapshot.Nodes)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := transaction.Exec(`
		UPDATE library_state
		SET revision = ?, digest = ?, nodes_json = ?, updated_at = ?
		WHERE singleton = 1 AND revision = ? AND digest = ?`,
		snapshot.Revision, snapshot.Digest, nodesJSON,
		s.now().UTC().Format(time.RFC3339Nano), revision, stateDigest,
	)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return Snapshot{}, contractError("REVISION_CONFLICT")
	}
	result, err = transaction.Exec(`
		UPDATE library_previews
		SET state = 'CONSUMED', consumed_revision = ?
		WHERE digest = ? AND state = 'ACTIVE'`, snapshot.Revision, digest)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	consumed, err := result.RowsAffected()
	if err != nil || consumed != 1 {
		return Snapshot{}, contractError("PREVIEW_REPLAYED")
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return snapshot, nil
}

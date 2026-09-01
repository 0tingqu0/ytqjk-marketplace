package knowledge

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
)

func appendVersion(tx *sql.Tx, documentID, state, content, source, sourceKind, now string) (int64, error) {
	digest := sha256.Sum256([]byte(content))
	digestText := hex.EncodeToString(digest[:])
	if _, err := tx.Exec("INSERT OR IGNORE INTO originals(sha256, content, created_at) VALUES (?, ?, ?)", digestText, []byte(content), now); err != nil {
		return 0, err
	}
	var ordinal int
	if err := tx.QueryRow("SELECT COALESCE(MAX(ordinal),0)+1 FROM versions WHERE document_id=?", documentID).Scan(&ordinal); err != nil {
		return 0, err
	}
	result, err := tx.Exec("INSERT INTO versions(document_id, ordinal, state, original_sha256, created_at) VALUES (?, ?, ?, ?, ?)", documentID, ordinal, state, digestText, now)
	if err != nil {
		return 0, err
	}
	versionID, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	chunkDigest := sha256.Sum256([]byte(fmt.Sprintf("%d:1:%s", versionID, digestText)))
	if _, err := tx.Exec("INSERT INTO chunks(id, version_id, ordinal, content) VALUES (?, ?, 1, ?)", hex.EncodeToString(chunkDigest[:]), versionID, content); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO sources(version_id, kind, locator) VALUES (?, ?, ?)", versionID, sourceKind, source); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO governance(version_id, action, actor, created_at) VALUES (?, ?, ?, ?)", versionID, state, "一听曲就困", now); err != nil {
		return 0, err
	}
	return versionID, nil
}

func requireEditable(tx *sql.Tx, documentID string) error {
	var mirrored int
	if err := tx.QueryRow("SELECT COUNT(*) FROM global_sync WHERE global_document_id=?", documentID).Scan(&mirrored); err != nil {
		return err
	}
	if mirrored != 0 {
		return errors.New("system-managed global mirrors are immutable")
	}
	var deleted sql.NullString
	var state string
	err := tx.QueryRow(`SELECT d.deleted_at, v.state FROM documents d JOIN versions v ON v.document_id=d.id
WHERE d.id=? ORDER BY v.ordinal DESC LIMIT 1`, documentID).Scan(&deleted, &state)
	if err != nil || deleted.Valid || state != "candidate" {
		return errors.New("only active candidate revisions are editable")
	}
	return nil
}

func appendState(tx *sql.Tx, payload map[string]any, now string) error {
	documentID := payload["document_id"].(string)
	state := payload["state"].(string)
	var mirrored int
	if err := tx.QueryRow("SELECT COUNT(*) FROM global_sync WHERE global_document_id=?", documentID).Scan(&mirrored); err != nil {
		return err
	}
	if mirrored != 0 {
		return errors.New("system-managed global mirrors are immutable")
	}
	var prior, digest string
	if err := tx.QueryRow("SELECT state, original_sha256 FROM versions WHERE document_id=? ORDER BY ordinal DESC LIMIT 1", documentID).Scan(&prior, &digest); err != nil {
		return err
	}
	allowed := map[string]map[string]bool{
		"candidate": {"candidate": true, "approved": true, "verified": true, "tombstone": true},
		"approved":  {"verified": true, "tombstone": true}, "verified": {"tombstone": true},
	}
	if !allowed[prior][state] {
		return errors.New("invalid governance state transition")
	}
	content := ""
	if value, ok := payload["content"].(string); ok && value != "" {
		content = value
	} else if err := tx.QueryRow("SELECT CAST(content AS TEXT) FROM originals WHERE sha256=?", digest).Scan(&content); err != nil {
		return err
	}
	if _, err := appendVersion(tx, documentID, state, content, "governance", "local", now); err != nil {
		return err
	}
	return audit(tx, "version_"+state, documentID, now)
}

func createSnapshot(tx *sql.Tx, projectID, snapshotID, now string) error {
	var generation int
	if err := tx.QueryRow("SELECT COALESCE(MAX(generation),0)+1 FROM snapshots WHERE project_id=?", projectID).Scan(&generation); err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO snapshots(id, project_id, generation, state, created_at) VALUES (?, ?, ?, 'BUILDING', ?)", snapshotID, projectID, generation, now); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO snapshot_versions(snapshot_id, document_id, version_id)
SELECT ?, d.id, v.id FROM documents d JOIN versions v ON v.document_id=d.id
WHERE d.project_id=? AND d.deleted_at IS NULL
AND v.ordinal=(SELECT MAX(latest.ordinal) FROM versions latest WHERE latest.document_id=d.id)
AND v.state!='tombstone'`, snapshotID, projectID); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE snapshots SET state='ACTIVE' WHERE id=?", snapshotID); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO active_snapshots(project_id, snapshot_id) VALUES (?, ?)
ON CONFLICT(project_id) DO UPDATE SET snapshot_id=excluded.snapshot_id`, projectID, snapshotID); err != nil {
		return err
	}
	return audit(tx, "snapshot_activated", snapshotID, now)
}

func audit(tx *sql.Tx, event, subjectID, now string) error {
	_, err := tx.Exec("INSERT INTO audit(event, subject_id, created_at, detail) VALUES (?, ?, ?, '{}')", event, subjectID, now)
	return err
}

package orchestration

import (
	"database/sql"
	"encoding/json"
)

func appendAudit(tx *sql.Tx, kind, runID, sessionKey, leaseID string, detail map[string]any, timestamp int64) error {
	encoded, _ := json.Marshal(detail)
	_, err := tx.Exec(
		"INSERT INTO audit_events(created_at,kind,run_id,session_key,lease_id,detail) VALUES (?,?,?,?,?,?)",
		timestamp,
		kind,
		runID,
		sessionKey,
		nullable(leaseID),
		string(encoded),
	)
	return err
}

func nullable(value string) any {
	if value == "" {
		return nil
	}
	return value
}

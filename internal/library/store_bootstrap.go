package library

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

func (s *Store) initialize(initialNodes []Node, initialRevision int64) error {
	transaction, err := s.database.Begin()
	if err != nil {
		return storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	defer transaction.Rollback()
	var version int
	if err := transaction.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if version > librarySchemaVersion {
		return storeError("LIBRARY_SCHEMA_TOO_NEW", fmt.Errorf("schema version %d", version))
	}
	if version < 0 {
		return storeError("LIBRARY_STORE_CORRUPT", fmt.Errorf("negative schema version %d", version))
	}
	if version == 0 {
		if _, err := transaction.Exec(librarySchema); err != nil {
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
		if err := validateStoreSchema(transaction); err != nil {
			return err
		}
		if err := ensureInitialState(transaction, initialNodes, initialRevision, s.now()); err != nil {
			return err
		}
		if _, err := transaction.Exec(fmt.Sprintf("PRAGMA user_version = %d", librarySchemaVersion)); err != nil {
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
	} else if err := validateStoreSchema(transaction); err != nil {
		return err
	}
	if _, _, _, err := loadState(transaction.QueryRow); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return nil
}

func ensureInitialState(transaction *sql.Tx, nodes []Node, revision int64, now time.Time) error {
	var count int
	if err := transaction.QueryRow("SELECT COUNT(*) FROM library_state").Scan(&count); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if count > 1 {
		return storeError("LIBRARY_STORE_CORRUPT", fmt.Errorf("multiple library state rows"))
	}
	if count == 1 {
		return nil
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return storeError("LIBRARY_SEED_CONFLICT", err)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		return storeError("LIBRARY_SEED_CONFLICT", err)
	}
	nodesJSON, err := configurationJSON(snapshot.Nodes)
	if err != nil {
		return storeError("LIBRARY_SEED_CONFLICT", err)
	}
	if _, err := transaction.Exec(`
		INSERT INTO library_state(singleton, revision, digest, nodes_json, updated_at)
		VALUES (1, ?, ?, ?, ?)`, snapshot.Revision, snapshot.Digest, nodesJSON,
		now.UTC().Format(time.RFC3339Nano)); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	return nil
}

func validateStoreSchema(transaction *sql.Tx) error {
	for _, query := range []string{
		"SELECT singleton, revision, digest, nodes_json, updated_at FROM library_state LIMIT 0",
		`SELECT digest, action, payload_json, base_revision, base_digest,
			target_digest, preview_json, state, created_at, consumed_revision
			FROM library_previews LIMIT 0`,
	} {
		rows, err := transaction.Query(query)
		if err != nil {
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
		if err := rows.Close(); err != nil {
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
	}
	if err := validateSingleColumnPrimaryKey(
		transaction, "PRAGMA table_info(library_state)", "singleton",
	); err != nil {
		return err
	}
	if err := validateSingleColumnPrimaryKey(
		transaction, "PRAGMA table_info(library_previews)", "digest",
	); err != nil {
		return err
	}
	if err := validatePreviewIndex(transaction); err != nil {
		return err
	}
	return nil
}

func validateSingleColumnPrimaryKey(transaction *sql.Tx, query string, expected string) error {
	rows, err := transaction.Query(query)
	if err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	primaryKeys := make([]string, 0, 1)
	for rows.Next() {
		var columnID, notNull, primaryKey int
		var name, dataType string
		var defaultValue sql.NullString
		if err := rows.Scan(
			&columnID, &name, &dataType, &notNull, &defaultValue, &primaryKey,
		); err != nil {
			rows.Close()
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
		if primaryKey > 0 {
			primaryKeys = append(primaryKeys, name)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if err := rows.Close(); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if len(primaryKeys) != 1 || primaryKeys[0] != expected {
		return storeError(
			"LIBRARY_STORE_CORRUPT",
			fmt.Errorf("expected primary key %s, found %v", expected, primaryKeys),
		)
	}
	return nil
}

func validatePreviewIndex(transaction *sql.Tx) error {
	rows, err := transaction.Query("PRAGMA index_list(library_previews)")
	if err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	found := false
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			rows.Close()
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
		if name == "library_previews_base_revision" {
			found = unique == 0 && partial == 0
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if err := rows.Close(); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if !found {
		return storeError("LIBRARY_STORE_CORRUPT", fmt.Errorf("invalid preview revision index"))
	}
	rows, err = transaction.Query("PRAGMA index_info(library_previews_base_revision)")
	if err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	columns := make([]string, 0, 2)
	for rows.Next() {
		var sequence, columnID int
		var name string
		if err := rows.Scan(&sequence, &columnID, &name); err != nil {
			rows.Close()
			return storeError("LIBRARY_STORE_CORRUPT", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if err := rows.Close(); err != nil {
		return storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if len(columns) != 2 || columns[0] != "base_revision" || columns[1] != "state" {
		return storeError(
			"LIBRARY_STORE_CORRUPT",
			fmt.Errorf("invalid preview revision index columns %v", columns),
		)
	}
	return nil
}

type queryRow func(string, ...any) *sql.Row

func loadState(query queryRow) (int64, string, []Node, error) {
	var revision int64
	var digest string
	var nodesJSON []byte
	if err := query(`SELECT revision, digest, nodes_json FROM library_state WHERE singleton = 1`).Scan(
		&revision, &digest, &nodesJSON,
	); err != nil {
		if err == sql.ErrNoRows {
			return 0, "", nil, storeError("LIBRARY_STORE_CORRUPT", err)
		}
		return 0, "", nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	var nodes []Node
	if err := decodeStrictJSON(nodesJSON, &nodes); err != nil {
		return 0, "", nil, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return 0, "", nil, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		return 0, "", nil, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if snapshot.Digest != digest {
		return 0, "", nil, storeError("LIBRARY_STORE_CORRUPT", fmt.Errorf("state digest mismatch"))
	}
	return revision, digest, nodes, nil
}

func configurationJSON(nodes []Node) ([]byte, error) {
	configured := make([]Node, len(nodes))
	for index, node := range nodes {
		configured[index] = cloneNode(node)
		configured[index].Stats = Statistics{}
	}
	result, err := json.Marshal(configured)
	if err != nil {
		return nil, fmt.Errorf("encode library configuration: %w", err)
	}
	return result, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

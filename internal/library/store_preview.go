package library

import (
	"bytes"
	"database/sql"
	"errors"
	"reflect"
	"sort"
	"time"
)

const (
	previewTTL          = 15 * time.Minute
	maxPersistedPreview = 256
)

type previewRecord struct {
	Action       string
	Payload      []byte
	BaseRevision int64
	BaseDigest   string
	TargetDigest string
	State        string
	CreatedAt    string
	Preview      MutationPreview
}

type rowQueryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func loadPreviewRecord(queryer rowQueryer, digest string) (previewRecord, error) {
	var record previewRecord
	var previewJSON []byte
	err := queryer.QueryRow(`
		SELECT action, payload_json, base_revision, base_digest, target_digest,
			state, created_at, preview_json
		FROM library_previews WHERE digest = ?`, digest).Scan(
		&record.Action, &record.Payload, &record.BaseRevision,
		&record.BaseDigest, &record.TargetDigest, &record.State, &record.CreatedAt, &previewJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return previewRecord{}, contractError("PREVIEW_NOT_FOUND")
	}
	if err != nil {
		return previewRecord{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	if err := decodeStrictJSON(previewJSON, &record.Preview); err != nil {
		return previewRecord{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if record.Preview.Digest != digest || record.Preview.Action != record.Action ||
		record.Preview.ExpectedRevision != record.BaseRevision {
		return previewRecord{}, storeError("LIBRARY_STORE_CORRUPT", errors.New("preview binding mismatch"))
	}
	return record, nil
}

func (r previewRecord) expired(now time.Time) (bool, error) {
	created, err := time.Parse(time.RFC3339Nano, r.CreatedAt)
	if err != nil {
		return false, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	return !now.Before(created.Add(previewTTL)), nil
}

func (r previewRecord) matchesPlanned(action string, planned Preview) bool {
	return reflect.DeepEqual(r.Preview, mutationPreview(action, planned))
}

func loadMatchingPreview(queryer rowQueryer, expected MutationPreview, payload []byte) (MutationPreview, error) {
	var action string
	var storedPayload []byte
	var state string
	var previewJSON []byte
	var createdAt string
	err := queryer.QueryRow(`
		SELECT action, payload_json, state, preview_json, created_at
		FROM library_previews WHERE digest = ?`, expected.Digest).Scan(
		&action, &storedPayload, &state, &previewJSON, &createdAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return MutationPreview{}, storeError("LIBRARY_STORE_CORRUPT", err)
		}
		return MutationPreview{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	if action != expected.Action || !bytes.Equal(storedPayload, payload) {
		return MutationPreview{}, storeError("LIBRARY_STORE_CORRUPT", errors.New("preview digest collision"))
	}
	if state != "ACTIVE" {
		return MutationPreview{}, contractError("PREVIEW_REPLAYED")
	}
	if _, err := time.Parse(time.RFC3339Nano, createdAt); err != nil {
		return MutationPreview{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	var stored MutationPreview
	if err := decodeStrictJSON(previewJSON, &stored); err != nil {
		return MutationPreview{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if !reflect.DeepEqual(stored, expected) {
		return MutationPreview{}, storeError("LIBRARY_STORE_CORRUPT", errors.New("preview record mismatch"))
	}
	return stored, nil
}

func prunePreviews(transaction *sql.Tx, now time.Time) error {
	records, err := readPreviewTimestamps(transaction)
	if err != nil {
		return err
	}
	for _, record := range records {
		if now.Before(record.Created.Add(previewTTL)) {
			continue
		}
		if _, err := transaction.Exec("DELETE FROM library_previews WHERE digest = ?", record.Digest); err != nil {
			return storeError("LIBRARY_STORE_UNAVAILABLE", err)
		}
	}
	return nil
}

func trimPreviews(transaction *sql.Tx, retainedDigest string) error {
	records, err := readPreviewTimestamps(transaction)
	if err != nil {
		return err
	}
	if len(records) <= maxPersistedPreview {
		return nil
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Created.Equal(records[j].Created) {
			return records[i].Digest < records[j].Digest
		}
		return records[i].Created.Before(records[j].Created)
	})
	remaining := len(records) - maxPersistedPreview
	for _, record := range records {
		if remaining == 0 {
			break
		}
		if record.Digest == retainedDigest {
			continue
		}
		if _, err := transaction.Exec("DELETE FROM library_previews WHERE digest = ?", record.Digest); err != nil {
			return storeError("LIBRARY_STORE_UNAVAILABLE", err)
		}
		remaining--
	}
	return nil
}

type previewTimestamp struct {
	Digest  string
	Created time.Time
}

func readPreviewTimestamps(transaction *sql.Tx) ([]previewTimestamp, error) {
	rows, err := transaction.Query("SELECT digest, created_at FROM library_previews")
	if err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	defer rows.Close()
	result := make([]previewTimestamp, 0)
	for rows.Next() {
		var digest, createdAt string
		if err := rows.Scan(&digest, &createdAt); err != nil {
			return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
		}
		created, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, storeError("LIBRARY_STORE_CORRUPT", err)
		}
		if !validDigest(digest) {
			return nil, storeError("LIBRARY_STORE_CORRUPT", errors.New("invalid preview digest"))
		}
		result = append(result, previewTimestamp{Digest: digest, Created: created})
	}
	if err := rows.Err(); err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return result, nil
}

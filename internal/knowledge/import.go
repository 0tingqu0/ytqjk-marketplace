package knowledge

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type CandidateImport struct {
	Title      string `json:"title"`
	Content    string `json:"content"`
	SourceKind string `json:"source_kind"`
	SourceRef  string `json:"source_ref"`
	SourceSHA  string `json:"source_sha256"`
}

type ImportReceipt struct {
	Marker                string `json:"marker"`
	ProjectID             string `json:"project_id"`
	Status                string `json:"status"`
	InputCount            int    `json:"input_count"`
	CreatedDocuments      int    `json:"created_documents"`
	DeduplicatedDocuments int    `json:"deduplicated_documents"`
	ProvenanceAdded       int    `json:"provenance_added"`
	ChunksCreated         int    `json:"chunks_created"`
	SchemaVersion         int    `json:"schema_version"`
	ReceiptSHA256         string `json:"receipt_sha256"`
}

type importReceiptPayload struct {
	Marker                string `json:"marker"`
	ProjectID             string `json:"project_id"`
	Status                string `json:"status"`
	InputCount            int    `json:"input_count"`
	CreatedDocuments      int    `json:"created_documents"`
	DeduplicatedDocuments int    `json:"deduplicated_documents"`
	ProvenanceAdded       int    `json:"provenance_added"`
	ChunksCreated         int    `json:"chunks_created"`
	SchemaVersion         int    `json:"schema_version"`
}

type rowQuerier interface {
	QueryRow(query string, args ...any) *sql.Row
}

func (s *Service) ImportCandidates(scope, alias, marker string, candidates []CandidateImport, force bool) (ImportReceipt, error) {
	if err := validateText(scope, 256); err != nil {
		return ImportReceipt{}, err
	}
	if err := validateText(alias, 1024); err != nil {
		return ImportReceipt{}, err
	}
	if err := validateText(marker, 4096); err != nil {
		return ImportReceipt{}, err
	}
	for _, candidate := range candidates {
		if err := validateText(candidate.Title, 4096); err != nil {
			return ImportReceipt{}, err
		}
		if err := validateText(candidate.Content, 16*1024*1024); err != nil {
			return ImportReceipt{}, err
		}
		if err := validateText(candidate.SourceKind, 256); err != nil {
			return ImportReceipt{}, errors.New("candidate provenance is invalid")
		}
		if err := validateText(candidate.SourceRef, 16384); err != nil || !sha256Pattern.MatchString(candidate.SourceSHA) {
			return ImportReceipt{}, errors.New("candidate provenance is invalid")
		}
	}
	s.writer.Lock()
	defer s.writer.Unlock()
	tx, err := s.database.Begin()
	if err != nil {
		return ImportReceipt{}, err
	}
	defer tx.Rollback()
	if prior, found, err := readImportReceipt(tx, marker); err != nil {
		return ImportReceipt{}, err
	} else if found {
		var priorScope, priorAlias string
		if err := tx.QueryRow("SELECT scope,alias FROM projects WHERE id=?", prior.ProjectID).Scan(&priorScope, &priorAlias); err != nil {
			return ImportReceipt{}, errors.New("import receipt project binding is invalid")
		}
		if priorScope != strings.TrimSpace(scope) || priorAlias != strings.TrimSpace(alias) {
			return ImportReceipt{}, errors.New("import marker belongs to another project")
		}
		if !force {
			prior.Status = "SKIPPED"
			checksum, checksumErr := importReceiptChecksum(prior)
			if checksumErr != nil {
				return ImportReceipt{}, checksumErr
			}
			prior.ReceiptSHA256 = checksum
			return prior, nil
		}
	}
	projectID := ""
	if err := tx.QueryRow("SELECT id FROM projects WHERE scope=? AND alias=?", scope, alias).Scan(&projectID); errors.Is(err, sql.ErrNoRows) {
		projectID, err = newUUID()
		if err != nil {
			return ImportReceipt{}, err
		}
		if _, err := tx.Exec("INSERT INTO projects(id, name, scope, alias, created_at) VALUES (?, ?, ?, ?, ?)", projectID, alias, scope, alias, timestamp()); err != nil {
			return ImportReceipt{}, err
		}
		if err := audit(tx, "project_created", projectID, timestamp()); err != nil {
			return ImportReceipt{}, err
		}
	} else if err != nil {
		return ImportReceipt{}, err
	}
	receipt := ImportReceipt{Marker: marker, ProjectID: projectID, Status: "IMPORTED", InputCount: len(candidates), SchemaVersion: LatestSchema}
	for _, candidate := range candidates {
		contentDigest := sha256.Sum256([]byte(candidate.Content))
		contentSHA := hex.EncodeToString(contentDigest[:])
		documentID := ""
		var versionID int64
		var currentState string
		created := false
		err := tx.QueryRow(`SELECT d.id,v.id,v.state FROM documents d JOIN versions v ON v.document_id=d.id
WHERE d.project_id=? AND d.deleted_at IS NULL AND v.original_sha256=? AND v.state!='tombstone'
AND v.ordinal=(SELECT MAX(latest.ordinal) FROM versions latest WHERE latest.document_id=d.id)
ORDER BY d.id LIMIT 1`, projectID, contentSHA).Scan(&documentID, &versionID, &currentState)
		if errors.Is(err, sql.ErrNoRows) {
			if _, err := tx.Exec("DELETE FROM import_documents WHERE project_id=? AND content_sha256=?", projectID, contentSHA); err != nil {
				return ImportReceipt{}, err
			}
			documentID, err = newUUID()
			if err != nil {
				return ImportReceipt{}, err
			}
			if _, err := tx.Exec("INSERT INTO documents(id, project_id, title) VALUES (?, ?, ?)", documentID, projectID, candidate.Title); err != nil {
				return ImportReceipt{}, err
			}
			versionID, err = appendVersion(tx, documentID, "candidate", candidate.Content, candidate.SourceRef, candidate.SourceKind, timestamp())
			if err != nil {
				return ImportReceipt{}, err
			}
			if _, err := tx.Exec("INSERT INTO import_documents(project_id, content_sha256, document_id, version_id) VALUES (?, ?, ?, ?)", projectID, contentSHA, documentID, versionID); err != nil {
				return ImportReceipt{}, err
			}
			receipt.CreatedDocuments++
			receipt.ChunksCreated++
			created = true
			currentState = "candidate"
		} else if err != nil {
			return ImportReceipt{}, err
		} else {
			if _, err := tx.Exec("DELETE FROM import_documents WHERE project_id=? AND (content_sha256=? OR document_id=?)", projectID, contentSHA, documentID); err != nil {
				return ImportReceipt{}, err
			}
			if _, err := tx.Exec("INSERT INTO import_documents(project_id,content_sha256,document_id,version_id) VALUES (?,?,?,?)", projectID, contentSHA, documentID, versionID); err != nil {
				return ImportReceipt{}, err
			}
			receipt.DeduplicatedDocuments++
		}
		provenanceChanged, err := upsertImportProvenance(tx, documentID, candidate)
		if err != nil {
			return ImportReceipt{}, err
		}
		if provenanceChanged {
			receipt.ProvenanceAdded++
			if !created && currentState == "candidate" {
				if _, err := tx.Exec("INSERT INTO sources(version_id,kind,locator) VALUES (?,?,?)", versionID, candidate.SourceKind, candidate.SourceRef); err != nil {
					return ImportReceipt{}, err
				}
			}
		}
	}
	checksum, err := importReceiptChecksum(receipt)
	if err != nil {
		return ImportReceipt{}, err
	}
	receipt.ReceiptSHA256 = checksum
	encoded, err := importReceiptJSON(receipt)
	if err != nil {
		return ImportReceipt{}, err
	}
	if _, err := tx.Exec(`INSERT INTO import_receipts(marker,project_id,receipt,receipt_sha256,completed_at)
VALUES (?,?,?,?,?) ON CONFLICT(marker) DO UPDATE SET project_id=excluded.project_id,
receipt=excluded.receipt,receipt_sha256=excluded.receipt_sha256,completed_at=excluded.completed_at`,
		marker, projectID, string(encoded), receipt.ReceiptSHA256, timestamp()); err != nil {
		return ImportReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return ImportReceipt{}, err
	}
	return receipt, nil
}

func upsertImportProvenance(tx *sql.Tx, documentID string, candidate CandidateImport) (bool, error) {
	var sourceSHA, scanner, scanState, governanceState string
	err := tx.QueryRow(`SELECT source_sha256,scanner,scan_state,governance_state FROM import_provenance
WHERE document_id=? AND source_kind=? AND source_ref=?`, documentID, candidate.SourceKind, candidate.SourceRef).
		Scan(&sourceSHA, &scanner, &scanState, &governanceState)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec(`INSERT INTO import_provenance(document_id,source_kind,source_ref,
source_sha256,scanner,scan_state,governance_state) VALUES (?,?,?,?,'go-sha256-v1','CLEAN','CANDIDATE')`,
			documentID, candidate.SourceKind, candidate.SourceRef, candidate.SourceSHA)
		return err == nil, err
	}
	if err != nil {
		return false, err
	}
	if sourceSHA == candidate.SourceSHA && scanner == "go-sha256-v1" && scanState == "CLEAN" && governanceState == "CANDIDATE" {
		return false, nil
	}
	_, err = tx.Exec(`UPDATE import_provenance SET source_sha256=?,scanner='go-sha256-v1',
scan_state='CLEAN',governance_state='CANDIDATE' WHERE document_id=? AND source_kind=? AND source_ref=?`,
		candidate.SourceSHA, documentID, candidate.SourceKind, candidate.SourceRef)
	return err == nil, err
}

func (s *Service) ImportReceipt(marker string) (ImportReceipt, bool, error) {
	return readImportReceipt(s.database, marker)
}

func readImportReceipt(query rowQuerier, marker string) (ImportReceipt, bool, error) {
	var projectID, encoded, checksum string
	err := query.QueryRow("SELECT project_id,receipt,receipt_sha256 FROM import_receipts WHERE marker=?", marker).Scan(&projectID, &encoded, &checksum)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportReceipt{}, false, nil
	}
	if err != nil {
		return ImportReceipt{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(encoded)))
	decoder.DisallowUnknownFields()
	var payload importReceiptPayload
	if err := decoder.Decode(&payload); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ImportReceipt{}, false, errors.New("import receipt is invalid")
	}
	receipt := ImportReceipt{
		Marker: payload.Marker, ProjectID: payload.ProjectID, Status: payload.Status,
		InputCount: payload.InputCount, CreatedDocuments: payload.CreatedDocuments,
		DeduplicatedDocuments: payload.DeduplicatedDocuments, ProvenanceAdded: payload.ProvenanceAdded,
		ChunksCreated: payload.ChunksCreated, SchemaVersion: payload.SchemaVersion,
	}
	if err := validateImportReceipt(receipt, marker, projectID); err != nil {
		return ImportReceipt{}, false, errors.New("import receipt is invalid")
	}
	expected, err := importReceiptChecksum(receipt)
	if err != nil || !sha256Pattern.MatchString(checksum) || subtle.ConstantTimeCompare([]byte(expected), []byte(checksum)) != 1 {
		return ImportReceipt{}, false, errors.New("import receipt is invalid")
	}
	receipt.ReceiptSHA256 = checksum
	return receipt, true, nil
}

func validateImportReceipt(receipt ImportReceipt, marker, projectID string) error {
	if receipt.Marker != marker || receipt.ProjectID != projectID || !validUUID(projectID) {
		return errors.New("receipt binding is invalid")
	}
	if receipt.Status != "IMPORTED" || (receipt.SchemaVersion != 3 && receipt.SchemaVersion != 4) {
		return errors.New("receipt schema is invalid")
	}
	values := []int{receipt.InputCount, receipt.CreatedDocuments, receipt.DeduplicatedDocuments, receipt.ProvenanceAdded, receipt.ChunksCreated}
	for _, value := range values {
		if value < 0 {
			return errors.New("receipt counter is invalid")
		}
	}
	if receipt.InputCount != receipt.CreatedDocuments+receipt.DeduplicatedDocuments || receipt.ProvenanceAdded > receipt.InputCount {
		return errors.New("receipt counters are inconsistent")
	}
	return nil
}

func importReceiptJSON(receipt ImportReceipt) ([]byte, error) {
	return json.Marshal(map[string]any{
		"marker": receipt.Marker, "project_id": receipt.ProjectID, "status": receipt.Status,
		"input_count": receipt.InputCount, "created_documents": receipt.CreatedDocuments,
		"deduplicated_documents": receipt.DeduplicatedDocuments, "provenance_added": receipt.ProvenanceAdded,
		"chunks_created": receipt.ChunksCreated, "schema_version": receipt.SchemaVersion,
	})
}

func importReceiptChecksum(receipt ImportReceipt) (string, error) {
	encoded, err := importReceiptJSON(receipt)
	if err != nil {
		return "", err
	}
	return hexDigest(encoded), nil
}

func hexDigest(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

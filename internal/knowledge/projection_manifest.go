package knowledge

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"time"
)

const (
	ProjectionManifestSchema = "ytqjk.sqlite-projection-manifest/v1"
	ProjectionReceiptSchema  = "ytqjk.sqlite-projection-receipt/v1"

	ProjectionInvalidRequest = "INVALID_PROJECTION_REQUEST"
	ProjectionPathEscape     = "PROJECTION_PATH_ESCAPE"
	ProjectionSourceBusy     = "SOURCE_NOT_QUIESCENT"
	ProjectionSourceChanged  = "SOURCE_CHANGED"
	ProjectionSourceInvalid  = "SOURCE_INTEGRITY_FAILED"
	ProjectionInvalid        = "PROJECTION_INTEGRITY_FAILED"
	ProjectionConflict       = "PROJECTION_CONFLICT"
	ProjectionIncomplete     = "PROJECTION_INCOMPLETE"
	ProjectionIOFailed       = "PROJECTION_IO_FAILED"
)

var (
	projectionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	projectionHash      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type ProjectionRequest struct {
	KnowledgeRoot  string
	SourcePath     string
	ProjectionRoot string
	OperationID    string
}

type VerifyProjectionRequest struct {
	KnowledgeRoot string
	ManifestPath  string
}

type ProjectionSource struct {
	RelativePath         string `json:"relative_path"`
	SizeBytesBefore      int64  `json:"size_bytes_before"`
	SizeBytesAfter       int64  `json:"size_bytes_after"`
	SHA256Before         string `json:"sha256_before"`
	SHA256After          string `json:"sha256_after"`
	UserVersion          int    `json:"user_version"`
	PageCount            int    `json:"page_count"`
	IntegrityCheck       string `json:"integrity_check"`
	ForeignKeyViolations int    `json:"foreign_key_violations"`
	LiveLeases           int    `json:"live_leases"`
	WALState             string `json:"wal_state"`
	SHMState             string `json:"shm_state"`
}

type ProjectionArtifact struct {
	RelativePath         string `json:"relative_path"`
	SizeBytes            int64  `json:"size_bytes"`
	SHA256               string `json:"sha256"`
	UserVersion          int    `json:"user_version"`
	PageCount            int    `json:"page_count"`
	IntegrityCheck       string `json:"integrity_check"`
	ForeignKeyViolations int    `json:"foreign_key_violations"`
}

type ProjectionManifest struct {
	Schema          string             `json:"schema"`
	OperationID     string             `json:"operation_id"`
	CreatedAt       string             `json:"created_at"`
	Source          ProjectionSource   `json:"source"`
	Projection      ProjectionArtifact `json:"projection"`
	SourceUnchanged bool               `json:"source_unchanged"`
}

type ProjectionReceipt struct {
	Schema               string `json:"schema"`
	OperationID          string `json:"operation_id"`
	Status               string `json:"status"`
	ManifestRelativePath string `json:"manifest_relative_path"`
	ManifestSHA256       string `json:"manifest_sha256"`
	ProjectionSHA256     string `json:"projection_sha256"`
	SourceUnchanged      bool   `json:"source_unchanged"`
	VerifiedAt           string `json:"verified_at"`
}

type ProjectionError struct {
	Code string
	Err  error
}

func (e *ProjectionError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return fmt.Sprintf("%s: %v", e.Code, e.Err)
}

func (e *ProjectionError) Unwrap() error { return e.Err }

func projectionFailure(code string, err error) error {
	return &ProjectionError{Code: code, Err: err}
}

func readProjectionManifest(path string) (ProjectionManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectionManifest{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest ProjectionManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ProjectionManifest{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProjectionManifest{}, errors.New("manifest contains trailing data")
	}
	if err := validateProjectionManifest(manifest); err != nil {
		return ProjectionManifest{}, err
	}
	return manifest, nil
}

func readProjectionReceipt(path string) (ProjectionReceipt, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProjectionReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var receipt ProjectionReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return ProjectionReceipt{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProjectionReceipt{}, errors.New("receipt contains trailing data")
	}
	if err := validateProjectionReceipt(receipt); err != nil {
		return ProjectionReceipt{}, err
	}
	return receipt, nil
}

func validateProjectionManifest(manifest ProjectionManifest) error {
	if _, err := time.Parse(time.RFC3339Nano, manifest.CreatedAt); err != nil {
		return errors.New("projection manifest has invalid creation time")
	}
	if manifest.Schema != ProjectionManifestSchema ||
		!projectionIDPattern.MatchString(manifest.OperationID) || manifest.CreatedAt == "" ||
		!manifest.SourceUnchanged || manifest.Source.RelativePath == "" ||
		manifest.Source.SizeBytesBefore <= 0 || manifest.Source.SizeBytesAfter <= 0 ||
		manifest.Source.SizeBytesBefore != manifest.Source.SizeBytesAfter ||
		manifest.Source.SHA256Before != manifest.Source.SHA256After ||
		!projectionHash.MatchString(manifest.Source.SHA256Before) ||
		manifest.Source.UserVersion != LatestSchema || manifest.Source.PageCount <= 0 ||
		manifest.Source.IntegrityCheck != "ok" || manifest.Source.ForeignKeyViolations != 0 ||
		manifest.Source.LiveLeases != 0 || manifest.Source.WALState != "ABSENT" ||
		manifest.Source.SHMState != "ABSENT" || manifest.Projection.RelativePath != projectionDatabaseName ||
		manifest.Projection.SizeBytes <= 0 || !projectionHash.MatchString(manifest.Projection.SHA256) ||
		manifest.Projection.UserVersion != LatestSchema ||
		manifest.Projection.PageCount != manifest.Source.PageCount ||
		manifest.Projection.IntegrityCheck != "ok" || manifest.Projection.ForeignKeyViolations != 0 {
		return errors.New("projection manifest is invalid")
	}
	return nil
}

func validateProjectionReceipt(receipt ProjectionReceipt) error {
	if _, err := time.Parse(time.RFC3339Nano, receipt.VerifiedAt); err != nil {
		return errors.New("projection receipt has invalid verification time")
	}
	if receipt.Schema != ProjectionReceiptSchema ||
		!projectionIDPattern.MatchString(receipt.OperationID) || receipt.Status != "VERIFIED" ||
		receipt.ManifestRelativePath == "" || !projectionHash.MatchString(receipt.ManifestSHA256) ||
		!projectionHash.MatchString(receipt.ProjectionSHA256) || !receipt.SourceUnchanged {
		return errors.New("projection receipt is invalid")
	}
	return nil
}

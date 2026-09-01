package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	sqlite "modernc.org/sqlite"
)

const projectionBackupPages int32 = 256

type projectionBackuper interface {
	NewBackup(string) (*sqlite.Backup, error)
}

func verifyProjectionAt(ctx context.Context, knowledgeRoot, manifestPath string) (ProjectionReceipt, ProjectionManifest, error) {
	manifestPath, err := safeio.Contained(knowledgeRoot, manifestPath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, fmt.Errorf("projection manifest path: %w", err))
	}
	if err := requireProjectionDirectory(filepath.Dir(manifestPath)); err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	manifest, err := readProjectionManifest(manifestPath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, err)
	}
	manifestSHA, err := safeio.FileSHA256(manifestPath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionIOFailed, err)
	}
	receiptPath, err := containedManifestPath(filepath.Dir(manifestPath), projectionReceiptName)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, err)
	}
	receipt, err := readProjectionReceipt(receiptPath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, err)
	}
	if err := verifyProjectionReceipt(knowledgeRoot, manifestPath, manifestSHA, manifest, receipt); err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	sourcePath, err := containedManifestPath(knowledgeRoot, manifest.Source.RelativePath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, fmt.Errorf("projection source path: %w", err))
	}
	projectionPath, err := containedManifestPath(filepath.Dir(manifestPath), manifest.Projection.RelativePath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, fmt.Errorf("projection artifact path: %w", err))
	}
	if err := requireSQLiteSidecarsAbsent(sourcePath); err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	if err := requireProjectionSQLiteSidecarsAbsent(projectionPath); err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	sourceIdentity, err := projectionFileFingerprint(sourcePath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionSourceChanged, err)
	}
	if sourceIdentity.size != manifest.Source.SizeBytesAfter || sourceIdentity.sha256 != manifest.Source.SHA256After {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionSourceChanged, errors.New("source no longer matches projection manifest"))
	}
	sourceDatabase, err := openProjectionDatabase(sourcePath, true)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionSourceInvalid, err)
	}
	sourceState, inspectErr := inspectProjectionDatabase(ctx, sourceDatabase)
	closeErr := sourceDatabase.Close()
	if inspectErr != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(
			ProjectionSourceInvalid, errors.Join(inspectErr, closeErr),
		)
	}
	if closeErr != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionIOFailed, closeErr)
	}
	if err := requireValidSourceState(sourceState); err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	if !sourceStateMatches(manifest.Source, sourceState) {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionSourceChanged, errors.New("source database state differs from manifest"))
	}
	projectionIdentity, projectionState, err := inspectProjectionFile(ctx, projectionPath)
	if err != nil {
		return ProjectionReceipt{}, ProjectionManifest{}, err
	}
	if !projectionStateMatches(manifest.Projection, projectionIdentity, projectionState) {
		return ProjectionReceipt{}, ProjectionManifest{}, projectionFailure(ProjectionInvalid, errors.New("projection bytes or database state differ from manifest"))
	}
	return receipt, manifest, nil
}

func verifyProjectionReceipt(
	knowledgeRoot string,
	manifestPath string,
	manifestSHA string,
	manifest ProjectionManifest,
	receipt ProjectionReceipt,
) error {
	expectedRelative, err := filepath.Rel(knowledgeRoot, manifestPath)
	if err != nil {
		return projectionFailure(ProjectionInvalid, fmt.Errorf("projection receipt manifest path: %w", err))
	}
	expectedRelative = filepath.ToSlash(expectedRelative)
	if receipt.ManifestRelativePath != expectedRelative {
		return projectionFailure(ProjectionInvalid, errors.New("projection receipt manifest path is not canonical"))
	}
	receiptManifest, err := containedManifestPath(knowledgeRoot, receipt.ManifestRelativePath)
	if err != nil {
		return projectionFailure(ProjectionInvalid, fmt.Errorf("projection receipt manifest path: %w", err))
	}
	if receiptManifest != manifestPath ||
		receipt.OperationID != manifest.OperationID || receipt.ManifestSHA256 != manifestSHA ||
		receipt.ProjectionSHA256 != manifest.Projection.SHA256 ||
		receipt.SourceUnchanged != manifest.SourceUnchanged || receipt.VerifiedAt != manifest.CreatedAt {
		return projectionFailure(ProjectionInvalid, errors.New("projection receipt does not match manifest"))
	}
	return nil
}

func openProjectionDatabase(path string, immutable bool) (*sql.DB, error) {
	query := url.Values{
		"mode":    {"ro"},
		"_pragma": {"foreign_keys(1)", "busy_timeout(15000)"},
	}
	if immutable {
		query.Set("immutable", "1")
	}
	dsn, err := sqliteFileURI(path, query)
	if err != nil {
		return nil, err
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func backupProjectionDatabase(ctx context.Context, source *sql.DB, destination string) (resultErr error) {
	destinationURI, err := sqliteFileURI(destination, url.Values{"mode": {"rwc"}})
	if err != nil {
		return fmt.Errorf("create SQLite projection: %w", err)
	}
	connection, err := source.Conn(ctx)
	if err != nil {
		return fmt.Errorf("create SQLite projection: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, connection.Close())
	}()
	err = connection.Raw(func(driverConnection any) error {
		backuper, ok := driverConnection.(projectionBackuper)
		if !ok {
			return errors.New("SQLite driver does not support online backup")
		}
		backup, backupErr := backuper.NewBackup(destinationURI)
		if backupErr != nil {
			return backupErr
		}
		for {
			if contextErr := ctx.Err(); contextErr != nil {
				return errors.Join(contextErr, backup.Finish())
			}
			more, stepErr := backup.Step(projectionBackupPages)
			if stepErr != nil {
				return errors.Join(stepErr, backup.Finish())
			}
			if !more {
				return backup.Finish()
			}
		}
	})
	if err != nil {
		return fmt.Errorf("create SQLite projection: %w", err)
	}
	return nil
}

func inspectProjectionDatabase(ctx context.Context, database *sql.DB) (projectionDatabaseState, error) {
	var state projectionDatabaseState
	if err := database.QueryRowContext(ctx, "PRAGMA user_version").Scan(&state.userVersion); err != nil {
		return state, err
	}
	if err := database.QueryRowContext(ctx, "PRAGMA page_count").Scan(&state.pageCount); err != nil {
		return state, err
	}
	if err := database.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&state.integrityCheck); err != nil {
		return state, err
	}
	if err := validateProjectionSchema(ctx, database); err != nil {
		return state, err
	}
	rows, err := database.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var table, parent string
		var rowID, foreignKeyID any
		if err := rows.Scan(&table, &rowID, &parent, &foreignKeyID); err != nil {
			_ = rows.Close()
			return state, err
		}
		state.foreignKeyViolations++
	}
	if err := rows.Close(); err != nil {
		return state, err
	}
	if err := rows.Err(); err != nil {
		return state, err
	}
	for _, table := range []string{"jobs", "feedback_jobs"} {
		count, err := runningProjectionLeases(ctx, database, table)
		if err != nil {
			return state, err
		}
		state.liveLeases += count
	}
	return state, nil
}

func runningProjectionLeases(ctx context.Context, database *sql.DB, table string) (int, error) {
	var exists int
	if err := database.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&exists); err != nil || exists == 0 {
		return 0, err
	}
	query := ""
	switch table {
	case "jobs":
		query = "SELECT COUNT(*) FROM jobs WHERE state='RUNNING'"
	case "feedback_jobs":
		query = "SELECT COUNT(*) FROM feedback_jobs WHERE state='RUNNING'"
	default:
		return 0, errors.New("unsupported lease table")
	}
	var count int
	err := database.QueryRowContext(ctx, query).Scan(&count)
	return count, err
}

func inspectProjectionFile(ctx context.Context, path string) (projectionFileIdentity, projectionDatabaseState, error) {
	identity, err := projectionFileFingerprint(path)
	if err != nil {
		return identity, projectionDatabaseState{}, projectionFailure(ProjectionInvalid, err)
	}
	database, err := openProjectionDatabase(path, true)
	if err != nil {
		return identity, projectionDatabaseState{}, projectionFailure(ProjectionInvalid, err)
	}
	state, inspectErr := inspectProjectionDatabase(ctx, database)
	closeErr := database.Close()
	if inspectErr != nil {
		return identity, state, projectionFailure(ProjectionInvalid, errors.Join(inspectErr, closeErr))
	}
	if closeErr != nil {
		return identity, state, projectionFailure(ProjectionIOFailed, closeErr)
	}
	if state.userVersion != LatestSchema || state.pageCount <= 0 || state.integrityCheck != "ok" ||
		state.foreignKeyViolations != 0 || state.liveLeases != 0 {
		return identity, state, projectionFailure(ProjectionInvalid, errors.New("projection database validation failed"))
	}
	return identity, state, nil
}

func requireValidSourceState(state projectionDatabaseState) error {
	if state.userVersion != LatestSchema || state.pageCount <= 0 ||
		state.integrityCheck != "ok" || state.foreignKeyViolations != 0 {
		return projectionFailure(ProjectionSourceInvalid, errors.New("source database validation failed"))
	}
	if state.liveLeases != 0 {
		return projectionFailure(ProjectionSourceBusy, fmt.Errorf("source has %d RUNNING leases", state.liveLeases))
	}
	return nil
}

func projectionFileFingerprint(path string) (projectionFileIdentity, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return projectionFileIdentity{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return projectionFileIdentity{}, errors.New("SQLite path is not a regular file")
	}
	digest, err := safeio.FileSHA256(path)
	if err != nil {
		return projectionFileIdentity{}, err
	}
	return projectionFileIdentity{size: info.Size(), sha256: digest}, nil
}

func requireSQLiteSidecarsAbsent(path string) error {
	return requireSQLiteSidecarsAbsentWithCode(path, ProjectionSourceBusy)
}

func requireProjectionSQLiteSidecarsAbsent(path string) error {
	return requireSQLiteSidecarsAbsentWithCode(path, ProjectionInvalid)
}

func requireSQLiteSidecarsAbsentWithCode(path, code string) error {
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return projectionFailure(code, fmt.Errorf("SQLite sidecar %s is present", suffix))
		} else if !errors.Is(err, os.ErrNotExist) {
			return projectionFailure(code, err)
		}
	}
	return nil
}

func requireProjectionDirectory(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return projectionFailure(ProjectionInvalid, err)
	}
	expected := map[string]bool{
		projectionDatabaseName: false,
		projectionManifestName: false,
		projectionReceiptName:  false,
	}
	if len(entries) != len(expected) {
		return projectionFailure(ProjectionInvalid, errors.New("projection directory contains unexpected entries"))
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return projectionFailure(ProjectionInvalid, errors.New("projection directory contains an unexpected file"))
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return projectionFailure(ProjectionInvalid, errors.New("projection directory contains a non-regular file"))
		}
		expected[entry.Name()] = true
	}
	for _, found := range expected {
		if !found {
			return projectionFailure(ProjectionInvalid, errors.New("projection directory is incomplete"))
		}
	}
	return nil
}

func containedManifestPath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", projectionFailure(ProjectionPathEscape, errors.New("manifest path must be relative"))
	}
	path, err := safeio.Contained(root, filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", projectionFailure(ProjectionPathEscape, err)
	}
	return path, nil
}

func pathsOverlap(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func sourceStateMatches(source ProjectionSource, state projectionDatabaseState) bool {
	return source.UserVersion == state.userVersion && source.PageCount == state.pageCount &&
		source.IntegrityCheck == state.integrityCheck &&
		source.ForeignKeyViolations == state.foreignKeyViolations && source.LiveLeases == state.liveLeases
}

func projectionStateMatches(artifact ProjectionArtifact, identity projectionFileIdentity, state projectionDatabaseState) bool {
	return artifact.SizeBytes == identity.size && artifact.SHA256 == identity.sha256 &&
		artifact.UserVersion == state.userVersion && artifact.PageCount == state.pageCount &&
		artifact.IntegrityCheck == state.integrityCheck && artifact.ForeignKeyViolations == state.foreignKeyViolations
}

func projectionReceipt(manifest ProjectionManifest, status, manifestRelative, manifestSHA string) ProjectionReceipt {
	return ProjectionReceipt{
		Schema: ProjectionReceiptSchema, OperationID: manifest.OperationID, Status: status,
		ManifestRelativePath: manifestRelative, ManifestSHA256: manifestSHA,
		ProjectionSHA256: manifest.Projection.SHA256, SourceUnchanged: manifest.SourceUnchanged,
		VerifiedAt: manifest.CreatedAt,
	}
}

func projectionTimestamp() string { return time.Now().UTC().Format(time.RFC3339Nano) }

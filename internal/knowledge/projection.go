package knowledge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	projectionDatabaseName = "projection.sqlite3"
	projectionManifestName = "manifest.json"
	projectionReceiptName  = "receipt.json"
)

type projectionPaths struct {
	knowledgeRoot         string
	source                string
	sourceRelative        string
	projectionRoot        string
	finalDirectory        string
	stagingDirectory      string
	finalManifest         string
	finalManifestRelative string
	stagingManifest       string
	stagingReceipt        string
	stagingProjection     string
	finalProjection       string
}

type projectionFileIdentity struct {
	size   int64
	sha256 string
}

type projectionDatabaseState struct {
	userVersion          int
	pageCount            int
	integrityCheck       string
	foreignKeyViolations int
	liveLeases           int
}

func CreateProjection(ctx context.Context, request ProjectionRequest) (ProjectionReceipt, error) {
	paths, err := resolveProjectionPaths(request)
	if err != nil {
		return ProjectionReceipt{}, err
	}
	if info, statErr := os.Lstat(paths.finalDirectory); statErr == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ProjectionReceipt{}, projectionFailure(ProjectionConflict, errors.New("projection target is not a real directory"))
		}
		receipt, manifest, verifyErr := verifyProjectionAt(ctx, paths.knowledgeRoot, paths.finalManifest)
		if verifyErr != nil || manifest.OperationID != request.OperationID ||
			manifest.Source.RelativePath != paths.sourceRelative {
			if verifyErr == nil {
				verifyErr = errors.New("existing projection does not match request")
			}
			return ProjectionReceipt{}, projectionFailure(ProjectionConflict, verifyErr)
		}
		return receipt, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, statErr)
	}
	if _, statErr := os.Lstat(paths.stagingDirectory); statErr == nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIncomplete, errors.New("projection staging directory already exists"))
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, statErr)
	}

	if err := requireSQLiteSidecarsAbsent(paths.source); err != nil {
		return ProjectionReceipt{}, err
	}
	before, err := projectionFileFingerprint(paths.source)
	if err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	sourceDatabase, err := openProjectionDatabase(paths.source, true)
	if err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionSourceInvalid, err)
	}
	sourceState, inspectErr := inspectProjectionDatabase(ctx, sourceDatabase)
	if inspectErr == nil {
		inspectErr = requireValidSourceState(sourceState)
	}
	if inspectErr != nil {
		closeErr := sourceDatabase.Close()
		var projectionErr *ProjectionError
		if !errors.As(inspectErr, &projectionErr) {
			inspectErr = projectionFailure(ProjectionSourceInvalid, inspectErr)
		}
		if closeErr != nil {
			inspectErr = errors.Join(inspectErr, projectionFailure(ProjectionIOFailed, closeErr))
		}
		return ProjectionReceipt{}, inspectErr
	}
	if err := os.Mkdir(paths.stagingDirectory, 0o700); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, errors.Join(err, sourceDatabase.Close()))
	}
	backupErr := backupProjectionDatabase(ctx, sourceDatabase, paths.stagingProjection)
	closeErr := sourceDatabase.Close()
	if err := errors.Join(backupErr, closeErr); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	if err := os.Chmod(paths.stagingProjection, 0o600); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	if err := requireSQLiteSidecarsAbsent(paths.source); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionSourceChanged, err)
	}
	after, err := projectionFileFingerprint(paths.source)
	if err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionSourceChanged, err)
	}
	if before != after {
		return ProjectionReceipt{}, projectionFailure(ProjectionSourceChanged, errors.New("source SQLite bytes changed during projection"))
	}
	if err := requireProjectionSQLiteSidecarsAbsent(paths.stagingProjection); err != nil {
		return ProjectionReceipt{}, err
	}

	projectionIdentity, projectionState, err := inspectProjectionFile(ctx, paths.stagingProjection)
	if err != nil {
		return ProjectionReceipt{}, err
	}
	if projectionState.userVersion != sourceState.userVersion || projectionState.pageCount != sourceState.pageCount {
		return ProjectionReceipt{}, projectionFailure(ProjectionInvalid, errors.New("projection database state differs from source"))
	}
	manifest := ProjectionManifest{
		Schema: ProjectionManifestSchema, OperationID: request.OperationID,
		CreatedAt: projectionTimestamp(), SourceUnchanged: true,
		Source: ProjectionSource{
			RelativePath: paths.sourceRelative, SizeBytesBefore: before.size, SizeBytesAfter: after.size,
			SHA256Before: before.sha256, SHA256After: after.sha256, UserVersion: sourceState.userVersion,
			PageCount: sourceState.pageCount, IntegrityCheck: sourceState.integrityCheck,
			ForeignKeyViolations: sourceState.foreignKeyViolations, LiveLeases: sourceState.liveLeases,
			WALState: "ABSENT", SHMState: "ABSENT",
		},
		Projection: ProjectionArtifact{
			RelativePath: projectionDatabaseName, SizeBytes: projectionIdentity.size,
			SHA256: projectionIdentity.sha256, UserVersion: projectionState.userVersion,
			PageCount: projectionState.pageCount, IntegrityCheck: projectionState.integrityCheck,
			ForeignKeyViolations: projectionState.foreignKeyViolations,
		},
	}
	if err := safeio.WriteJSON(paths.stagingManifest, manifest); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	manifestSHA, err := safeio.FileSHA256(paths.stagingManifest)
	if err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	receipt := projectionReceipt(manifest, "VERIFIED", paths.finalManifestRelative, manifestSHA)
	if err := safeio.WriteJSON(paths.stagingReceipt, receipt); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	if _, err := os.Lstat(paths.finalDirectory); err == nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionConflict, errors.New("projection target appeared before publication"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	if err := os.Rename(paths.stagingDirectory, paths.finalDirectory); err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionIOFailed, err)
	}
	verified, _, err := verifyProjectionAt(ctx, paths.knowledgeRoot, paths.finalManifest)
	if err != nil {
		return ProjectionReceipt{}, err
	}
	return verified, nil
}

func VerifyProjection(ctx context.Context, request VerifyProjectionRequest) (ProjectionReceipt, error) {
	knowledgeRoot, err := filepath.Abs(strings.TrimSpace(request.KnowledgeRoot))
	if err != nil || strings.TrimSpace(request.KnowledgeRoot) == "" || strings.TrimSpace(request.ManifestPath) == "" {
		return ProjectionReceipt{}, projectionFailure(ProjectionInvalidRequest, errors.New("knowledge root and manifest path are required"))
	}
	manifestPath := request.ManifestPath
	if !filepath.IsAbs(manifestPath) {
		manifestPath = filepath.Join(knowledgeRoot, manifestPath)
	}
	manifestPath, err = lexicallyContained(knowledgeRoot, manifestPath)
	if err != nil {
		return ProjectionReceipt{}, projectionFailure(ProjectionPathEscape, err)
	}
	receipt, _, err := verifyProjectionAt(ctx, knowledgeRoot, manifestPath)
	return receipt, err
}

func lexicallyContained(root, candidate string) (string, error) {
	candidate, err := filepath.Abs(candidate)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(root, candidate)
	if err != nil || relative == "." || relative == "" || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes configured root")
	}
	return candidate, nil
}

func resolveProjectionPaths(request ProjectionRequest) (projectionPaths, error) {
	if strings.TrimSpace(request.KnowledgeRoot) == "" || !projectionIDPattern.MatchString(request.OperationID) {
		return projectionPaths{}, projectionFailure(ProjectionInvalidRequest, errors.New("knowledge root and valid operation ID are required"))
	}
	knowledgeRoot, err := filepath.Abs(request.KnowledgeRoot)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionInvalidRequest, err)
	}
	source := request.SourcePath
	if strings.TrimSpace(source) == "" {
		source = filepath.Join(knowledgeRoot, "service", "knowledge.sqlite3")
	} else if !filepath.IsAbs(source) {
		source = filepath.Join(knowledgeRoot, source)
	}
	source, err = safeio.Contained(knowledgeRoot, source)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	if _, err := projectionFileFingerprint(source); err != nil {
		return projectionPaths{}, projectionFailure(ProjectionIOFailed, err)
	}
	projectionRoot := request.ProjectionRoot
	if strings.TrimSpace(projectionRoot) == "" {
		projectionRoot = filepath.Join(knowledgeRoot, "handoffs", "sqlite-projections")
	} else if !filepath.IsAbs(projectionRoot) {
		projectionRoot = filepath.Join(knowledgeRoot, projectionRoot)
	}
	projectionRoot, err = safeio.Contained(knowledgeRoot, projectionRoot)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	if pathsOverlap(filepath.Dir(source), projectionRoot) || pathsOverlap(projectionRoot, source) {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, errors.New("projection root overlaps active SQLite directory"))
	}
	if err := os.MkdirAll(projectionRoot, 0o755); err != nil {
		return projectionPaths{}, projectionFailure(ProjectionIOFailed, err)
	}
	projectionRoot, err = safeio.Contained(knowledgeRoot, projectionRoot)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	finalDirectory, err := safeio.Contained(projectionRoot, filepath.Join(projectionRoot, request.OperationID))
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	stagingDirectory, err := safeio.Contained(projectionRoot, filepath.Join(projectionRoot, request.OperationID+".staging"))
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	sourceRelative, err := filepath.Rel(knowledgeRoot, source)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	finalManifest := filepath.Join(finalDirectory, projectionManifestName)
	finalManifestRelative, err := filepath.Rel(knowledgeRoot, finalManifest)
	if err != nil {
		return projectionPaths{}, projectionFailure(ProjectionPathEscape, err)
	}
	return projectionPaths{
		knowledgeRoot: knowledgeRoot, source: source, sourceRelative: filepath.ToSlash(sourceRelative),
		projectionRoot: projectionRoot, finalDirectory: finalDirectory, stagingDirectory: stagingDirectory,
		finalManifest: finalManifest, finalManifestRelative: filepath.ToSlash(finalManifestRelative),
		stagingManifest:   filepath.Join(stagingDirectory, projectionManifestName),
		stagingReceipt:    filepath.Join(stagingDirectory, projectionReceiptName),
		stagingProjection: filepath.Join(stagingDirectory, projectionDatabaseName),
		finalProjection:   filepath.Join(finalDirectory, projectionDatabaseName),
	}, nil
}

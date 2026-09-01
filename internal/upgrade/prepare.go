package upgrade

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	_ "modernc.org/sqlite"
)

type PrepareOptions struct {
	RuntimeRoot      string
	CodexRoot        string
	KnowledgeRoot    string
	CurrentVersion   string
	Port             int
	RestartDashboard bool
}

var inspectReleaseBinary = inspectBinary

func Prepare(ctx context.Context, client *Client, release Release, options PrepareOptions) (returned Plan, returnedErr error) {
	if client == nil {
		client = NewClient()
	}
	if newer, err := IsNewer(release.Version, options.CurrentVersion); err != nil || !newer {
		return Plan{}, failure("UPDATE_NOT_NEWER", err)
	}
	archiveName, err := ArchiveAssetName()
	if err != nil {
		return Plan{}, err
	}
	archiveAsset, archiveOK := release.Assets[archiveName]
	manifestAsset, manifestOK := release.Assets["release-manifest.json"]
	signaturesAsset, signaturesOK := release.Assets["signatures.json"]
	if !archiveOK || !manifestOK || !signaturesOK || archiveAsset.Size > maxArchiveBytes ||
		manifestAsset.Size > maxMetadataBytes || signaturesAsset.Size > maxMetadataBytes {
		return Plan{}, failure("RELEASE_ASSET_MISSING", nil)
	}
	if _, _, err := releaseTrustRoot(); err != nil {
		return Plan{}, err
	}
	roots, err := absolutePrepareRoots(options)
	if err != nil {
		return Plan{}, err
	}
	if err := bootstrapRestoreControlRoot(roots.Runtime); err != nil {
		return Plan{}, err
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	if err := acquireOperation(roots.Runtime, identifier, phasePreparing); err != nil {
		return Plan{}, err
	}
	operationOwned := true
	stageRoot := filepath.Join(roots.Runtime, "upgrade", "staging", identifier)
	defer func() {
		if returnedErr != nil && operationOwned {
			_ = os.RemoveAll(stageRoot)
			returnedErr = writeFailureState(roots.Runtime, State{
				Status: "FAILED", OperationID: identifier, CurrentVersion: options.CurrentVersion,
				TargetVersion: release.Version, ErrorCode: errorCodeOf(returnedErr),
			}, returnedErr)
			returnedErr = errors.Join(returnedErr, releaseTerminalOperation(roots.Runtime, identifier, returnedErr))
		}
	}()
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	if err := writeState(roots.Runtime, State{
		Status: "PREPARING", OperationID: identifier, CurrentVersion: options.CurrentVersion,
		TargetVersion: release.Version,
	}); err != nil {
		return Plan{}, stateWriteFailure(err)
	}
	manifestPath := filepath.Join(stageRoot, "release-manifest.json")
	if _, err := client.Download(ctx, manifestAsset.URL, manifestPath, maxMetadataBytes); err != nil {
		return Plan{}, err
	}
	signaturesPath := filepath.Join(stageRoot, "signatures.json")
	if _, err := client.Download(ctx, signaturesAsset.URL, signaturesPath, maxMetadataBytes); err != nil {
		return Plan{}, err
	}
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		return Plan{}, failure("RELEASE_MANIFEST_INVALID", err)
	}
	signaturesData, err := os.ReadFile(signaturesPath)
	if err != nil {
		return Plan{}, failure("RELEASE_SIGNATURE_INVALID", err)
	}
	manifest, manifestDigest, err := verifyReleaseEnvelope(release, manifestData, signaturesData)
	if err != nil {
		return Plan{}, err
	}
	signedArchive, ok := manifest.asset(archiveName)
	if !ok || signedArchive.Size != archiveAsset.Size || signedArchive.Size > maxArchiveBytes {
		return Plan{}, failure("RELEASE_MANIFEST_INVALID", nil)
	}
	archivePath := filepath.Join(stageRoot, archiveName)
	archiveDigest, err := client.Download(ctx, archiveAsset.URL, archivePath, signedArchive.Size)
	if err != nil {
		return Plan{}, err
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil || archiveInfo.Size() != signedArchive.Size ||
		subtle.ConstantTimeCompare([]byte(archiveDigest), []byte(signedArchive.SHA256)) != 1 {
		return Plan{}, failure("RELEASE_CHECKSUM_MISMATCH", nil)
	}
	sourceRoot, binaryDigest, err := ExtractBundle(
		archivePath, filepath.Join(stageRoot, "source"), release.Version, runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		return Plan{}, err
	}
	binaryName, err := bundleBinaryName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Plan{}, err
	}
	binaryPath := filepath.Join(sourceRoot, "bin", binaryName)
	if err := os.Chmod(binaryPath, 0o700); err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	version, targetSchema, err := inspectReleaseBinary(ctx, binaryPath)
	if err != nil || version != release.Version {
		return Plan{}, failure("RELEASE_BINARY_INVALID", err)
	}
	databasePath := filepath.Join(roots.Knowledge, "service", "knowledge.sqlite3")
	databaseSchema, err := databaseSchemaVersion(databasePath)
	if err != nil {
		return Plan{}, failure("KNOWLEDGE_SCHEMA_READ_FAILED", err)
	}
	if targetSchema < databaseSchema {
		return Plan{}, failure("UPGRADE_SCHEMA_INCOMPATIBLE", nil)
	}
	sourceTreeDigest, err := safeio.TreeHash(sourceRoot)
	if err != nil {
		return Plan{}, failure("RELEASE_BUNDLE_INVALID", err)
	}
	plan := Plan{
		Schema: planSchema, ID: identifier, PreparedAt: time.Now().UTC(),
		FromVersion: options.CurrentVersion, ToVersion: release.Version,
		DatabaseSchema: databaseSchema, PreviousMaxSchema: knowledge.LatestSchema, TargetMaxSchema: targetSchema,
		RuntimeRoot: roots.Runtime, CodexRoot: roots.Codex, KnowledgeRoot: roots.Knowledge,
		StageRoot: stageRoot, SourceRoot: sourceRoot, SourceTreeSHA256: sourceTreeDigest,
		BinaryPath: binaryPath, BinarySHA256: binaryDigest, ArchiveSHA256: archiveDigest,
		ReleaseManifestSHA256: manifestDigest, SigningKeySHA256: buildinfo.ReleaseEd25519PublicKeySHA256,
		Port: options.Port, RestartDashboard: options.RestartDashboard, ReleaseURL: release.PageURL,
	}
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		return Plan{}, planWriteFailure(err)
	}
	if err := validatePlan(plan, planPath(plan)); err != nil {
		return Plan{}, err
	}
	if err := writeState(roots.Runtime, State{
		Status: "PREPARED", OperationID: identifier, CurrentVersion: options.CurrentVersion,
		TargetVersion: release.Version,
	}); err != nil {
		return Plan{}, stateWriteFailure(err)
	}
	if err := transitionOperation(roots.Runtime, identifier, phasePreparing, phasePrepared); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

type prepareRoots struct{ Runtime, Codex, Knowledge string }

func absolutePrepareRoots(options PrepareOptions) (prepareRoots, error) {
	if options.Port < 1 || options.Port > 65535 {
		return prepareRoots{}, failure("UPGRADE_OPTIONS_INVALID", nil)
	}
	values := []*string{&options.RuntimeRoot, &options.CodexRoot, &options.KnowledgeRoot}
	for _, value := range values {
		absolute, err := filepath.Abs(strings.TrimSpace(*value))
		if err != nil || strings.TrimSpace(*value) == "" {
			return prepareRoots{}, failure("UPGRADE_OPTIONS_INVALID", err)
		}
		*value = filepath.Clean(absolute)
	}
	planRoots := Plan{
		RuntimeRoot: options.RuntimeRoot, CodexRoot: options.CodexRoot, KnowledgeRoot: options.KnowledgeRoot,
	}
	if _, err := restorePlanRoots(planRoots); err != nil {
		return prepareRoots{}, failure("UPGRADE_OPTIONS_INVALID", err)
	}
	for _, value := range values {
		if err := os.MkdirAll(*value, 0o700); err != nil {
			return prepareRoots{}, failure("UPGRADE_OPTIONS_INVALID", err)
		}
		info, err := os.Lstat(*value)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return prepareRoots{}, failure("UPGRADE_OPTIONS_INVALID", err)
		}
	}
	return prepareRoots{Runtime: options.RuntimeRoot, Codex: options.CodexRoot, Knowledge: options.KnowledgeRoot}, nil
}

func inspectBinary(ctx context.Context, binary string) (string, int, error) {
	version, err := commandOutput(ctx, binary, "version")
	if err != nil {
		return "", 0, err
	}
	if _, err := parseVersion(version); err != nil {
		return "", 0, err
	}
	schemaText, err := commandOutput(ctx, binary, "upgrade", "schema-version")
	if err != nil {
		return "", 0, err
	}
	schema, err := strconv.Atoi(schemaText)
	if err != nil || schema < 0 {
		return "", 0, errors.New("binary returned invalid schema version")
	}
	return version, schema, nil
}

func commandOutput(parent context.Context, binary string, arguments ...string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, arguments...)
	command.Stdin = nil
	output, err := command.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(output))
	if value == "" || strings.ContainsAny(value, "\r\n") || len(value) > 128 {
		return "", fmt.Errorf("command output is invalid")
	}
	return value, nil
}

func databaseSchemaVersion(path string) (int, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return 0, err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return 0, err
	}
	defer database.Close()
	var version int
	if err := database.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

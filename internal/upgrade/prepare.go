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
	"strconv"
	"strings"
	"time"

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
	assetName, err := BinaryAssetName()
	if err != nil {
		return Plan{}, err
	}
	binaryAsset, binaryOK := release.Assets[assetName]
	checksums, checksumOK := release.Assets["SHA256SUMS"]
	if !binaryOK || !checksumOK || binaryAsset.Size > maxBinaryBytes || checksums.Size > maxMetadataBytes {
		return Plan{}, failure("RELEASE_ASSET_MISSING", nil)
	}
	roots, err := absolutePrepareRoots(options)
	if err != nil {
		return Plan{}, err
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	stageRoot := filepath.Join(roots.Runtime, "upgrade", "staging", identifier)
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	defer func() {
		if returnedErr != nil {
			_ = os.RemoveAll(stageRoot)
			_ = writeState(roots.Runtime, State{
				Status: "FAILED", OperationID: identifier, CurrentVersion: options.CurrentVersion,
				TargetVersion: release.Version, ErrorCode: errorCodeOf(returnedErr),
			})
		}
	}()
	if err := writeState(roots.Runtime, State{
		Status: "PREPARING", OperationID: identifier, CurrentVersion: options.CurrentVersion,
		TargetVersion: release.Version,
	}); err != nil {
		return Plan{}, failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	checksumPath := filepath.Join(stageRoot, "SHA256SUMS")
	if _, err := client.Download(ctx, checksums.URL, checksumPath, maxMetadataBytes); err != nil {
		return Plan{}, err
	}
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		return Plan{}, failure("RELEASE_CHECKSUM_INVALID", err)
	}
	expectedDigest, err := ExpectedChecksum(checksumData, assetName)
	if err != nil {
		return Plan{}, err
	}
	binaryPath := filepath.Join(stageRoot, "helper")
	if filepath.Ext(assetName) == ".exe" {
		binaryPath += ".exe"
	}
	actualDigest, err := client.Download(ctx, binaryAsset.URL, binaryPath, maxBinaryBytes)
	if err != nil {
		return Plan{}, err
	}
	if subtle.ConstantTimeCompare([]byte(actualDigest), []byte(expectedDigest)) != 1 {
		return Plan{}, failure("RELEASE_CHECKSUM_MISMATCH", nil)
	}
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
	archivePath := filepath.Join(stageRoot, "source.zip")
	if _, err := client.Download(ctx, release.ArchiveURL, archivePath, maxArchiveBytes); err != nil {
		return Plan{}, err
	}
	sourceRoot, err := ExtractRelease(archivePath, filepath.Join(stageRoot, "source"), release.Version)
	if err != nil {
		return Plan{}, err
	}
	_ = os.Remove(archivePath)
	_ = os.Remove(checksumPath)
	plan := Plan{
		Schema: planSchema, ID: identifier, PreparedAt: time.Now().UTC(),
		FromVersion: options.CurrentVersion, ToVersion: release.Version,
		DatabaseSchema: databaseSchema, PreviousMaxSchema: knowledge.LatestSchema, TargetMaxSchema: targetSchema,
		RuntimeRoot: roots.Runtime, CodexRoot: roots.Codex, KnowledgeRoot: roots.Knowledge,
		StageRoot: stageRoot, SourceRoot: sourceRoot, BinaryPath: binaryPath, BinarySHA256: actualDigest,
		Port: options.Port, RestartDashboard: options.RestartDashboard, ReleaseURL: release.PageURL,
	}
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		return Plan{}, failure("UPGRADE_PLAN_WRITE_FAILED", err)
	}
	if err := validatePlan(plan, planPath(plan)); err != nil {
		return Plan{}, err
	}
	if err := writeState(roots.Runtime, State{
		Status: "PREPARED", OperationID: identifier, CurrentVersion: options.CurrentVersion,
		TargetVersion: release.Version,
	}); err != nil {
		return Plan{}, failure("UPGRADE_STATE_WRITE_FAILED", err)
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

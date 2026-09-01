package upgrade

import (
	"context"
	"crypto/subtle"
	"os"
	"path/filepath"
	"runtime"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func readAuthenticatedPlan(path, expectedSHA256 string) (Plan, error) {
	if !hexDigestPattern.MatchString(expectedSHA256) {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	actualSHA256 := safeio.SHA256(data)
	if subtle.ConstantTimeCompare([]byte(actualSHA256), []byte(expectedSHA256)) != 1 {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", nil)
	}
	var plan Plan
	if err := decodeStrictJSON(data, &plan); err != nil {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	if err := validatePlan(plan, path); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func verifyPreparedPlan(ctx context.Context, plan Plan) (Plan, error) {
	state := Status(plan.RuntimeRoot, plan.FromVersion)
	if state.OperationID != plan.ID || (state.Status != "PREPARED" && state.Status != "ACTIVATION_PENDING") {
		return Plan{}, failure("UPGRADE_STATE_CONFLICT", nil)
	}
	archive, err := verifyPreparedReleaseEnvelope(plan)
	if err != nil {
		return Plan{}, err
	}
	return verifyPreparedArchive(ctx, plan, archive)
}

func verifyPreparedReleaseEnvelope(plan Plan) (releaseManifestAsset, error) {
	manifestData, err := os.ReadFile(filepath.Join(plan.StageRoot, "release-manifest.json"))
	if err != nil {
		return releaseManifestAsset{}, failure("RELEASE_MANIFEST_INVALID", err)
	}
	signaturesData, err := os.ReadFile(filepath.Join(plan.StageRoot, "signatures.json"))
	if err != nil {
		return releaseManifestAsset{}, failure("RELEASE_SIGNATURE_INVALID", err)
	}
	release := Release{Version: plan.ToVersion, Tag: "v" + plan.ToVersion}
	manifest, manifestDigest, err := verifyReleaseEnvelope(release, manifestData, signaturesData)
	if err != nil {
		return releaseManifestAsset{}, err
	}
	archiveName, err := archiveAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return releaseManifestAsset{}, err
	}
	archive, ok := manifest.asset(archiveName)
	if !ok || subtle.ConstantTimeCompare([]byte(manifestDigest), []byte(plan.ReleaseManifestSHA256)) != 1 ||
		subtle.ConstantTimeCompare([]byte(manifest.Signature.PublicKeySHA256), []byte(plan.SigningKeySHA256)) != 1 ||
		subtle.ConstantTimeCompare([]byte(archive.SHA256), []byte(plan.ArchiveSHA256)) != 1 {
		return releaseManifestAsset{}, failure("UPGRADE_PLAN_INVALID", nil)
	}
	return archive, nil
}

func verifyPreparedArchive(ctx context.Context, plan Plan, signed releaseManifestAsset) (Plan, error) {
	archiveName, err := archiveAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Plan{}, err
	}
	archivePath := filepath.Join(plan.StageRoot, archiveName)
	info, err := os.Lstat(archivePath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() != signed.Size {
		return Plan{}, failure("RELEASE_ARCHIVE_INVALID", err)
	}
	archiveHash, err := safeio.FileSHA256(archivePath)
	if err != nil || subtle.ConstantTimeCompare([]byte(archiveHash), []byte(signed.SHA256)) != 1 {
		return Plan{}, failure("RELEASE_CHECKSUM_MISMATCH", err)
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	verifiedRoot := filepath.Join(plan.StageRoot, "verified-source-"+identifier)
	sourceRoot, binaryHash, err := ExtractBundle(
		archivePath, verifiedRoot, plan.ToVersion, runtime.GOOS, runtime.GOARCH,
	)
	if err != nil {
		return Plan{}, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = os.RemoveAll(verifiedRoot)
		}
	}()
	treeHash, err := safeio.TreeHash(sourceRoot)
	if err != nil || subtle.ConstantTimeCompare([]byte(treeHash), []byte(plan.SourceTreeSHA256)) != 1 {
		return Plan{}, failure("RELEASE_BUNDLE_INVALID", err)
	}
	if subtle.ConstantTimeCompare([]byte(binaryHash), []byte(plan.BinarySHA256)) != 1 {
		return Plan{}, failure("RELEASE_BINARY_INVALID", nil)
	}
	binaryName, err := bundleBinaryName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return Plan{}, err
	}
	binaryPath := filepath.Join(sourceRoot, "bin", binaryName)
	if err := os.Chmod(binaryPath, 0o700); err != nil {
		return Plan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	version, schema, err := inspectReleaseBinary(ctx, binaryPath)
	if err != nil || version != plan.ToVersion || schema != plan.TargetMaxSchema {
		return Plan{}, failure("RELEASE_BINARY_INVALID", err)
	}
	plan.SourceRoot = sourceRoot
	plan.SourceTreeSHA256 = treeHash
	plan.BinaryPath = binaryPath
	plan.BinarySHA256 = binaryHash
	plan.ArchiveSHA256 = archiveHash
	valid = true
	return plan, nil
}

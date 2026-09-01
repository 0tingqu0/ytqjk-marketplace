package upgrade

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestVerifyPreparedPlanRejectsTamperedSourceAndPlanHashes(t *testing.T) {
	originalInspector := inspectReleaseBinary
	inspectReleaseBinary = func(context.Context, string) (string, int, error) {
		return buildinfo.Version, 4, nil
	}
	t.Cleanup(func() { inspectReleaseBinary = originalInspector })

	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	stageRoot := filepath.Join(runtimeRoot, "upgrade", "staging", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	archiveName, err := archiveAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(stageRoot, archiveName)
	goodFiles, binaryName, _, _ := bundleFixture(t, runtime.GOOS, runtime.GOARCH)
	if runtime.GOOS == "windows" {
		writeZip(t, archivePath, goodFiles)
	} else {
		writeTarGzip(t, archivePath, goodFiles)
	}
	archiveHash, err := safeio.FileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	_, manifestData, signaturesData := signedReleaseFixtureWithArchive(
		t, archiveName, archiveHash, archiveInfo.Size(),
	)
	writeFixture(t, filepath.Join(stageRoot, "release-manifest.json"), string(manifestData))
	writeFixture(t, filepath.Join(stageRoot, "signatures.json"), string(signaturesData))

	maliciousFiles := make(map[string]string, len(goodFiles))
	for name, content := range goodFiles {
		maliciousFiles[name] = content
	}
	maliciousBinary := "attacker-controlled-binary"
	maliciousBinaryHash := safeio.SHA256([]byte(maliciousBinary))
	maliciousFiles["bin/"+binaryName] = maliciousBinary
	innerManifest, err := json.Marshal(map[string]string{
		"schema": "ytqjk-release-bundle/v1", "version": buildinfo.Version,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "binary_sha256": maliciousBinaryHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	maliciousFiles["release-manifest.json"] = string(innerManifest)
	sourceRoot := filepath.Join(stageRoot, "source")
	for name, content := range maliciousFiles {
		writeFixture(t, filepath.Join(sourceRoot, filepath.FromSlash(name)), content)
	}
	maliciousTreeHash, err := safeio.TreeHash(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Schema: planSchema, ID: filepath.Base(stageRoot), PreparedAt: time.Now().UTC(),
		FromVersion: "0.6.10", ToVersion: buildinfo.Version,
		PreviousMaxSchema: 4, TargetMaxSchema: 4,
		RuntimeRoot: runtimeRoot, CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"), StageRoot: stageRoot,
		SourceRoot: sourceRoot, SourceTreeSHA256: maliciousTreeHash,
		BinaryPath: filepath.Join(sourceRoot, "bin", binaryName), BinarySHA256: maliciousBinaryHash,
		ArchiveSHA256: archiveHash, ReleaseManifestSHA256: safeio.SHA256(manifestData),
		SigningKeySHA256: buildinfo.ReleaseEd25519PublicKeySHA256, Port: 8765,
	}
	if err := writeState(runtimeRoot, State{
		Status: "PREPARED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := verifyPreparedPlan(context.Background(), plan); errorCode(err) != "RELEASE_BUNDLE_INVALID" {
		t.Fatalf("tampered prepared source error = %v", err)
	}
}

func TestPlanDigestBindsLaunchAndActivationToExactBytes(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	stageRoot := filepath.Join(runtimeRoot, "upgrade", "staging", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	sourceRoot := filepath.Join(stageRoot, "source")
	binaryPath := filepath.Join(sourceRoot, "bin", runtimeBinaryName())
	writeFixture(t, binaryPath, "bound-helper")
	binaryHash, err := safeio.FileSHA256(binaryPath)
	if err != nil {
		t.Fatal(err)
	}
	plan := Plan{
		Schema: planSchema, ID: filepath.Base(stageRoot), PreparedAt: time.Now().UTC(),
		FromVersion: "0.6.10", ToVersion: buildinfo.Version,
		PreviousMaxSchema: 4, TargetMaxSchema: 4,
		RuntimeRoot: runtimeRoot, CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"), StageRoot: stageRoot,
		SourceRoot: sourceRoot, SourceTreeSHA256: safeio.SHA256([]byte("tree")),
		BinaryPath: binaryPath, BinarySHA256: binaryHash,
		ArchiveSHA256:         safeio.SHA256([]byte("archive")),
		ReleaseManifestSHA256: safeio.SHA256([]byte("manifest")),
		SigningKeySHA256:      safeio.SHA256([]byte("key")), Port: 8765,
	}
	path := planPath(plan)
	if err := safeio.WriteJSON(path, plan); err != nil {
		t.Fatal(err)
	}
	digest, err := launchPlanDigest(plan, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAuthenticatedPlan(path, digest); err != nil {
		t.Fatalf("authenticated plan read failed: %v", err)
	}
	tampered := plan
	tampered.ReleaseURL = "https://example.invalid/tampered"
	if err := safeio.WriteJSON(path, tampered); err != nil {
		t.Fatal(err)
	}
	if _, err := launchPlanDigest(plan, path); errorCode(err) != "UPGRADE_PLAN_INVALID" {
		t.Fatalf("launch accepted changed plan: %v", err)
	}
	if _, err := readAuthenticatedPlan(path, digest); errorCode(err) != "UPGRADE_PLAN_INVALID" {
		t.Fatalf("activation accepted changed plan bytes: %v", err)
	}
}

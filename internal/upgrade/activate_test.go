package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestActivateKeepsPreviousGenerationSnapshot(t *testing.T) {
	originalInspector := inspectReleaseBinary
	inspectReleaseBinary = func(context.Context, string) (string, int, error) {
		return buildinfo.Version, 4, nil
	}
	t.Cleanup(func() { inspectReleaseBinary = originalInspector })

	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	codexRoot := filepath.Join(root, "codex")
	knowledgeRoot := filepath.Join(root, "knowledge")
	operationID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stageRoot := filepath.Join(runtimeRoot, "upgrade", "staging", operationID)
	sourceRoot := filepath.Join(stageRoot, "source")
	helper := filepath.Join(sourceRoot, "bin", runtimeBinaryName())
	writeFixture(t, helper, "new-runtime")
	writeFixture(t, filepath.Join(sourceRoot, "install.ps1"), "fixture")
	writeFixture(t, filepath.Join(sourceRoot, "install.cmd"), "fixture")
	writeFixture(t, filepath.Join(sourceRoot, "install.sh"), "fixture")
	binaryHash, _ := safeio.FileSHA256(helper)
	bundleManifest, _ := json.Marshal(map[string]string{
		"schema": "ytqjk-release-bundle/v1", "version": buildinfo.Version,
		"os": runtime.GOOS, "arch": runtime.GOARCH, "binary_sha256": binaryHash,
	})
	writeFixture(t, filepath.Join(sourceRoot, "release-manifest.json"), string(bundleManifest))
	for _, name := range pluginNames {
		manifest, _ := json.Marshal(map[string]string{"name": name, "version": buildinfo.Version})
		writeFixture(t, filepath.Join(sourceRoot, "plugins", name, ".codex-plugin", "plugin.json"), string(manifest))
		writeFixture(t, filepath.Join(sourceRoot, "plugins", name, "SKILL.md"), "new-"+name)
	}
	if digest, err := validateReleaseBundle(sourceRoot, buildinfo.Version, runtime.GOOS, runtime.GOARCH); err != nil || digest != binaryHash {
		t.Fatalf("bundle validation = %q, %v", digest, err)
	}
	oldBinary := filepath.Join(runtimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, oldBinary, "old-runtime")
	oldEntries := make([]map[string]string, 0, len(pluginNames))
	for _, name := range pluginNames {
		pluginRoot := filepath.Join(codexRoot, "plugins", name)
		manifest, _ := json.Marshal(map[string]string{"name": name, "version": "0.6.10"})
		writeFixture(t, filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), string(manifest))
		writeFixture(t, filepath.Join(pluginRoot, "old.txt"), "old-"+name)
		hash, err := safeio.TreeHash(pluginRoot)
		if err != nil {
			t.Fatal(err)
		}
		oldEntries = append(oldEntries, map[string]string{"name": name, "tree_sha256": hash})
	}
	managed := map[string]any{"schema": "ytqjk-managed-plugins/v1", "version": "0.6.10", "plugins": oldEntries}
	if err := safeio.WriteJSON(filepath.Join(codexRoot, "plugins", ".ytqjk-managed-plugins.json"), managed); err != nil {
		t.Fatal(err)
	}
	sourceTreeHash, _ := safeio.TreeHash(sourceRoot)
	archiveName, err := archiveAssetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(stageRoot, archiveName)
	if runtime.GOOS == "windows" {
		writeZip(t, archivePath, fixtureTreeFiles(t, sourceRoot))
	} else {
		writeTarGzip(t, archivePath, fixtureTreeFiles(t, sourceRoot))
	}
	archiveHash, err := safeio.FileSHA256(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	archiveInfo, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	release, manifestData, signaturesData := signedReleaseFixtureWithArchive(
		t, archiveName, archiveHash, archiveInfo.Size(),
	)
	writeFixture(t, filepath.Join(stageRoot, "release-manifest.json"), string(manifestData))
	writeFixture(t, filepath.Join(stageRoot, "signatures.json"), string(signaturesData))
	manifestDigest := fmt.Sprintf("%x", sha256.Sum256(manifestData))
	manifest, _, err := verifyReleaseEnvelope(release, manifestData, signaturesData)
	if err != nil {
		t.Fatal(err)
	}
	archive, ok := manifest.asset(archiveName)
	if !ok {
		t.Fatalf("signed archive %s is missing", archiveName)
	}
	port := unusedPort(t)
	plan := Plan{
		Schema: planSchema, ID: operationID, PreparedAt: time.Now().UTC(),
		FromVersion: "0.6.10", ToVersion: buildinfo.Version, DatabaseSchema: 0,
		PreviousMaxSchema: 4, TargetMaxSchema: 4, RuntimeRoot: runtimeRoot,
		CodexRoot: codexRoot, KnowledgeRoot: knowledgeRoot, StageRoot: stageRoot,
		SourceRoot: sourceRoot, SourceTreeSHA256: sourceTreeHash,
		BinaryPath: helper, BinarySHA256: binaryHash,
		ArchiveSHA256: archive.SHA256, ReleaseManifestSHA256: manifestDigest,
		SigningKeySHA256: buildinfo.ReleaseEd25519PublicKeySHA256, Port: port,
	}
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		t.Fatal(err)
	}
	if err := acquireOperation(runtimeRoot, operationID, phaseActivationPending); err != nil {
		t.Fatal(err)
	}
	if err := writeState(runtimeRoot, State{
		Status: "ACTIVATION_PENDING", OperationID: operationID, CurrentVersion: "0.6.10", TargetVersion: buildinfo.Version,
	}); err != nil {
		t.Fatal(err)
	}
	planDigest, err := safeio.FileSHA256(planPath(plan))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Activate(context.Background(), planPath(plan), planDigest)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ACTIVE" || result.CurrentVersion != buildinfo.Version || result.PreviousVersion != "0.6.10" {
		t.Fatalf("result = %#v", result)
	}
	assertFixture(t, oldBinary, "new-runtime")
	previous, err := readSnapshot(runtimeRoot, result.SnapshotID)
	if err != nil || previous.FromVersion != "0.6.10" {
		t.Fatalf("snapshot = %#v, %v", previous, err)
	}
	assertFixture(t, filepath.Join(snapshotRoot(runtimeRoot, result.SnapshotID), "runtime", "bin", runtimeBinaryName()), "old-runtime")
	for _, name := range pluginNames {
		if _, err := os.Stat(filepath.Join(codexRoot, "plugins", name, "old.txt")); !os.IsNotExist(err) {
			t.Fatalf("old plugin content remains for %s: %v", name, err)
		}
		assertFixture(t, filepath.Join(codexRoot, "plugins", name, "SKILL.md"), "new-"+name)
	}
}

func fixtureTreeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func executableSuffix() string {
	if filepath.Ext(runtimeBinaryName()) == ".exe" {
		return ".exe"
	}
	return ""
}

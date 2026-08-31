package upgrade

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestActivateKeepsPreviousGenerationSnapshot(t *testing.T) {
	originalInspector := inspectReleaseBinary
	inspectReleaseBinary = func(context.Context, string) (string, int, error) {
		return "0.6.10", 4, nil
	}
	t.Cleanup(func() { inspectReleaseBinary = originalInspector })

	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	codexRoot := filepath.Join(root, "codex")
	knowledgeRoot := filepath.Join(root, "knowledge")
	operationID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	stageRoot := filepath.Join(runtimeRoot, "upgrade", "staging", operationID)
	sourceRoot := filepath.Join(stageRoot, "source", "0tingqu0-ytqjk-marketplace-fixture")
	helper := filepath.Join(stageRoot, "helper"+executableSuffix())
	writeFixture(t, helper, "new-runtime")
	writeFixture(t, filepath.Join(sourceRoot, "go.mod"), "module fixture")
	writeFixture(t, filepath.Join(sourceRoot, "install.ps1"), "fixture")
	writeFixture(t, filepath.Join(sourceRoot, "install.sh"), "fixture")
	for _, name := range pluginNames {
		manifest, _ := json.Marshal(map[string]string{"name": name, "version": "0.6.10"})
		writeFixture(t, filepath.Join(sourceRoot, "plugins", name, ".codex-plugin", "plugin.json"), string(manifest))
		writeFixture(t, filepath.Join(sourceRoot, "plugins", name, "SKILL.md"), "new-"+name)
	}
	oldBinary := filepath.Join(runtimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, oldBinary, "old-runtime")
	oldEntries := make([]map[string]string, 0, len(pluginNames))
	for _, name := range pluginNames {
		pluginRoot := filepath.Join(codexRoot, "plugins", name)
		manifest, _ := json.Marshal(map[string]string{"name": name, "version": "0.6.9"})
		writeFixture(t, filepath.Join(pluginRoot, ".codex-plugin", "plugin.json"), string(manifest))
		writeFixture(t, filepath.Join(pluginRoot, "old.txt"), "old-"+name)
		hash, err := safeio.TreeHash(pluginRoot)
		if err != nil {
			t.Fatal(err)
		}
		oldEntries = append(oldEntries, map[string]string{"name": name, "tree_sha256": hash})
	}
	managed := map[string]any{"schema": "ytqjk-managed-plugins/v1", "version": "0.6.9", "plugins": oldEntries}
	if err := safeio.WriteJSON(filepath.Join(codexRoot, "plugins", ".ytqjk-managed-plugins.json"), managed); err != nil {
		t.Fatal(err)
	}
	binaryHash, _ := safeio.FileSHA256(helper)
	port := unusedPort(t)
	plan := Plan{
		Schema: planSchema, ID: operationID, PreparedAt: time.Now().UTC(),
		FromVersion: "0.6.9", ToVersion: "0.6.10", DatabaseSchema: 0,
		PreviousMaxSchema: 4, TargetMaxSchema: 4, RuntimeRoot: runtimeRoot,
		CodexRoot: codexRoot, KnowledgeRoot: knowledgeRoot, StageRoot: stageRoot,
		SourceRoot: sourceRoot, BinaryPath: helper, BinarySHA256: binaryHash, Port: port,
	}
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		t.Fatal(err)
	}
	if err := writeState(runtimeRoot, State{
		Status: "PREPARED", OperationID: operationID, CurrentVersion: "0.6.9", TargetVersion: "0.6.10",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := Activate(context.Background(), planPath(plan))
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ACTIVE" || result.CurrentVersion != "0.6.10" || result.PreviousVersion != "0.6.9" {
		t.Fatalf("result = %#v", result)
	}
	assertFixture(t, oldBinary, "new-runtime")
	previous, err := readSnapshot(runtimeRoot, result.SnapshotID)
	if err != nil || previous.FromVersion != "0.6.9" {
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

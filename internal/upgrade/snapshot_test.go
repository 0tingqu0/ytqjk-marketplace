package upgrade

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestSnapshotRestoresRuntimePluginsAndDatabase(t *testing.T) {
	root := t.TempDir()
	plan := Plan{
		FromVersion: "0.6.10", ToVersion: "0.7.0", PreviousMaxSchema: 4, DatabaseSchema: 4,
		RuntimeRoot: filepath.Join(root, "runtime"), CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"),
	}
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "old-binary")
	manifest := filepath.Join(plan.CodexRoot, "plugins", ".ytqjk-managed-plugins.json")
	writeFixture(t, manifest, "old-manifest")
	for _, name := range pluginNames {
		writeFixture(t, filepath.Join(plan.CodexRoot, "plugins", name, "content.txt"), "old-"+name)
	}
	databasePath := filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3")
	if err := os.MkdirAll(filepath.Dir(databasePath), 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("CREATE TABLE value(text TEXT); INSERT INTO value VALUES ('old'); PRAGMA user_version=4"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	snapshot, err := captureSnapshot(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "new-binary")
	writeFixture(t, manifest, "new-manifest")
	for _, name := range pluginNames {
		path := filepath.Join(plan.CodexRoot, "plugins", name, "content.txt")
		writeFixture(t, path, "new-"+name)
	}
	database, err = sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("UPDATE value SET text='new'"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	loaded, err := readSnapshot(plan.RuntimeRoot, snapshot.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := restoreSnapshot(plan, loaded, true); err != nil {
		t.Fatal(err)
	}
	assertFixture(t, binary, "old-binary")
	assertFixture(t, manifest, "old-manifest")
	for _, name := range pluginNames {
		assertFixture(t, filepath.Join(plan.CodexRoot, "plugins", name, "content.txt"), "old-"+name)
	}
	database, err = sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err := database.QueryRow("SELECT text FROM value").Scan(&value); err != nil || value != "old" {
		t.Fatalf("database value = %q, %v", value, err)
	}
}

func TestManualRollbackKeepsRolledBackGenerationAsPrevious(t *testing.T) {
	root := t.TempDir()
	base := Plan{
		FromVersion: "0.6.10", ToVersion: "0.7.0", PreviousMaxSchema: 4,
		RuntimeRoot: filepath.Join(root, "runtime"), CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"),
	}
	binary := filepath.Join(base.RuntimeRoot, "bin", runtimeBinaryName())
	writeFixture(t, binary, "generation-a")
	writeFixture(t, filepath.Join(base.CodexRoot, "plugins", ".ytqjk-managed-plugins.json"), "manifest-a")
	for _, name := range pluginNames {
		writeFixture(t, filepath.Join(base.CodexRoot, "plugins", name, "content.txt"), "generation-a")
	}
	target, err := captureSnapshot(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	targetBinaryHash, err := snapshotRuntimeBinarySHA256(target)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, binary, "generation-b")
	writeFixture(t, filepath.Join(base.CodexRoot, "plugins", ".ytqjk-managed-plugins.json"), "manifest-b")
	for _, name := range pluginNames {
		writeFixture(t, filepath.Join(base.CodexRoot, "plugins", name, "content.txt"), "generation-b")
	}
	operationID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	stage := filepath.Join(base.RuntimeRoot, "upgrade", "staging", operationID)
	helper := filepath.Join(stage, "helper"+filepath.Ext(binary))
	writeFixture(t, helper, "helper")
	helperHash, _ := safeio.FileSHA256(helper)
	rollback := RollbackPlan{
		Schema: rollbackPlanSchema, ID: operationID, PreparedAt: time.Now().UTC(),
		CurrentVersion: "0.7.0", TargetVersion: "0.6.10", TargetSnapshotID: target.ID,
		TargetSnapshotManifestSHA256: target.ManifestSHA256,
		TargetBinarySHA256:           targetBinaryHash,
		RuntimeRoot:                  base.RuntimeRoot, CodexRoot: base.CodexRoot, KnowledgeRoot: base.KnowledgeRoot,
		StageRoot: stage, BinaryPath: helper, BinarySHA256: helperHash, Port: unusedPort(t),
	}
	if err := safeio.WriteJSON(rollbackPlanPath(rollback), rollback); err != nil {
		t.Fatal(err)
	}
	if err := acquireOperation(base.RuntimeRoot, operationID, phaseRollbackPending); err != nil {
		t.Fatal(err)
	}
	if err := writeState(base.RuntimeRoot, State{
		Status: "ROLLBACK_PENDING", OperationID: operationID, CurrentVersion: "0.7.0",
		PreviousVersion: "0.6.10", TargetVersion: "0.6.10", SnapshotID: target.ID,
		SnapshotManifestSHA256: target.ManifestSHA256,
	}); err != nil {
		t.Fatal(err)
	}
	planDigest, err := safeio.FileSHA256(rollbackPlanPath(rollback))
	if err != nil {
		t.Fatal(err)
	}
	result, err := Rollback(context.Background(), rollbackPlanPath(rollback), planDigest)
	if err != nil {
		t.Fatal(err)
	}
	if result.CurrentVersion != "0.6.10" || result.PreviousVersion != "0.7.0" || result.SnapshotID == target.ID ||
		!hexDigestPattern.MatchString(result.SnapshotManifestSHA256) {
		t.Fatalf("result = %#v", result)
	}
	assertFixture(t, binary, "generation-a")
	previous, err := readSnapshot(base.RuntimeRoot, result.SnapshotID)
	if err != nil || previous.FromVersion != "0.7.0" {
		t.Fatalf("previous = %#v, %v", previous, err)
	}
	assertFixture(t, filepath.Join(snapshotRoot(base.RuntimeRoot, previous.ID), "runtime", "bin", runtimeBinaryName()), "generation-b")
}

func writeFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFixture(t *testing.T, path, expected string) {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil || string(value) != expected {
		t.Fatalf("%s = %q, %v", path, value, err)
	}
}

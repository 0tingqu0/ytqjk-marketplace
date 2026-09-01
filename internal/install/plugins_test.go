package install

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestMaterializePluginsBundlesBinaryAndRejectsTampering(t *testing.T) {
	sourceRoot := t.TempDir()
	for _, name := range pluginNames {
		directory := filepath.Join(sourceRoot, "plugins", name)
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "plugin.txt"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	binary := filepath.Join(t.TempDir(), "ytqjk")
	binaryContent := []byte("go-runtime")
	if err := os.WriteFile(binary, binaryContent, 0o755); err != nil {
		t.Fatal(err)
	}
	codexRoot := t.TempDir()

	first, err := MaterializePlugins(codexRoot, sourceRoot, binary)
	if err != nil || !first.Changed || len(first.StablePaths) != len(pluginNames) {
		t.Fatalf("first materialization = %#v, %v", first, err)
	}
	manifest, err := readManagedManifest(filepath.Join(codexRoot, "plugins", managedManifest))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range manifest.Plugins {
		target := filepath.Join(codexRoot, "plugins", entry.Name)
		bundled, err := os.ReadFile(filepath.Join(target, "bin", "ytqjk"))
		if err != nil || !bytes.Equal(bundled, binaryContent) {
			t.Fatalf("bundled runtime for %s = %q, %v", entry.Name, bundled, err)
		}
		digest, err := safeio.TreeHash(target)
		if err != nil || digest != entry.TreeSHA256 {
			t.Fatalf("tree hash for %s = %q, %v", entry.Name, digest, err)
		}
	}

	second, err := MaterializePlugins(codexRoot, sourceRoot, binary)
	if err != nil || second.Changed {
		t.Fatalf("idempotent materialization = %#v, %v", second, err)
	}
	tampered := filepath.Join(codexRoot, "plugins", pluginNames[0], "plugin.txt")
	if err := os.WriteFile(tampered, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializePlugins(codexRoot, sourceRoot, binary); err == nil {
		t.Fatal("tampered managed plugin was accepted")
	}
}

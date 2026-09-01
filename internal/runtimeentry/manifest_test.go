package runtimeentry

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestBootstrapLegacyAndAtomicGenerationSwitch(t *testing.T) {
	runtimeRoot := t.TempDir()
	launcher := LauncherPath(runtimeRoot)
	writeRuntimeFixture(t, launcher, "legacy")
	legacy, err := BootstrapLegacy(runtimeRoot, "0.6.10")
	if err != nil {
		t.Fatal(err)
	}
	active, target, err := ReadActive(runtimeRoot)
	if err != nil || active != legacy {
		t.Fatalf("legacy active=%#v target=%q error=%v", active, target, err)
	}
	assertRuntimeFixture(t, target, "legacy")

	source := filepath.Join(t.TempDir(), BinaryName())
	writeRuntimeFixture(t, source, "v0.7.0")
	digest, err := safeio.FileSHA256(source)
	if err != nil {
		t.Fatal(err)
	}
	generation := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	next, err := MaterializeGeneration(runtimeRoot, generation, "0.7.0", source, digest)
	if err != nil {
		t.Fatal(err)
	}
	if err := Activate(runtimeRoot, next); err != nil {
		t.Fatal(err)
	}
	active, target, err = ReadActive(runtimeRoot)
	if err != nil || active.Generation != generation || active.Version != "0.7.0" {
		t.Fatalf("next active=%#v target=%q error=%v", active, target, err)
	}
	assertRuntimeFixture(t, target, "v0.7.0")
	legacyPath, err := GenerationBinaryPath(runtimeRoot, legacy.Generation)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeFixture(t, legacyPath, "legacy")
}

func TestReadActiveRejectsGenerationReplacement(t *testing.T) {
	runtimeRoot := t.TempDir()
	launcher := LauncherPath(runtimeRoot)
	writeRuntimeFixture(t, launcher, "legacy")
	manifest, err := BootstrapLegacy(runtimeRoot, "0.6.10")
	if err != nil {
		t.Fatal(err)
	}
	target, err := GenerationBinaryPath(runtimeRoot, manifest.Generation)
	if err != nil {
		t.Fatal(err)
	}
	writeRuntimeFixture(t, target, "replacement")
	if _, _, err := ReadActive(runtimeRoot); err == nil {
		t.Fatal("active generation replacement was accepted")
	}
}

func TestReadActiveRejectsUnknownManifestField(t *testing.T) {
	runtimeRoot := t.TempDir()
	launcher := LauncherPath(runtimeRoot)
	writeRuntimeFixture(t, launcher, "legacy")
	if _, err := BootstrapLegacy(runtimeRoot, "0.6.10"); err != nil {
		t.Fatal(err)
	}
	path := ActiveManifestPath(runtimeRoot)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data[:len(data)-2], []byte(",\n  \"unknown\": true\n}\n")...)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadActive(runtimeRoot); err == nil {
		t.Fatal("unknown active manifest field was accepted")
	}
}

func writeRuntimeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertRuntimeFixture(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != expected {
		t.Fatalf("runtime fixture %q=%q error=%v", path, data, err)
	}
}

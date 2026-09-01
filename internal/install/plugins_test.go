package install

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestMaterializePluginsUpgradesLegacyManifest(t *testing.T) {
	sourceRoot := t.TempDir()
	codexRoot := t.TempDir()
	pluginsRoot := filepath.Join(codexRoot, "plugins")
	if err := os.MkdirAll(pluginsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := legacyPluginManifest{Schema: "ytqjk-managed-plugins/v1"}
	for _, name := range pluginNames {
		source := filepath.Join(sourceRoot, "plugins", name)
		installed := filepath.Join(pluginsRoot, name)
		for _, directory := range []string{source, installed} {
			if err := os.MkdirAll(directory, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(directory, "plugin.txt"), []byte(name), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		digest, err := legacyTreeHash(installed)
		if err != nil {
			t.Fatal(err)
		}
		legacy.Plugins = append(legacy.Plugins, legacyPluginEntry{Name: name, Version: "0.6.10", TreeSHA256: digest})
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(pluginsRoot, legacyManagedManifest)
	if err := os.WriteFile(legacyPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "ytqjk")
	if err := os.WriteFile(binary, []byte("go-runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err := MaterializePlugins(codexRoot, sourceRoot, binary)
	if err != nil || !result.Changed {
		t.Fatalf("legacy upgrade = %#v, %v", result, err)
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy manifest remains: %v", err)
	}
	if _, err := readManagedManifest(filepath.Join(pluginsRoot, managedManifest)); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializePluginsRejectsTamperedLegacyTree(t *testing.T) {
	codexRoot := t.TempDir()
	pluginsRoot := filepath.Join(codexRoot, "plugins")
	legacy := legacyPluginManifest{Schema: "ytqjk-managed-plugins/v1"}
	for _, name := range pluginNames {
		target := filepath.Join(pluginsRoot, name)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(target, "plugin.txt"), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
		digest, err := legacyTreeHash(target)
		if err != nil {
			t.Fatal(err)
		}
		legacy.Plugins = append(legacy.Plugins, legacyPluginEntry{Name: name, Version: "0.6.10", TreeSHA256: digest})
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsRoot, legacyManagedManifest), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginsRoot, pluginNames[0], "plugin.txt"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MaterializePlugins(codexRoot, t.TempDir(), ""); err == nil {
		t.Fatal("tampered legacy tree was accepted")
	}
}

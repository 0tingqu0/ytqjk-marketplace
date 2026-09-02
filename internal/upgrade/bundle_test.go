package upgrade

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
)

func TestExtractBundleValidatesWindowsZipAndLinuxTarGzip(t *testing.T) {
	tests := []struct {
		name   string
		goos   string
		goarch string
		write  func(*testing.T, string, map[string]string)
	}{
		{name: "windows_zip", goos: "windows", goarch: "amd64", write: writeZip},
		{name: "linux_tar_gzip", goos: "linux", goarch: "amd64", write: writeTarGzip},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			files, binaryName, binaryContent, expectedDigest := bundleFixture(t, test.goos, test.goarch)
			archive := filepath.Join(root, "bundle")
			test.write(t, archive, files)

			source, digest, err := ExtractBundle(
				archive, filepath.Join(root, "source"), buildinfo.Version, test.goos, test.goarch,
			)
			if err != nil || digest != expectedDigest {
				t.Fatalf("bundle digest = %q, %v", digest, err)
			}
			data, err := os.ReadFile(filepath.Join(source, "bin", binaryName))
			if err != nil || string(data) != binaryContent {
				t.Fatalf("bundle binary = %q, %v", data, err)
			}
		})
	}
}

func TestExtractBundleRejectsTraversalForBothArchiveFormats(t *testing.T) {
	tests := []struct {
		name  string
		goos  string
		write func(*testing.T, string, map[string]string)
	}{
		{name: "windows_zip", goos: "windows", write: writeZip},
		{name: "linux_tar_gzip", goos: "linux", write: writeTarGzip},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archive := filepath.Join(root, "unsafe")
			test.write(t, archive, map[string]string{"../escape.txt": "blocked"})
			_, _, err := ExtractBundle(
				archive, filepath.Join(root, "source"), buildinfo.Version, test.goos, "amd64",
			)
			if errorCode(err) != "RELEASE_ARCHIVE_UNSAFE" {
				t.Fatalf("unsafe bundle error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
				t.Fatalf("escape was created: %v", err)
			}
		})
	}
}

func bundleFixture(t *testing.T, goos, goarch string) (map[string]string, string, string, string) {
	t.Helper()
	binaryName, err := bundleBinaryName(goos, goarch)
	if err != nil {
		t.Fatal(err)
	}
	binaryContent := "verified-" + goos + "-binary"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(binaryContent)))
	manifest, err := json.Marshal(map[string]string{
		"schema": "ytqjk-release-bundle/v1", "version": buildinfo.Version,
		"os": goos, "arch": goarch, "binary_sha256": digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"release-manifest.json": string(manifest),
		"bin/" + binaryName:     binaryContent,
	}
	if goos == "windows" {
		files["install.ps1"] = "fixture"
		files["install.cmd"] = "fixture"
	} else {
		files["install.sh"] = "fixture"
	}
	for _, name := range pluginNames {
		pluginManifest, err := json.Marshal(map[string]string{"name": name, "version": buildinfo.Version})
		if err != nil {
			t.Fatal(err)
		}
		files["plugins/"+name+"/.codex-plugin/plugin.json"] = string(pluginManifest)
	}
	return files, binaryName, binaryContent, digest
}

func writeTarGzip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	compressed := gzip.NewWriter(output)
	archive := tar.NewWriter(compressed)
	for name, content := range files {
		header := &tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

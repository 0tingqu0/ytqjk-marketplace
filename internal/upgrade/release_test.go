package upgrade

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestParseReleaseAndVersionOrdering(t *testing.T) {
	asset, err := BinaryAssetName()
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"draft": false, "prerelease": false, "tag_name": "v0.7.0",
		"zipball_url": "https://api.github.com/repos/0tingqu0/ytqjk-marketplace/zipball/v0.7.0",
		"html_url":    "https://github.com/0tingqu0/ytqjk-marketplace/releases/tag/v0.7.0",
		"assets": []map[string]any{
			{"name": asset, "size": 10, "browser_download_url": "https://github.com/0tingqu0/ytqjk-marketplace/releases/download/v0.7.0/" + asset},
			{"name": "SHA256SUMS", "size": 100, "browser_download_url": "https://github.com/0tingqu0/ytqjk-marketplace/releases/download/v0.7.0/SHA256SUMS"},
		},
	}
	encoded, _ := json.Marshal(payload)
	release, err := parseRelease(encoded)
	if err != nil || release.Version != "0.7.0" || release.Assets[asset].Name != asset {
		t.Fatalf("release = %#v, %v", release, err)
	}
	if newer, err := IsNewer("0.7.0", "0.6.10"); err != nil || !newer {
		t.Fatalf("newer = %v, %v", newer, err)
	}
	payload["prerelease"] = true
	encoded, _ = json.Marshal(payload)
	if _, err := parseRelease(encoded); errorCode(err) != "RELEASE_NOT_STABLE" {
		t.Fatalf("prerelease error = %v", err)
	}
	payload["prerelease"] = false
	payload["tag_name"] = "v0.07.0"
	encoded, _ = json.Marshal(payload)
	if _, err := parseRelease(encoded); errorCode(err) != "RELEASE_VERSION_INVALID" {
		t.Fatalf("version error = %v", err)
	}
}

func TestExpectedChecksumIsExact(t *testing.T) {
	digest := bytes.Repeat([]byte{'a'}, 64)
	value, err := ExpectedChecksum([]byte(string(digest)+"  ytqjk-linux-amd64\n"), "ytqjk-linux-amd64")
	if err != nil || value != string(digest) {
		t.Fatalf("checksum = %q, %v", value, err)
	}
	if _, err := ExpectedChecksum([]byte(string(digest)+"  other\n"), "ytqjk-linux-amd64"); errorCode(err) != "RELEASE_CHECKSUM_MISSING" {
		t.Fatalf("missing error = %v", err)
	}
}

func TestExtractReleaseRejectsTraversalAndValidatesManifests(t *testing.T) {
	root := t.TempDir()
	unsafe := filepath.Join(root, "unsafe.zip")
	writeZip(t, unsafe, map[string]string{"../escape.txt": "no"})
	if _, err := ExtractRelease(unsafe, filepath.Join(root, "unsafe-out"), "0.7.0"); errorCode(err) != "RELEASE_ARCHIVE_UNSAFE" {
		t.Fatalf("unsafe error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "escape.txt")); !os.IsNotExist(err) {
		t.Fatalf("escape was created: %v", err)
	}

	valid := filepath.Join(root, "valid.zip")
	top := "0tingqu0-ytqjk-marketplace-fixture/"
	files := map[string]string{
		top + "go.mod": "module fixture\n", top + "install.ps1": "fixture", top + "install.sh": "fixture",
	}
	for _, name := range pluginNames {
		manifest, _ := json.Marshal(map[string]string{"name": name, "version": "0.7.0"})
		files[top+"plugins/"+name+"/.codex-plugin/plugin.json"] = string(manifest)
		files[top+"plugins/"+name+"/SKILL.md"] = "fixture"
	}
	writeZip(t, valid, files)
	source, err := ExtractRelease(valid, filepath.Join(root, "valid-out"), "0.7.0")
	if err != nil || filepath.Base(source) != "0tingqu0-ytqjk-marketplace-fixture" {
		t.Fatalf("source = %q, %v", source, err)
	}
}

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(output)
	for name, content := range files {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func errorCode(err error) string {
	if value, ok := err.(*Error); ok {
		return value.Code
	}
	return ""
}

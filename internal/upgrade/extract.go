package upgrade

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	maxArchiveFiles   = 10_000
	maxExtractedBytes = 256 * 1024 * 1024
)

var (
	topLevelPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hexDigestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
	pluginNames      = []string{"ytqjk-agentic-orchestrator", "ytqjk-knowledge"}
)

func ExtractRelease(archive, destination, expectedVersion string) (string, error) {
	if _, err := parseVersion(expectedVersion); err != nil {
		return "", failure("RELEASE_VERSION_INVALID", err)
	}
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return "", failure("RELEASE_ARCHIVE_INVALID", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxArchiveFiles {
		return "", failure("RELEASE_ARCHIVE_INVALID", nil)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", failure("RELEASE_STAGE_FAILED", err)
	}
	valid := false
	defer func() {
		if !valid {
			_ = os.RemoveAll(destination)
		}
	}()
	var root string
	var extracted uint64
	for _, member := range reader.File {
		parts, pathErr := safeArchiveParts(member.Name)
		if pathErr != nil {
			return "", failure("RELEASE_ARCHIVE_UNSAFE", pathErr)
		}
		if root == "" {
			root = parts[0]
		} else if parts[0] != root {
			return "", failure("RELEASE_ARCHIVE_INVALID", errors.New("multiple roots"))
		}
		mode := member.Mode()
		if member.Flags&1 != 0 || mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return "", failure("RELEASE_ARCHIVE_UNSAFE", nil)
		}
		extracted += member.UncompressedSize64
		if extracted > maxExtractedBytes {
			return "", failure("RELEASE_ARCHIVE_TOO_LARGE", nil)
		}
		target := filepath.Join(append([]string{destination}, parts...)...)
		contained, containErr := safeio.Contained(destination, target)
		if containErr != nil {
			return "", failure("RELEASE_ARCHIVE_UNSAFE", containErr)
		}
		if member.FileInfo().IsDir() || strings.HasSuffix(member.Name, "/") {
			if err := os.MkdirAll(contained, 0o700); err != nil {
				return "", failure("RELEASE_STAGE_FAILED", err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(contained), 0o700); err != nil {
			return "", failure("RELEASE_STAGE_FAILED", err)
		}
		input, err := member.Open()
		if err != nil {
			return "", failure("RELEASE_ARCHIVE_INVALID", err)
		}
		output, err := os.OpenFile(contained, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			input.Close()
			return "", failure("RELEASE_STAGE_FAILED", err)
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, int64(member.UncompressedSize64)+1))
		closeErr := errors.Join(output.Close(), input.Close())
		if copyErr != nil || closeErr != nil || uint64(written) != member.UncompressedSize64 {
			return "", failure("RELEASE_ARCHIVE_INVALID", errors.Join(copyErr, closeErr))
		}
	}
	if !topLevelPattern.MatchString(root) || !strings.Contains(strings.ToLower(root), "ytqjk-marketplace") {
		return "", failure("RELEASE_ARCHIVE_INVALID", errors.New("invalid root"))
	}
	source := filepath.Join(destination, root)
	if err := validateSource(source, expectedVersion); err != nil {
		return "", err
	}
	valid = true
	return source, nil
}

func safeArchiveParts(name string) ([]string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") {
		return nil, errors.New("unsafe archive path")
	}
	trimmed := strings.TrimSuffix(name, "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 {
		return nil, errors.New("unsafe archive path")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return nil, errors.New("unsafe archive path")
		}
	}
	return parts, nil
}

func validateSource(source, expectedVersion string) error {
	for _, required := range []string{"go.mod", "install.ps1", "install.sh"} {
		info, err := os.Lstat(filepath.Join(source, required))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return failure("RELEASE_SOURCE_INVALID", err)
		}
	}
	for _, name := range pluginNames {
		manifestPath := filepath.Join(source, "plugins", name, ".codex-plugin", "plugin.json")
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			return failure("RELEASE_MANIFEST_INVALID", err)
		}
		var manifest struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Name != name || manifest.Version != expectedVersion {
			return failure("RELEASE_MANIFEST_INVALID", nil)
		}
		if _, err := safeio.TreeHash(filepath.Join(source, "plugins", name)); err != nil {
			return failure("RELEASE_SOURCE_INVALID", err)
		}
	}
	return nil
}

func ExpectedChecksum(data []byte, assetName string) (string, error) {
	if strings.ContainsAny(assetName, "\r\n/\\") || assetName == "" {
		return "", failure("RELEASE_CHECKSUM_INVALID", nil)
	}
	result := ""
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return "", failure("RELEASE_CHECKSUM_INVALID", nil)
		}
		name := strings.TrimPrefix(fields[1], "*")
		if !hexDigestPattern.MatchString(fields[0]) || strings.ContainsAny(name, "/\\") {
			return "", failure("RELEASE_CHECKSUM_INVALID", nil)
		}
		if name == assetName {
			if result != "" {
				return "", failure("RELEASE_CHECKSUM_INVALID", errors.New("duplicate checksum"))
			}
			result = fields[0]
		}
	}
	if result == "" {
		return "", failure("RELEASE_CHECKSUM_MISSING", fmt.Errorf("checksum missing for %s", assetName))
	}
	return result, nil
}

package upgrade

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func ExtractBundle(
	archive string,
	destination string,
	expectedVersion string,
	goos string,
	goarch string,
) (string, string, error) {
	assetName, err := archiveAssetName(goos, goarch)
	if err != nil {
		return "", "", err
	}
	if _, err := parseVersion(expectedVersion); err != nil {
		return "", "", failure("RELEASE_VERSION_INVALID", err)
	}
	if err := os.Mkdir(destination, 0o700); err != nil {
		return "", "", failure("RELEASE_STAGE_FAILED", err)
	}
	valid := false
	defer func() {
		if !valid {
			_ = os.RemoveAll(destination)
		}
	}()
	if strings.HasSuffix(assetName, ".zip") {
		err = extractBundleZip(archive, destination)
	} else {
		err = extractBundleTarGzip(archive, destination)
	}
	if err != nil {
		return "", "", err
	}
	binaryDigest, err := validateReleaseBundle(destination, expectedVersion, goos, goarch)
	if err != nil {
		return "", "", err
	}
	valid = true
	return destination, binaryDigest, nil
}

func extractBundleZip(archive, destination string) error {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return failure("RELEASE_ARCHIVE_INVALID", err)
	}
	defer reader.Close()
	if len(reader.File) == 0 || len(reader.File) > maxArchiveFiles {
		return failure("RELEASE_ARCHIVE_INVALID", nil)
	}
	var extracted uint64
	for _, member := range reader.File {
		parts, err := bundleArchiveParts(member.Name)
		if err != nil {
			return failure("RELEASE_ARCHIVE_UNSAFE", err)
		}
		mode := member.Mode()
		if member.Flags&1 != 0 || mode&os.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
			return failure("RELEASE_ARCHIVE_UNSAFE", nil)
		}
		if len(parts) == 0 {
			if mode.IsDir() {
				continue
			}
			return failure("RELEASE_ARCHIVE_UNSAFE", nil)
		}
		extracted += member.UncompressedSize64
		if extracted > maxExtractedBytes {
			return failure("RELEASE_ARCHIVE_TOO_LARGE", nil)
		}
		target, err := bundleTarget(destination, parts)
		if err != nil {
			return err
		}
		if mode.IsDir() || strings.HasSuffix(member.Name, "/") {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return failure("RELEASE_STAGE_FAILED", err)
			}
			continue
		}
		input, err := member.Open()
		if err != nil {
			return failure("RELEASE_ARCHIVE_INVALID", err)
		}
		writeErr := writeBundleMember(input, target, int64(member.UncompressedSize64))
		closeErr := input.Close()
		if writeErr != nil || closeErr != nil {
			return failure("RELEASE_ARCHIVE_INVALID", errors.Join(writeErr, closeErr))
		}
	}
	return nil
}

func extractBundleTarGzip(archive, destination string) error {
	file, err := os.Open(archive)
	if err != nil {
		return failure("RELEASE_ARCHIVE_INVALID", err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return failure("RELEASE_ARCHIVE_INVALID", err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	count := 0
	var extracted uint64
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return failure("RELEASE_ARCHIVE_INVALID", err)
		}
		count++
		if count > maxArchiveFiles || header.Size < 0 {
			return failure("RELEASE_ARCHIVE_INVALID", nil)
		}
		parts, err := bundleArchiveParts(header.Name)
		if err != nil {
			return failure("RELEASE_ARCHIVE_UNSAFE", err)
		}
		isDirectory := header.Typeflag == tar.TypeDir
		isRegular := header.Typeflag == tar.TypeReg || header.Typeflag == tar.TypeRegA
		if !isDirectory && !isRegular {
			return failure("RELEASE_ARCHIVE_UNSAFE", nil)
		}
		if len(parts) == 0 {
			if isDirectory {
				continue
			}
			return failure("RELEASE_ARCHIVE_UNSAFE", nil)
		}
		extracted += uint64(header.Size)
		if extracted > maxExtractedBytes {
			return failure("RELEASE_ARCHIVE_TOO_LARGE", nil)
		}
		target, err := bundleTarget(destination, parts)
		if err != nil {
			return err
		}
		if isDirectory {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return failure("RELEASE_STAGE_FAILED", err)
			}
			continue
		}
		if err := writeBundleMember(reader, target, header.Size); err != nil {
			return failure("RELEASE_ARCHIVE_INVALID", err)
		}
	}
	if count == 0 {
		return failure("RELEASE_ARCHIVE_INVALID", nil)
	}
	return nil
}

func bundleArchiveParts(name string) ([]string, error) {
	if name == "" || strings.Contains(name, "\\") || strings.HasPrefix(name, "/") || strings.ContainsRune(name, 0) {
		return nil, errors.New("unsafe archive path")
	}
	trimmed := strings.TrimSuffix(strings.TrimPrefix(name, "./"), "/")
	if trimmed == "" {
		return nil, nil
	}
	parts := strings.Split(trimmed, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.Contains(part, ":") {
			return nil, errors.New("unsafe archive path")
		}
	}
	return parts, nil
}

func bundleTarget(destination string, parts []string) (string, error) {
	target := filepath.Join(append([]string{destination}, parts...)...)
	contained, err := safeio.Contained(destination, target)
	if err != nil {
		return "", failure("RELEASE_ARCHIVE_UNSAFE", err)
	}
	return contained, nil
}

func writeBundleMember(input io.Reader, target string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, size+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil || written != size {
		return errors.Join(copyErr, closeErr, errors.New("archive member size mismatch"))
	}
	return nil
}

func validateReleaseBundle(source, expectedVersion, goos, goarch string) (string, error) {
	var manifest struct {
		Schema       string `json:"schema"`
		Version      string `json:"version"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		BinarySHA256 string `json:"binary_sha256"`
	}
	data, err := os.ReadFile(filepath.Join(source, "release-manifest.json"))
	if err != nil {
		return "", failure("RELEASE_BUNDLE_INVALID", err)
	}
	if err := decodeStrictJSON(data, &manifest); err != nil {
		return "", failure("RELEASE_BUNDLE_INVALID", err)
	}
	if manifest.Schema != "ytqjk-release-bundle/v1" ||
		manifest.Version != expectedVersion || manifest.OS != goos || manifest.Arch != goarch ||
		!hexDigestPattern.MatchString(manifest.BinarySHA256) {
		return "", failure("RELEASE_BUNDLE_INVALID", nil)
	}
	binaryName, err := bundleBinaryName(goos, goarch)
	if err != nil {
		return "", err
	}
	binaryPath := filepath.Join(source, "bin", binaryName)
	info, err := os.Lstat(binaryPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", failure("RELEASE_BINARY_INVALID", err)
	}
	binaryDigest, err := safeio.FileSHA256(binaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(binaryDigest), []byte(manifest.BinarySHA256)) != 1 {
		return "", failure("RELEASE_BINARY_INVALID", err)
	}
	required := []string{"release-manifest.json"}
	if goos == "windows" {
		required = append(required, "install.ps1", "install.cmd")
	} else {
		required = append(required, "install.sh")
	}
	for _, name := range required {
		info, err := os.Lstat(filepath.Join(source, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", failure("RELEASE_BUNDLE_INVALID", err)
		}
	}
	for _, name := range pluginNames {
		data, err := os.ReadFile(filepath.Join(source, "plugins", name, ".codex-plugin", "plugin.json"))
		if err != nil {
			return "", failure("RELEASE_MANIFEST_INVALID", err)
		}
		var plugin struct{ Name, Version string }
		if json.Unmarshal(data, &plugin) != nil || plugin.Name != name || plugin.Version != expectedVersion {
			return "", failure("RELEASE_MANIFEST_INVALID", nil)
		}
		if _, err := safeio.TreeHash(filepath.Join(source, "plugins", name)); err != nil {
			return "", failure("RELEASE_BUNDLE_INVALID", err)
		}
	}
	return binaryDigest, nil
}

func bundleBinaryName(goos, goarch string) (string, error) {
	if goarch != "amd64" || goos != "linux" && goos != "windows" {
		return "", failure("PLATFORM_NOT_SUPPORTED", nil)
	}
	if goos == "windows" {
		return "ytqjk.exe", nil
	}
	return "ytqjk", nil
}

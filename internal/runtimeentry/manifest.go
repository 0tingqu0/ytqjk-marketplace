package runtimeentry

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const manifestSchema = "ytqjk-runtime-active/v1"

var (
	digestPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	versionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
)

// Manifest is the single atomic runtime entry pointer. Generation contents are
// immutable; activation replaces only this small record.
type Manifest struct {
	Schema       string    `json:"schema"`
	Generation   string    `json:"generation"`
	Version      string    `json:"version"`
	BinarySHA256 string    `json:"binary_sha256"`
	ActivatedAt  time.Time `json:"activated_at"`
}

func BinaryName() string {
	if runtime.GOOS == "windows" {
		return "ytqjk.exe"
	}
	return "ytqjk"
}

func ActiveManifestPath(runtimeRoot string) string {
	return filepath.Join(runtimeRoot, "active.json")
}

func LauncherPath(runtimeRoot string) string {
	return filepath.Join(runtimeRoot, "bin", BinaryName())
}

func GenerationBinaryPath(runtimeRoot, generation string) (string, error) {
	root, err := canonicalRuntimeRoot(runtimeRoot)
	if err != nil || !digestPattern.MatchString(generation) {
		return "", errors.Join(errors.New("runtime generation is invalid"), err)
	}
	return safeio.Contained(root, filepath.Join(root, "generations", generation, "bin", BinaryName()))
}

func MaterializeGeneration(
	runtimeRoot, generation, version, source, expectedSHA256 string,
) (Manifest, error) {
	if !versionPattern.MatchString(version) || !digestPattern.MatchString(expectedSHA256) {
		return Manifest{}, errors.New("runtime generation metadata is invalid")
	}
	target, err := GenerationBinaryPath(runtimeRoot, generation)
	if err != nil {
		return Manifest{}, err
	}
	if err := atomicCopyVerified(source, target, expectedSHA256); err != nil {
		return Manifest{}, err
	}
	return Manifest{
		Schema: manifestSchema, Generation: generation, Version: version,
		BinarySHA256: expectedSHA256, ActivatedAt: time.Now().UTC(),
	}, nil
}

// BootstrapLegacy freezes the fixed-path pre-v0.7 binary as an immutable
// generation. Existing valid manifests are returned unchanged.
func BootstrapLegacy(runtimeRoot, version string) (Manifest, error) {
	manifest, _, err := ReadActive(runtimeRoot)
	if err == nil {
		return manifest, nil
	}
	if !errors.Is(err, os.ErrNotExist) || !versionPattern.MatchString(version) {
		return Manifest{}, errors.Join(errors.New("legacy runtime cannot be bound"), err)
	}
	launcher := LauncherPath(runtimeRoot)
	digest, err := regularFileSHA256(launcher)
	if err != nil {
		return Manifest{}, err
	}
	seed := sha256.Sum256([]byte(version + "\x00" + digest))
	generation := hex.EncodeToString(seed[:])
	manifest, err = MaterializeGeneration(runtimeRoot, generation, version, launcher, digest)
	if err != nil {
		return Manifest{}, err
	}
	if err := Activate(runtimeRoot, manifest); err != nil {
		return Manifest{}, err
	}
	manifest, _, err = ReadActive(runtimeRoot)
	return manifest, err
}

func InstallLauncher(runtimeRoot, source, expectedSHA256 string) error {
	root, err := canonicalRuntimeRoot(runtimeRoot)
	if err != nil || !digestPattern.MatchString(expectedSHA256) {
		return errors.Join(errors.New("runtime launcher metadata is invalid"), err)
	}
	target, err := safeio.Contained(root, LauncherPath(root))
	if err != nil {
		return err
	}
	return atomicCopyVerified(source, target, expectedSHA256)
}

func Activate(runtimeRoot string, manifest Manifest) error {
	root, err := canonicalRuntimeRoot(runtimeRoot)
	if err != nil {
		return err
	}
	if err := validateManifest(manifest); err != nil {
		return err
	}
	target, err := GenerationBinaryPath(root, manifest.Generation)
	if err != nil {
		return err
	}
	digest, err := regularFileSHA256(target)
	if err != nil || digest != manifest.BinarySHA256 {
		return errors.Join(errors.New("active generation binary is unavailable or changed"), err)
	}
	manifest.Schema = manifestSchema
	manifest.ActivatedAt = time.Now().UTC()
	return safeio.WriteJSON(ActiveManifestPath(root), manifest)
}

func ReadActive(runtimeRoot string) (Manifest, string, error) {
	root, err := canonicalRuntimeRoot(runtimeRoot)
	if err != nil {
		return Manifest{}, "", err
	}
	data, err := os.ReadFile(ActiveManifestPath(root))
	if err != nil {
		return Manifest{}, "", err
	}
	var manifest Manifest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Manifest{}, "", errors.Join(errors.New("active runtime manifest is invalid"), err)
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, "", err
	}
	target, err := GenerationBinaryPath(root, manifest.Generation)
	if err != nil {
		return Manifest{}, "", err
	}
	digest, err := regularFileSHA256(target)
	if err != nil || digest != manifest.BinarySHA256 {
		return Manifest{}, "", errors.Join(errors.New("active runtime binary digest differs from manifest"), err)
	}
	return manifest, target, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Schema != manifestSchema || !digestPattern.MatchString(manifest.Generation) ||
		!versionPattern.MatchString(manifest.Version) || !digestPattern.MatchString(manifest.BinarySHA256) ||
		manifest.ActivatedAt.IsZero() || manifest.ActivatedAt.Location() != time.UTC {
		return errors.New("active runtime manifest is invalid")
	}
	return nil
}

func canonicalRuntimeRoot(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("runtime root is empty")
	}
	root, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(errors.New("runtime root is unavailable or unsafe"), err)
	}
	return root, nil
}

func atomicCopyVerified(source, target, expectedSHA256 string) error {
	digest, err := regularFileSHA256(source)
	if err != nil || digest != expectedSHA256 {
		return errors.Join(errors.New("runtime source digest differs from expectation"), err)
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := safeio.AtomicWrite(target, data, 0o700); err != nil {
		return err
	}
	digest, err = regularFileSHA256(target)
	if err != nil || digest != expectedSHA256 {
		return errors.Join(errors.New("materialized runtime digest differs from expectation"), err)
	}
	return nil
}

func regularFileSHA256(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(errors.New("runtime binary is unavailable or unsafe"), err)
	}
	return safeio.FileSHA256(path)
}

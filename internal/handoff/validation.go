package handoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func loadManifest(bundle string) (Manifest, error) {
	var manifest Manifest
	path := filepath.Join(bundle, "manifest.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("invalid bundle manifest")
	}
	if err := safeio.ReadJSON(path, &manifest); err != nil {
		return manifest, fmt.Errorf("invalid bundle manifest: %w", err)
	}
	if manifest.Format != Format {
		return manifest, errors.New("unsupported bundle format")
	}
	digest, err := manifestDigest(manifest)
	if err != nil || digest != manifest.BundleSHA256 {
		return manifest, errors.New("bundle manifest hash mismatch")
	}
	return manifest, nil
}

func validatePayload(bundle string, manifest Manifest) (string, []string, error) {
	allowed, err := normalizedUnique(manifest.Allowlist)
	if err != nil || len(allowed) != len(manifest.Allowlist) {
		return "", nil, errors.New("malformed bundle manifest")
	}
	tracked, err := normalizedUnique(manifest.Tracked.Paths)
	if err != nil || len(tracked) != len(manifest.Tracked.Paths) {
		return "", nil, errors.New("malformed bundle manifest")
	}
	patch := filepath.Join(bundle, "tracked.patch")
	info, err := os.Lstat(patch)
	if err != nil || manifest.Tracked.Patch != "tracked.patch" || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("tracked patch is missing or unsafe")
	}
	digest, err := safeio.FileSHA256(patch)
	if err != nil || manifest.Tracked.Bytes != info.Size() || manifest.Tracked.SHA256 != digest {
		return "", nil, errors.New("tracked patch hash mismatch")
	}
	changed := append([]string(nil), tracked...)
	for _, record := range manifest.Untracked {
		relative, err := normalizePath(record.Path)
		if err != nil {
			return "", nil, errors.New("malformed untracked manifest entry")
		}
		payload := filepath.Join(bundle, "untracked", filepath.FromSlash(relative))
		payloadInfo, err := os.Lstat(payload)
		if err != nil || !payloadInfo.Mode().IsRegular() || payloadInfo.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("untracked payload is missing or unsafe: %s", relative)
		}
		inside, relErr := filepath.Rel(bundle, payload)
		if relErr != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
			return "", nil, fmt.Errorf("untracked payload is missing or unsafe: %s", relative)
		}
		payloadDigest, err := safeio.FileSHA256(payload)
		if err != nil || record.Bytes != payloadInfo.Size() || record.SHA256 != payloadDigest {
			return "", nil, fmt.Errorf("untracked payload hash mismatch: %s", relative)
		}
		changed = append(changed, relative)
	}
	sort.Strings(changed)
	if len(changed) != len(unique(changed)) {
		return "", nil, errors.New("bundle paths are duplicated or outside the allowlist")
	}
	allowedSet := stringSet(allowed)
	for _, path := range changed {
		if !allowedSet[path] {
			return "", nil, errors.New("bundle paths are duplicated or outside the allowlist")
		}
	}
	return patch, changed, nil
}

func patchPaths(repo, patch string) ([]string, error) {
	info, err := os.Stat(patch)
	if err != nil || info.Size() == 0 {
		return nil, err
	}
	output, err := gitOutput(repo, "apply", "--numstat", "-z", patch)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, record := range bytes.Split(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		fields := bytes.SplitN(record, []byte{'\t'}, 3)
		if len(fields) != 3 {
			return nil, errors.New("tracked patch contains an unsupported path record")
		}
		path, err := normalizePath(string(fields[2]))
		if err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return uniqueSorted(paths), nil
}

func repositoryRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	root, err := gitText(absolute, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return filepath.Abs(root)
}

func outsideRepository(repo, bundle string) (string, error) {
	absolute, err := filepath.Abs(bundle)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if err := requireExistingRealDirectory(parent); err != nil {
		return "", fmt.Errorf("handoff bundle parent must be pre-provisioned: %w", err)
	}
	resolvedRepo, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	resolvedBundle := filepath.Join(resolvedParent, filepath.Base(absolute))
	relative, err := filepath.Rel(resolvedRepo, resolvedBundle)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("handoff bundle must be outside the repository")
	}
	return absolute, nil
}

func requireExistingRealDirectory(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("path contains a non-directory or symbolic-link ancestor")
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func normalizePath(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("unsafe repository path: %q", value)
	}
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.HasPrefix(value, "/") || (len(value) > 1 && value[1] == ':') {
		return "", fmt.Errorf("unsafe repository path: %q", value)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", fmt.Errorf("unsafe repository path: %q", value)
		}
		if strings.EqualFold(part, ".git") {
			return "", fmt.Errorf("Git metadata is not a handoff path: %q", value)
		}
	}
	if clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))); clean != value {
		return "", fmt.Errorf("unsafe repository path: %q", value)
	}
	return value, nil
}

func normalizedUnique(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	for _, value := range values {
		normalized, err := normalizePath(value)
		if err != nil {
			return nil, err
		}
		result = append(result, normalized)
	}
	return uniqueSorted(result), nil
}

func manifestDigest(manifest Manifest) (string, error) {
	encodedManifest, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	var value map[string]any
	if err := json.Unmarshal(encodedManifest, &value); err != nil {
		return "", err
	}
	delete(value, "bundle_sha256")
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func fullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

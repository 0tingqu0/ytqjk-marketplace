package handoff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const Format = "ytqjk-handoff-v1"

type TrackedPayload struct {
	Paths  []string `json:"paths"`
	Patch  string   `json:"patch"`
	Bytes  int64    `json:"bytes"`
	SHA256 string   `json:"sha256"`
}

type FilePayload struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format       string         `json:"format"`
	BaseHead     string         `json:"base_head"`
	Allowlist    []string       `json:"allowlist"`
	Tracked      TrackedPayload `json:"tracked"`
	Untracked    []FilePayload  `json:"untracked"`
	BundleSHA256 string         `json:"bundle_sha256"`
}

type ExportResult struct {
	Bundle       string   `json:"bundle"`
	BundleSHA256 string   `json:"bundle_sha256"`
	BaseHead     string   `json:"base_head"`
	Paths        []string `json:"paths"`
}

type ApplyResult struct {
	BundleSHA256       string   `json:"bundle_sha256"`
	BaseHead           string   `json:"base_head"`
	IntegrationHead    string   `json:"integration_head"`
	StagedPaths        []string `json:"staged_paths"`
	StagedSnapshotHash string   `json:"staged_snapshot_sha256"`
}

func Export(repoArg, bundleArg string, allowlist []string) (ExportResult, error) {
	repo, err := repositoryRoot(repoArg)
	if err != nil {
		return ExportResult{}, err
	}
	bundle, err := outsideRepository(repo, bundleArg)
	if err != nil {
		return ExportResult{}, err
	}
	if _, err := os.Lstat(bundle); err == nil {
		return ExportResult{}, fmt.Errorf("bundle already exists: %s", bundle)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ExportResult{}, err
	}
	allowed, err := normalizedUnique(allowlist)
	if err != nil {
		return ExportResult{}, err
	}
	if len(allowed) == 0 {
		return ExportResult{}, errors.New("at least one --path allowlist entry is required")
	}
	if err := ensureWorkerIndexClean(repo); err != nil {
		return ExportResult{}, err
	}
	tracked, err := gitPaths(repo, "diff", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return ExportResult{}, err
	}
	untracked, err := gitPaths(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return ExportResult{}, err
	}
	changed := union(tracked, untracked)
	if len(changed) == 0 {
		return ExportResult{}, errors.New("worker has no changes to export")
	}
	allowedSet := stringSet(allowed)
	var outside []string
	for _, path := range changed {
		if !allowedSet[path] {
			outside = append(outside, path)
		}
	}
	if len(outside) > 0 {
		return ExportResult{}, fmt.Errorf("changes outside allowlist: %s", strings.Join(outside, ", "))
	}
	patch, err := gitOutput(repo, "diff", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-renames", "--no-textconv", "--")
	if err != nil {
		return ExportResult{}, err
	}
	records := make([]FilePayload, 0, len(untracked))
	for _, relative := range untracked {
		source := filepath.Join(repo, filepath.FromSlash(relative))
		info, err := os.Lstat(source)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return ExportResult{}, fmt.Errorf("untracked payload is not a regular file: %s", relative)
		}
		digest, err := safeio.FileSHA256(source)
		if err != nil {
			return ExportResult{}, err
		}
		records = append(records, FilePayload{Path: relative, Bytes: info.Size(), SHA256: digest})
	}
	head, err := gitText(repo, "rev-parse", "HEAD")
	if err != nil {
		return ExportResult{}, err
	}
	manifest := Manifest{
		Format: Format, BaseHead: head, Allowlist: allowed,
		Tracked:   TrackedPayload{Paths: tracked, Patch: "tracked.patch", Bytes: int64(len(patch)), SHA256: safeio.SHA256(patch)},
		Untracked: records,
	}
	manifest.BundleSHA256, err = manifestDigest(manifest)
	if err != nil {
		return ExportResult{}, err
	}
	if err := os.MkdirAll(filepath.Dir(bundle), 0o755); err != nil {
		return ExportResult{}, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(bundle), "."+filepath.Base(bundle)+"-*")
	if err != nil {
		return ExportResult{}, err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := safeio.AtomicWrite(filepath.Join(temporary, "tracked.patch"), patch, 0o600); err != nil {
		return ExportResult{}, err
	}
	for _, record := range records {
		source := filepath.Join(repo, filepath.FromSlash(record.Path))
		target := filepath.Join(temporary, "untracked", filepath.FromSlash(record.Path))
		if err := safeio.CopyFile(source, target, 0o600); err != nil {
			return ExportResult{}, err
		}
	}
	if err := safeio.WriteJSON(filepath.Join(temporary, "manifest.json"), manifest); err != nil {
		return ExportResult{}, err
	}
	if err := os.Rename(temporary, bundle); err != nil {
		return ExportResult{}, err
	}
	keep = true
	return ExportResult{Bundle: bundle, BundleSHA256: manifest.BundleSHA256, BaseHead: head, Paths: changed}, nil
}

func Apply(repoArg, bundleArg string) (result ApplyResult, returnedErr error) {
	repo, err := repositoryRoot(repoArg)
	if err != nil {
		return result, err
	}
	bundle, err := outsideRepository(repo, bundleArg)
	if err != nil {
		return result, err
	}
	status, err := gitOutput(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return result, err
	}
	if len(status) != 0 {
		return result, errors.New("integration worktree must be clean")
	}
	manifest, err := loadManifest(bundle)
	if err != nil {
		return result, err
	}
	patch, changed, err := validatePayload(bundle, manifest)
	if err != nil {
		return result, err
	}
	if !fullObjectID(manifest.BaseHead) {
		return result, errors.New("bundle base HEAD is not a full object ID")
	}
	if _, err := gitOutput(repo, "merge-base", "--is-ancestor", manifest.BaseHead, "HEAD"); err != nil {
		return result, errors.New("bundle base HEAD is not an ancestor of integration HEAD")
	}
	trackedPaths := append([]string(nil), manifest.Tracked.Paths...)
	sort.Strings(trackedPaths)
	actualPatchPaths, err := patchPaths(repo, patch)
	if err != nil {
		return result, err
	}
	if !equalStrings(actualPatchPaths, trackedPaths) {
		return result, errors.New("tracked patch paths do not match the manifest")
	}
	trackedSet := stringSet(trackedPaths)
	var untrackedPaths []string
	for _, path := range changed {
		if !trackedSet[path] {
			untrackedPaths = append(untrackedPaths, path)
		}
	}
	for _, relative := range untrackedPaths {
		target := filepath.Join(repo, filepath.FromSlash(relative))
		if _, err := os.Lstat(target); err == nil {
			return result, fmt.Errorf("untracked target already exists: %s", relative)
		} else if !errors.Is(err, os.ErrNotExist) {
			return result, err
		}
		contained, err := filepath.Rel(repo, target)
		if err != nil || contained == ".." || strings.HasPrefix(contained, ".."+string(filepath.Separator)) {
			return result, fmt.Errorf("untracked target escapes the repository: %s", relative)
		}
		ignored := runGit(repo, "check-ignore", "--quiet", "--no-index", "--", relative)
		if ignored.err == nil {
			return result, fmt.Errorf("untracked target is ignored: %s", relative)
		}
		if ignored.exitCode != 1 {
			return result, fmt.Errorf("unable to check ignore rules for: %s", relative)
		}
		if err := safeParents(repo, target); err != nil {
			return result, fmt.Errorf("untracked target has an unsafe parent: %s", relative)
		}
		source := filepath.Join(bundle, "untracked", filepath.FromSlash(relative))
		if _, err := gitOutput(repo, "hash-object", "--path="+relative, source); err != nil {
			return result, fmt.Errorf("untracked payload filter failed: %s: %w", relative, err)
		}
	}
	patchInfo, err := os.Stat(patch)
	if err != nil {
		return result, err
	}
	if patchInfo.Size() > 0 {
		check := runGit(repo, "apply", "--check", "--3way", "--index", "--binary", patch)
		if check.err != nil || bytes.Contains(bytes.ToLower(append(check.stderr, check.stdout...)), []byte("with conflicts")) {
			return result, fmt.Errorf("tracked patch does not apply cleanly: %s", check.detail())
		}
	}
	wrote := false
	defer func() {
		if returnedErr != nil && wrote {
			if rollbackErr := rollback(repo, changed, untrackedPaths); rollbackErr != nil {
				returnedErr = fmt.Errorf("%w; rollback failed: %v", returnedErr, rollbackErr)
			}
		}
	}()
	if patchInfo.Size() > 0 {
		applied := runGit(repo, "apply", "--3way", "--index", "--binary", patch)
		if applied.err != nil {
			return result, fmt.Errorf("tracked patch application failed: %s", applied.detail())
		}
		wrote = true
	}
	for _, relative := range untrackedPaths {
		source := filepath.Join(bundle, "untracked", filepath.FromSlash(relative))
		target := filepath.Join(repo, filepath.FromSlash(relative))
		if err := safeio.CopyFile(source, target, 0o600); err != nil {
			return result, err
		}
		wrote = true
	}
	if len(untrackedPaths) > 0 {
		arguments := []string{"add", "--"}
		for _, path := range untrackedPaths {
			arguments = append(arguments, ":(top,literal)"+path)
		}
		if _, err := gitOutput(repo, arguments...); err != nil {
			return result, err
		}
	}
	staged, err := gitPaths(repo, "diff", "--cached", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return result, err
	}
	if !equalStrings(staged, changed) {
		return result, errors.New("staged paths do not match the handoff manifest")
	}
	if diff := runGit(repo, "diff", "--quiet", "--exit-code", "--"); diff.err != nil {
		return result, errors.New("integration produced unexpected unstaged content")
	}
	others, err := gitPaths(repo, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || len(others) > 0 {
		return result, errors.New("integration produced unexpected untracked content")
	}
	snapshot, err := gitOutput(repo, "diff", "--cached", "--binary", "--full-index", "--no-color", "--no-ext-diff", "--no-renames", "--no-textconv", "--")
	if err != nil {
		return result, err
	}
	integrationHead, err := gitText(repo, "rev-parse", "HEAD")
	if err != nil {
		return result, err
	}
	return ApplyResult{
		BundleSHA256: manifest.BundleSHA256, BaseHead: manifest.BaseHead, IntegrationHead: integrationHead,
		StagedPaths: staged, StagedSnapshotHash: safeio.SHA256(snapshot),
	}, nil
}

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

func ensureWorkerIndexClean(repo string) error {
	status, err := gitOutput(repo, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) > 1 && (record[0] != ' ' || record[1] == 'A') {
			return errors.New("worker index is not clean; workers must not stage changes")
		}
	}
	if result := runGit(repo, "diff", "--cached", "--quiet", "--exit-code", "--"); result.err != nil {
		return errors.New("worker index is not clean; workers must not stage changes")
	}
	unresolved, err := gitOutput(repo, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return err
	}
	if len(unresolved) > 0 {
		return errors.New("worker has unresolved paths")
	}
	return nil
}

func rollback(repo string, changed, untracked []string) error {
	untrackedSet := stringSet(untracked)
	tracked := make([]string, 0, len(changed))
	for _, path := range changed {
		if !untrackedSet[path] {
			tracked = append(tracked, path)
		}
	}
	if len(tracked) > 0 {
		arguments := []string{"restore", "--source=HEAD", "--staged", "--worktree", "--"}
		for _, path := range tracked {
			arguments = append(arguments, ":(top,literal)"+path)
		}
		if _, err := gitOutput(repo, arguments...); err != nil {
			return fmt.Errorf("index/worktree restore failed: %w", err)
		}
	}
	for _, relative := range untracked {
		target := filepath.Join(repo, filepath.FromSlash(relative))
		if info, err := os.Lstat(target); err == nil && (info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
			if err := os.Remove(target); err != nil {
				return err
			}
			removeEmptyParents(repo, filepath.Dir(target))
		}
	}
	status, err := gitOutput(repo, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil || len(status) != 0 {
		return errors.New("integration worktree is not clean after rollback")
	}
	return nil
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
	relative, err := filepath.Rel(repo, absolute)
	if err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("handoff bundle must be outside the repository")
	}
	return absolute, nil
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

type gitResult struct {
	stdout, stderr []byte
	err            error
	exitCode       int
}

func runGit(repo string, arguments ...string) gitResult {
	command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
	command.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err, exitCode: exitCode}
}

func (result gitResult) detail() string {
	value := strings.TrimSpace(string(result.stderr))
	if value == "" {
		value = strings.TrimSpace(string(result.stdout))
	}
	return value
}

func gitOutput(repo string, arguments ...string) ([]byte, error) {
	result := runGit(repo, arguments...)
	if result.err != nil {
		detail := result.detail()
		if detail == "" {
			detail = "git " + strings.Join(arguments, " ") + " failed"
		}
		return nil, errors.New(detail)
	}
	return result.stdout, nil
}

func gitText(repo string, arguments ...string) (string, error) {
	value, err := gitOutput(repo, arguments...)
	return strings.TrimSpace(string(value)), err
}

func gitPaths(repo string, arguments ...string) ([]string, error) {
	value, err := gitOutput(repo, arguments...)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, item := range bytes.Split(value, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path, err := normalizePath(string(item))
		if err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return uniqueSorted(result), nil
}

func safeParents(root, target string) error {
	for parent := filepath.Dir(target); parent != root; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe parent")
		}
		if next := filepath.Dir(parent); next == parent {
			return errors.New("unsafe parent")
		}
	}
	return nil
}

func removeEmptyParents(root, directory string) {
	for directory != root {
		if err := os.Remove(directory); err != nil {
			return
		}
		directory = filepath.Dir(directory)
	}
}

func union(left, right []string) []string {
	return uniqueSorted(append(append([]string{}, left...), right...))
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	return unique(values)
}

func unique(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
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

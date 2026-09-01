package handoff

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

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
	if err := disjointPathDomains(trackedPaths, untrackedPaths); err != nil {
		return result, err
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
		wrote = true
		if err := writeUntrackedFile(source, repo, relative, 0o600); err != nil {
			return result, fmt.Errorf("restore untracked payload %s: %w", relative, err)
		}
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

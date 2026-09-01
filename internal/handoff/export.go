package handoff

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

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
	if err := disjointPathDomains(tracked, untracked); err != nil {
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
	result := ExportResult{Bundle: bundle, BundleSHA256: manifest.BundleSHA256, BaseHead: head, Paths: changed}
	if err := safeio.PublishDirectory(temporary, bundle); err != nil {
		if safeio.WasCommitted(err) {
			keep = true
			return result, fmt.Errorf("handoff bundle published but durability is uncertain: %w", err)
		}
		return ExportResult{}, fmt.Errorf("publish handoff bundle: %w", err)
	}
	keep = true
	return result, nil
}

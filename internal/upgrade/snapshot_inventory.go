package upgrade

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	snapshotRootRuntime   = "runtime"
	snapshotRootCodex     = "codex"
	snapshotRootKnowledge = "knowledge"
	snapshotClassActive   = "activation"
	snapshotClassData     = "data"
	snapshotClassCache    = "cache"
	snapshotKindFile      = "file"
	snapshotKindTree      = "tree"
	snapshotKindSQLite    = "sqlite"
)

type snapshotSpec struct {
	Root         string
	RelativePath string
	Class        string
	Kind         string
}

func baseSnapshotSpecs() []snapshotSpec {
	return []snapshotSpec{
		{snapshotRootRuntime, filepath.ToSlash(filepath.Join("bin", runtimeBinaryName())), snapshotClassActive, snapshotKindFile},
		{snapshotRootCodex, "plugins/ytqjk-agentic-orchestrator", snapshotClassActive, snapshotKindTree},
		{snapshotRootCodex, "plugins/ytqjk-knowledge", snapshotClassActive, snapshotKindTree},
		{snapshotRootCodex, "plugins/.ytqjk-managed.json", snapshotClassActive, snapshotKindFile},
		{snapshotRootCodex, "plugins/.ytqjk-managed-plugins.json", snapshotClassActive, snapshotKindFile},
		{snapshotRootKnowledge, "service/knowledge.sqlite3", snapshotClassData, snapshotKindSQLite},
		{snapshotRootKnowledge, "service/library-v1.sqlite3", snapshotClassData, snapshotKindSQLite},
		{snapshotRootKnowledge, "service/orchestration.sqlite3", snapshotClassData, snapshotKindSQLite},
		{snapshotRootKnowledge, "service/orchestration.key", snapshotClassData, snapshotKindFile},
		{snapshotRootKnowledge, "catalog.json", snapshotClassData, snapshotKindFile},
		{snapshotRootKnowledge, "sessions", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "global", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "verified", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "personal-experience/approved", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "personal-experience/candidates", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "error-experience/approved", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "error-experience/candidates", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "service/intake/uploads", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "libraries", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "handoffs/sqlite-projections", snapshotClassData, snapshotKindTree},
		{snapshotRootKnowledge, "global-cache", snapshotClassCache, snapshotKindTree},
	}
}

func optionalSnapshotSpecs() []snapshotSpec {
	return []snapshotSpec{
		{snapshotRootRuntime, "active.json", snapshotClassActive, snapshotKindFile},
	}
}

func snapshotSpecs(plan Plan) ([]snapshotSpec, error) {
	specs := append(baseSnapshotSpecs(), optionalSnapshotSpecs()...)
	projects, err := snapshotProjectSpecs(plan.KnowledgeRoot)
	if err != nil {
		return nil, err
	}
	specs = append(specs, projects...)
	sort.Slice(specs, func(left, right int) bool { return snapshotSpecKey(specs[left]) < snapshotSpecKey(specs[right]) })
	return specs, nil
}

func snapshotProjectSpecs(knowledgeRoot string) ([]snapshotSpec, error) {
	projects := filepath.Join(knowledgeRoot, "projects")
	info, err := os.Lstat(projects)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("knowledge projects root must be a real directory")
	}
	entries, err := os.ReadDir(projects)
	if err != nil {
		return nil, err
	}
	var specs []snapshotSpec
	for _, entry := range entries {
		path := filepath.Join(projects, entry.Name())
		current, statErr := os.Lstat(path)
		if statErr != nil || !current.IsDir() || current.Mode()&os.ModeSymlink != 0 || !safePathSegment(entry.Name()) {
			return nil, fmt.Errorf("unsafe project cache entry: %s", entry.Name())
		}
		prefix := filepath.ToSlash(filepath.Join("projects", entry.Name()))
		for _, suffix := range []string{"handoffs", "errors"} {
			specs = append(specs, snapshotSpec{snapshotRootKnowledge, prefix + "/" + suffix, snapshotClassData, snapshotKindTree})
		}
		for _, suffix := range []string{"manifest.json", "index.json", "vectors.json"} {
			specs = append(specs, snapshotSpec{snapshotRootKnowledge, prefix + "/" + suffix, snapshotClassCache, snapshotKindFile})
		}
		for _, suffix := range []string{"cache", "vectors"} {
			specs = append(specs, snapshotSpec{snapshotRootKnowledge, prefix + "/" + suffix, snapshotClassCache, snapshotKindTree})
		}
	}
	return specs, nil
}

func captureSnapshotInventory(ctx context.Context, plan Plan, root string) ([]SnapshotItem, error) {
	specs, err := snapshotSpecs(plan)
	if err != nil {
		return nil, err
	}
	items := make([]SnapshotItem, 0, len(specs))
	for _, spec := range specs {
		item, err := captureSnapshotItem(ctx, plan, root, spec)
		if err != nil {
			return nil, fmt.Errorf("snapshot %s/%s: %w", spec.Root, spec.RelativePath, err)
		}
		items = append(items, item)
	}
	return items, nil
}

func captureSnapshotItem(ctx context.Context, plan Plan, root string, spec snapshotSpec) (SnapshotItem, error) {
	item := SnapshotItem{Root: spec.Root, RelativePath: spec.RelativePath, Class: spec.Class, Kind: spec.Kind}
	source, err := snapshotTargetPath(plan, item)
	if err != nil {
		return SnapshotItem{}, err
	}
	info, err := os.Lstat(source)
	if errors.Is(err, os.ErrNotExist) {
		return item, nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return SnapshotItem{}, errors.New("snapshot source is unsafe")
	}
	if spec.Kind == snapshotKindTree && !info.IsDir() {
		return SnapshotItem{}, errors.New("snapshot tree source is not a directory")
	}
	if spec.Kind != snapshotKindTree && !info.Mode().IsRegular() {
		return SnapshotItem{}, errors.New("snapshot file source is not regular")
	}
	destination, err := snapshotStoredPath(root, item)
	if err != nil {
		return SnapshotItem{}, err
	}
	item.Present = true
	item.Mode = uint32(info.Mode().Perm())
	expectedCopyHash := ""
	switch spec.Kind {
	case snapshotKindSQLite:
		if err := backupDatabase(ctx, source, destination); err != nil {
			return SnapshotItem{}, err
		}
		if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
			return SnapshotItem{}, err
		}
	case snapshotKindFile:
		before, err := safeio.FileSHA256(source)
		if err != nil {
			return SnapshotItem{}, err
		}
		if err := safeio.CopyFile(source, destination, info.Mode().Perm()); err != nil {
			return SnapshotItem{}, err
		}
		after, err := safeio.FileSHA256(source)
		if err != nil || before != after {
			return SnapshotItem{}, errors.New("snapshot source changed during copy")
		}
		expectedCopyHash = before
	case snapshotKindTree:
		before, err := snapshotTreeHash(source)
		if err != nil {
			return SnapshotItem{}, err
		}
		if err := snapshotCopyTree(source, destination); err != nil {
			return SnapshotItem{}, err
		}
		after, err := snapshotTreeHash(source)
		if err != nil || before != after {
			return SnapshotItem{}, errors.New("snapshot source changed during copy")
		}
		expectedCopyHash = before
	default:
		return SnapshotItem{}, errors.New("unsupported snapshot item kind")
	}
	if item.Kind == snapshotKindTree {
		item.SHA256, err = snapshotTreeHash(destination)
		if err == nil {
			item.Size, err = snapshotTreeSize(destination)
		}
	} else {
		item.SHA256, err = safeio.FileSHA256(destination)
		if err == nil {
			stored, statErr := os.Lstat(destination)
			if statErr != nil {
				err = statErr
			} else {
				item.Size = stored.Size()
			}
		}
	}
	if err == nil && expectedCopyHash != "" && item.SHA256 != expectedCopyHash {
		return SnapshotItem{}, errors.New("snapshot destination differs from source")
	}
	return item, err
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Schema != snapshotSchema || !hexDigestPattern.MatchString(snapshot.ID) || snapshot.Items == nil {
		return failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	if _, err := parseVersion(snapshot.FromVersion); err != nil {
		return failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	if _, err := parseVersion(snapshot.ToVersion); err != nil {
		return failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	required := map[string]snapshotSpec{}
	for _, spec := range baseSnapshotSpecs() {
		required[snapshotSpecKey(spec)] = spec
	}
	seen := map[string]SnapshotItem{}
	var previous string
	for index, item := range snapshot.Items {
		spec, ok := allowedSnapshotSpec(item)
		key := snapshotItemKey(item)
		if !ok || key == previous || (index > 0 && key < previous) {
			return failure("UPGRADE_SNAPSHOT_INVALID", nil)
		}
		if item.Present != (item.SHA256 != "") || item.Present && !hexDigestPattern.MatchString(item.SHA256) {
			return failure("UPGRADE_SNAPSHOT_INVALID", nil)
		}
		if !item.Present && item.Mode != 0 {
			return failure("UPGRADE_SNAPSHOT_INVALID", nil)
		}
		if item.Size < 0 || !item.Present && item.Size != 0 {
			return failure("UPGRADE_SNAPSHOT_INVALID", nil)
		}
		seen[key] = item
		if _, exists := required[key]; exists {
			delete(required, key)
		}
		_ = spec
		previous = key
	}
	if len(required) != 0 || snapshotInventoryOverlaps(snapshot.Items) {
		return failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	database := seen[snapshotItemKeyFrom(snapshotRootKnowledge, "service/orchestration.sqlite3")]
	key := seen[snapshotItemKeyFrom(snapshotRootKnowledge, "service/orchestration.key")]
	if database.Present != key.Present {
		return failure("UPGRADE_SNAPSHOT_INVALID", errors.New("orchestration database and key must be paired"))
	}
	return nil
}

func verifySnapshot(root string, snapshot Snapshot) error {
	if err := verifySnapshotManifest(root); err != nil {
		return err
	}
	if err := validateSnapshot(snapshot); err != nil {
		return err
	}
	for _, item := range snapshot.Items {
		path, err := snapshotStoredPath(root, item)
		if err != nil {
			return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
		}
		info, statErr := os.Lstat(path)
		if !item.Present {
			if !errors.Is(statErr, os.ErrNotExist) {
				return failure("UPGRADE_SNAPSHOT_CORRUPT", statErr)
			}
			continue
		}
		if statErr != nil || info.Mode()&os.ModeSymlink != 0 || item.Kind == snapshotKindTree && !info.IsDir() || item.Kind != snapshotKindTree && !info.Mode().IsRegular() {
			return failure("UPGRADE_SNAPSHOT_CORRUPT", statErr)
		}
		if info.Mode().Perm() != os.FileMode(item.Mode).Perm() {
			return failure("UPGRADE_SNAPSHOT_CORRUPT", errors.New("snapshot item mode mismatch"))
		}
		var digest string
		if item.Kind == snapshotKindTree {
			digest, err = snapshotTreeHash(path)
			if err == nil {
				var size int64
				size, err = snapshotTreeSize(path)
				if err == nil && size != item.Size {
					return failure("UPGRADE_SNAPSHOT_CORRUPT", errors.New("snapshot tree size mismatch"))
				}
			}
		} else {
			digest, err = safeio.FileSHA256(path)
			if err == nil && info.Size() != item.Size {
				return failure("UPGRADE_SNAPSHOT_CORRUPT", errors.New("snapshot file size mismatch"))
			}
		}
		if err != nil || digest != item.SHA256 {
			return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
		}
	}
	return verifySnapshotLayout(root, snapshot.Items)
}

func verifySnapshotLayout(root string, items []SnapshotItem) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("snapshot contains a symbolic link")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				return errors.New("snapshot contains a non-regular entry")
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil || !snapshotLayoutAllowed(filepath.ToSlash(relative), entry.IsDir(), items) {
			return errors.New("snapshot contains an undeclared entry")
		}
		return nil
	})
	if err != nil {
		return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
	}
	return nil
}

func snapshotLayoutAllowed(relative string, directory bool, items []SnapshotItem) bool {
	if relative == snapshotManifestName || relative == snapshotDigestName {
		return !directory
	}
	for _, item := range items {
		if !item.Present {
			continue
		}
		stored := item.Root + "/" + item.RelativePath
		if relative == stored || directory && strings.HasPrefix(stored, relative+"/") || item.Kind == snapshotKindTree && strings.HasPrefix(relative, stored+"/") {
			return true
		}
	}
	return false
}

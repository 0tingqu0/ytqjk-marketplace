package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func allowedSnapshotSpec(item SnapshotItem) (snapshotSpec, bool) {
	for _, spec := range append(baseSnapshotSpecs(), optionalSnapshotSpecs()...) {
		if snapshotSpecKey(spec) == snapshotItemKey(item) {
			return spec, spec.Class == item.Class && spec.Kind == item.Kind
		}
	}
	parts := strings.Split(item.RelativePath, "/")
	if item.Root != snapshotRootKnowledge || len(parts) != 3 || parts[0] != "projects" || !safePathSegment(parts[1]) {
		return snapshotSpec{}, false
	}
	kinds := map[string]snapshotSpec{
		"handoffs":      {item.Root, item.RelativePath, snapshotClassData, snapshotKindTree},
		"errors":        {item.Root, item.RelativePath, snapshotClassData, snapshotKindTree},
		"manifest.json": {item.Root, item.RelativePath, snapshotClassCache, snapshotKindFile},
		"index.json":    {item.Root, item.RelativePath, snapshotClassCache, snapshotKindFile},
		"vectors.json":  {item.Root, item.RelativePath, snapshotClassCache, snapshotKindFile},
		"cache":         {item.Root, item.RelativePath, snapshotClassCache, snapshotKindTree},
		"vectors":       {item.Root, item.RelativePath, snapshotClassCache, snapshotKindTree},
	}
	spec, ok := kinds[parts[2]]
	return spec, ok && spec.Class == item.Class && spec.Kind == item.Kind
}

func snapshotInventoryOverlaps(items []SnapshotItem) bool {
	for left := range items {
		first := snapshotItemKey(items[left]) + "/"
		for right := left + 1; right < len(items); right++ {
			second := snapshotItemKey(items[right]) + "/"
			if strings.HasPrefix(first, second) || strings.HasPrefix(second, first) {
				return true
			}
		}
	}
	return false
}

func snapshotTargetPath(plan Plan, item SnapshotItem) (string, error) {
	root := map[string]string{
		snapshotRootRuntime: plan.RuntimeRoot, snapshotRootCodex: plan.CodexRoot,
		snapshotRootKnowledge: plan.KnowledgeRoot,
	}[item.Root]
	if root == "" {
		return "", errors.New("unknown snapshot root")
	}
	return rootedSnapshotPath(root, item.RelativePath)
}

func snapshotStoredPath(root string, item SnapshotItem) (string, error) {
	return rootedSnapshotPath(root, item.Root+"/"+item.RelativePath)
}

func rootedSnapshotPath(root, relative string) (string, error) {
	if relative == "" || strings.Contains(relative, "\\") || filepath.IsAbs(filepath.FromSlash(relative)) || filepath.VolumeName(filepath.FromSlash(relative)) != "" {
		return "", errors.New("unsafe snapshot relative path")
	}
	cleaned := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if cleaned != relative || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("unsafe snapshot relative path")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(absoluteRoot, filepath.FromSlash(relative))
	info, statErr := os.Lstat(absoluteRoot)
	if errors.Is(statErr, os.ErrNotExist) {
		return candidate, nil
	}
	if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("snapshot root must be a real directory")
	}
	return safeio.Contained(absoluteRoot, candidate)
}

func snapshotSpecKey(spec snapshotSpec) string {
	return snapshotItemKeyFrom(spec.Root, spec.RelativePath)
}

func snapshotItemKey(item SnapshotItem) string {
	return snapshotItemKeyFrom(item.Root, item.RelativePath)
}

func snapshotItemKeyFrom(root, relative string) string {
	return root + "\x00" + relative
}

func safePathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && len(value) <= 255 && !strings.ContainsAny(value, "/\\")
}

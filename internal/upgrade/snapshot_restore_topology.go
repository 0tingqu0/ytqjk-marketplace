package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func restorePlanRoots(plan Plan) ([]string, error) {
	values := []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot}
	roots := make([]string, 0, len(values))
	for _, value := range values {
		root, err := validateProspectiveRestoreRoot(value)
		if err != nil {
			return nil, err
		}
		roots = append(roots, root)
	}
	for left := range roots {
		for right := left + 1; right < len(roots); right++ {
			if restorePathsOverlap(roots[left], roots[right]) {
				return nil, errors.New("snapshot restore roots overlap")
			}
		}
	}
	return roots, nil
}

func validateRestoreTopology(plan Plan, items []restoreItem) error {
	if _, err := restorePlanRoots(plan); err != nil {
		return err
	}
	if err := validateRestoreTargetSet(items); err != nil {
		return err
	}
	for _, item := range items {
		if err := validateRestoreTarget(plan, item.Target); err != nil {
			return err
		}
	}
	return nil
}

func validateStandaloneRestoreTopology(items []restoreItem) error {
	if err := validateRestoreTargetSet(items); err != nil {
		return err
	}
	for _, item := range items {
		if !filepath.IsAbs(item.Target) || filepath.Clean(item.Target) != item.Target {
			return errors.New("restore target must be an absolute clean path")
		}
		if err := validateExistingRestorePath(item.Target); err != nil {
			return err
		}
	}
	return nil
}

func validateRestoreTargetSet(items []restoreItem) error {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		key, err := restorePathKey(item.Target)
		if err != nil {
			return err
		}
		keys = append(keys, key)
	}
	for left := range keys {
		for right := left + 1; right < len(keys); right++ {
			if keys[left] == keys[right] || restorePathDescendant(keys[left], keys[right]) ||
				restorePathDescendant(keys[right], keys[left]) {
				return errors.New("snapshot restore targets overlap")
			}
		}
	}
	return nil
}

func validateRestoreTarget(plan Plan, target string) error {
	roots, err := restorePlanRoots(plan)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("snapshot restore target must be an absolute clean path")
	}
	matched := ""
	for _, root := range roots {
		if restorePathDescendant(root, target) {
			if matched != "" {
				return errors.New("snapshot restore target matches multiple roots")
			}
			matched = root
		}
	}
	if matched == "" {
		return errors.New("snapshot restore target escapes configured roots")
	}
	return validateExistingRestorePath(target)
}

func validateProspectiveRestoreRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errors.New("snapshot restore root must be an absolute clean path")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := validateExistingRestorePath(absolute); err != nil {
		return "", err
	}
	if info, err := os.Lstat(absolute); err == nil && !info.IsDir() {
		return "", errors.New("snapshot restore root must be a directory")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func validateExistingRestorePath(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	var chain []string
	for current := filepath.Clean(absolute); ; current = filepath.Dir(current) {
		chain = append(chain, current)
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	missing := false
	for index := len(chain) - 1; index >= 0; index-- {
		if missing {
			continue
		}
		info, err := os.Lstat(chain[index])
		if errors.Is(err, os.ErrNotExist) {
			missing = true
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("snapshot restore path contains a symbolic link")
		}
		if index != 0 && !info.IsDir() {
			return errors.New("snapshot restore path ancestor is not a directory")
		}
		if index == 0 && !info.IsDir() && !info.Mode().IsRegular() {
			return errors.New("snapshot restore target is non-regular")
		}
	}
	return nil
}

func restorePathsOverlap(left, right string) bool {
	leftKey, leftErr := restorePathKey(left)
	rightKey, rightErr := restorePathKey(right)
	if leftErr != nil || rightErr != nil {
		return true
	}
	return leftKey == rightKey || restorePathDescendant(leftKey, rightKey) || restorePathDescendant(rightKey, leftKey)
}

func restorePathDescendant(root, candidate string) bool {
	rootKey, rootErr := restorePathKey(root)
	candidateKey, candidateErr := restorePathKey(candidate)
	if rootErr != nil || candidateErr != nil || rootKey == candidateKey {
		return false
	}
	relative, err := filepath.Rel(rootKey, candidateKey)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func restorePathKey(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	if runtime.GOOS == "windows" {
		absolute = strings.ToLower(absolute)
	}
	return absolute, nil
}

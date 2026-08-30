package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const managedManifest = ".ytqjk-managed-plugins.json"

var pluginNames = []string{"ytqjk-agentic-orchestrator", "ytqjk-knowledge"}

type pluginEntry struct {
	Name       string `json:"name"`
	TreeSHA256 string `json:"tree_sha256"`
}

type pluginManifest struct {
	Schema  string        `json:"schema"`
	Version string        `json:"version"`
	Plugins []pluginEntry `json:"plugins"`
}

type PluginResult struct {
	Changed     bool     `json:"changed"`
	StablePaths []string `json:"stable_paths"`
}

func MaterializePlugins(codexRoot, sourceRoot, binary string) (PluginResult, error) {
	pluginsRoot := filepath.Join(codexRoot, "plugins")
	if err := validateManagedTargets(pluginsRoot); err != nil {
		return PluginResult{}, err
	}
	stage := filepath.Join(pluginsRoot, fmt.Sprintf(".ytqjk-stage-%d", time.Now().UnixNano()))
	backup := filepath.Join(pluginsRoot, fmt.Sprintf(".ytqjk-backup-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(stage, 0o755); err != nil {
		return PluginResult{}, err
	}
	defer os.RemoveAll(stage)
	defer os.RemoveAll(backup)
	if err := os.MkdirAll(backup, 0o755); err != nil {
		return PluginResult{}, err
	}
	desired := pluginManifest{Schema: "ytqjk-managed-plugins/v1", Version: Version}
	for _, name := range pluginNames {
		source := filepath.Join(sourceRoot, "plugins", name)
		target := filepath.Join(stage, name)
		if err := safeio.CopyTree(source, target); err != nil {
			return PluginResult{}, fmt.Errorf("copy plugin %s: %w", name, err)
		}
		if binary != "" {
			binName := "ytqjk"
			if strings.EqualFold(filepath.Ext(binary), ".exe") {
				binName += ".exe"
			}
			if err := safeio.CopyFile(binary, filepath.Join(target, "bin", binName), 0o755); err != nil {
				return PluginResult{}, fmt.Errorf("bundle Go runtime: %w", err)
			}
		}
		hash, err := safeio.TreeHash(target)
		if err != nil {
			return PluginResult{}, err
		}
		desired.Plugins = append(desired.Plugins, pluginEntry{Name: name, TreeSHA256: hash})
	}
	current, _ := readManagedManifest(filepath.Join(pluginsRoot, managedManifest))
	unchanged := current != nil && manifestsEqual(*current, desired)
	if unchanged {
		for _, entry := range desired.Plugins {
			hash, err := safeio.TreeHash(filepath.Join(pluginsRoot, entry.Name))
			if err != nil || hash != entry.TreeSHA256 {
				unchanged = false
				break
			}
		}
	}
	result := PluginResult{Changed: !unchanged}
	for _, name := range pluginNames {
		result.StablePaths = append(result.StablePaths, filepath.ToSlash(filepath.Join("plugins", name)))
	}
	if unchanged {
		return result, nil
	}
	data, err := json.Marshal(desired)
	if err != nil {
		return PluginResult{}, err
	}
	if err := os.WriteFile(filepath.Join(stage, managedManifest), append(data, '\n'), 0o600); err != nil {
		return PluginResult{}, err
	}
	type move struct{ target, saved string }
	var changed []move
	rollback := func() {
		for index := len(changed) - 1; index >= 0; index-- {
			item := changed[index]
			_ = os.RemoveAll(item.target)
			if item.saved != "" {
				_ = os.Rename(item.saved, item.target)
			}
		}
	}
	for _, name := range append(append([]string{}, pluginNames...), managedManifest) {
		target := filepath.Join(pluginsRoot, name)
		staged := filepath.Join(stage, name)
		saved := ""
		if _, err := os.Lstat(target); err == nil {
			saved = filepath.Join(backup, name)
			if err := os.Rename(target, saved); err != nil {
				rollback()
				return PluginResult{}, err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			rollback()
			return PluginResult{}, err
		}
		changed = append(changed, move{target: target, saved: saved})
		if err := os.Rename(staged, target); err != nil {
			rollback()
			return PluginResult{}, err
		}
	}
	return result, nil
}

func RemoveManagedPlugins(codexRoot string) ([]string, error) {
	pluginsRoot := filepath.Join(codexRoot, "plugins")
	if err := validateManagedTargets(pluginsRoot); err != nil {
		return nil, err
	}
	manifestPath := filepath.Join(pluginsRoot, managedManifest)
	manifest, err := readManagedManifest(manifestPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var removed []string
	for _, entry := range manifest.Plugins {
		target := filepath.Join(pluginsRoot, entry.Name)
		hash, hashErr := safeio.TreeHash(target)
		if hashErr != nil || hash != entry.TreeSHA256 {
			return nil, fmt.Errorf("managed plugin %s was modified", entry.Name)
		}
		if err := os.RemoveAll(target); err != nil {
			return nil, err
		}
		removed = append(removed, filepath.ToSlash(filepath.Join("plugins", entry.Name)))
	}
	if err := os.Remove(manifestPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return removed, nil
}

func validateManagedTargets(pluginsRoot string) error {
	info, err := os.Lstat(pluginsRoot)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
		return errors.New("stable plugin root is invalid")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	manifest, manifestErr := readManagedManifest(filepath.Join(pluginsRoot, managedManifest))
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return manifestErr
	}
	managed := map[string]pluginEntry{}
	if manifest != nil {
		for _, entry := range manifest.Plugins {
			managed[entry.Name] = entry
		}
	}
	for _, name := range pluginNames {
		target := filepath.Join(pluginsRoot, name)
		info, statErr := os.Lstat(target)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return statErr
		}
		entry, ok := managed[name]
		if !ok {
			return fmt.Errorf("stable plugin directory %s is not managed", name)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("managed stable plugin directory %s is invalid", name)
		}
		hash, hashErr := safeio.TreeHash(target)
		if hashErr != nil || hash != entry.TreeSHA256 {
			return fmt.Errorf("managed stable plugin directory %s was modified", name)
		}
	}
	return nil
}

func readManagedManifest(path string) (*pluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest pluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("managed plugin manifest is invalid")
	}
	if manifest.Schema != "ytqjk-managed-plugins/v1" || manifest.Version == "" || len(manifest.Plugins) != len(pluginNames) {
		return nil, errors.New("managed plugin manifest is invalid")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Plugins {
		if seen[entry.Name] || len(entry.TreeSHA256) != 64 {
			return nil, errors.New("managed plugin manifest is invalid")
		}
		seen[entry.Name] = true
	}
	return &manifest, nil
}

func manifestsEqual(left, right pluginManifest) bool {
	if left.Schema != right.Schema || left.Version != right.Version || len(left.Plugins) != len(right.Plugins) {
		return false
	}
	sort.Slice(left.Plugins, func(i, j int) bool { return left.Plugins[i].Name < left.Plugins[j].Name })
	sort.Slice(right.Plugins, func(i, j int) bool { return right.Plugins[i].Name < right.Plugins[j].Name })
	for index := range left.Plugins {
		if left.Plugins[index] != right.Plugins[index] {
			return false
		}
	}
	return true
}

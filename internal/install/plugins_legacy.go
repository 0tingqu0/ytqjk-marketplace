package install

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

type legacyPluginEntry struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	TreeSHA256 string `json:"tree_sha256"`
}

type legacyPluginManifest struct {
	Schema  string              `json:"schema"`
	Plugins []legacyPluginEntry `json:"plugins"`
}

func readLegacyManagedManifest(path string) (*legacyPluginManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var manifest legacyPluginManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, errors.New("legacy managed plugin manifest is invalid")
	}
	if manifest.Schema != "ytqjk-managed-plugins/v1" || len(manifest.Plugins) != len(pluginNames) {
		return nil, errors.New("legacy managed plugin manifest is invalid")
	}
	seen := map[string]bool{}
	for _, entry := range manifest.Plugins {
		if seen[entry.Name] || !knownPlugin(entry.Name) || entry.Version == "" || !validSHA256(entry.TreeSHA256) {
			return nil, errors.New("legacy managed plugin manifest is invalid")
		}
		seen[entry.Name] = true
	}
	return &manifest, nil
}

func legacyTreeHash(root string) (string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("legacy plugin tree is invalid")
	}
	digest := sha256.New()
	if err := hashLegacyDirectory(root, root, digest); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func hashLegacyDirectory(root, directory string, digest hash.Hash) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("legacy plugin tree contains a link")
		}
		if info.IsDir() {
			if entry.Name() == "__pycache__" {
				if err := validateLegacyCache(path); err != nil {
					return err
				}
				continue
			}
			if err := hashLegacyDirectory(root, path, digest); err != nil {
				return err
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return errors.New("legacy plugin tree contains a non-regular entry")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if _, err := digest.Write([]byte(filepath.ToSlash(relative))); err != nil {
			return err
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(digest, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateLegacyCache(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".pyc" {
			return errors.New("legacy plugin cache contains an unexpected entry")
		}
	}
	return nil
}

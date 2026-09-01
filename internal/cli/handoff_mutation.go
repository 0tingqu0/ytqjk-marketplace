package cli

import (
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/handoff"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/orchestration"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type handoffMutationBinding struct {
	bundleSHA256 string
	paths        []string
}

var applyHandoff = handoff.Apply

func handoffApplyScope(
	knowledgeRoot, repositoryRoot, bundle, database, key string,
) (maintenance.Scope, error) {
	scope, err := knowledgeFileScope(knowledgeRoot, database, key)
	if err != nil {
		return maintenance.Scope{}, err
	}
	absoluteBundle, err := filepath.Abs(bundle)
	if err != nil {
		return maintenance.Scope{}, err
	}
	bundleParent := filepath.Dir(absoluteBundle)
	// snapshotHandoffBundle materializes its staging directory beside the
	// source bundle, so one exact parent binding covers both locations.
	scope.ExtraRoots = append(scope.ExtraRoots, repositoryRoot, bundleParent)
	return scope, nil
}

func readAttestation(path string) (orchestration.Attestation, error) {
	var token orchestration.Attestation
	if err := safeio.ReadJSON(path, &token); err != nil {
		return token, err
	}
	return token, nil
}

func readHandoffMutationBinding(bundle string) (handoffMutationBinding, error) {
	var manifest handoff.Manifest
	if err := safeio.ReadJSON(filepath.Join(bundle, "manifest.json"), &manifest); err != nil {
		return handoffMutationBinding{}, err
	}
	paths := append([]string(nil), manifest.Tracked.Paths...)
	for _, record := range manifest.Untracked {
		paths = append(paths, record.Path)
	}
	sort.Strings(paths)
	return handoffMutationBinding{bundleSHA256: manifest.BundleSHA256, paths: paths}, nil
}

func snapshotHandoffBundle(source string) (string, func(), error) {
	absolute, err := filepath.Abs(source)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("handoff bundle is not a safe directory")
	}
	temporary, err := os.MkdirTemp(filepath.Dir(absolute), ".ytqjk-handoff-apply-*")
	if err != nil {
		return "", nil, err
	}
	if err := safeio.CopyTree(absolute, temporary); err != nil {
		_ = os.RemoveAll(temporary)
		return "", nil, err
	}
	return temporary, func() { _ = os.RemoveAll(temporary) }, nil
}

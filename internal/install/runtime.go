package install

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type RuntimeResult struct {
	Status       string `json:"status"`
	Changed      bool   `json:"changed"`
	Generation   string `json:"generation,omitempty"`
	Version      string `json:"version,omitempty"`
	BinarySHA256 string `json:"binary_sha256,omitempty"`
	Rollback     string `json:"rollback,omitempty"`
}

func InstallRuntime(runtimeRoot, source, version string) (RuntimeResult, error) {
	root, err := prepareRuntimeRoot(runtimeRoot)
	if err != nil {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, err
	}
	digest, err := safeio.FileSHA256(source)
	if err != nil {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, err
	}
	generation := installGeneration(version, digest)
	previous, _, previousErr := runtimeentry.ReadActive(root)
	fresh := errors.Is(previousErr, os.ErrNotExist)
	if previousErr != nil && !fresh {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, previousErr
	}
	if fresh {
		if info, statErr := os.Lstat(runtimeentry.LauncherPath(root)); statErr == nil {
			return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, errors.New("legacy runtime requires the authenticated upgrade workflow")
		} else if !errors.Is(statErr, os.ErrNotExist) || info != nil {
			return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, statErr
		}
	} else if previous.Generation == generation && previous.Version == version &&
		previous.BinarySHA256 == digest && launcherMatches(root, digest) {
		return RuntimeResult{
			Status: "ACTIVE", Changed: false, Generation: generation,
			Version: version, BinarySHA256: digest, Rollback: "NOT_NEEDED",
		}, nil
	} else if !fresh {
		return RuntimeResult{
			Status: "FAILED", Changed: false, Generation: generation,
			Version: version, BinarySHA256: digest, Rollback: "NOT_NEEDED",
		}, errors.New("runtime update requires the authenticated upgrade workflow")
	}
	manifest, err := runtimeentry.MaterializeGeneration(root, generation, version, source, digest)
	if err == nil {
		err = runtimeentry.InstallLauncher(root, source, digest)
	}
	if err == nil {
		err = runtimeentry.Activate(root, manifest)
	}
	if err == nil {
		active, _, readErr := runtimeentry.ReadActive(root)
		if readErr != nil || active.Generation != generation || active.Version != version ||
			active.BinarySHA256 != digest {
			err = errors.Join(errors.New("installed runtime did not become active"), readErr)
		}
	}
	if err != nil {
		rollback := rollbackRuntimeInstall(root, generation, previous, fresh)
		status := "SUCCEEDED"
		if rollback != nil {
			status = "FAILED"
		}
		return RuntimeResult{
			Status: "FAILED", Generation: generation, Version: version,
			BinarySHA256: digest, Rollback: status,
		}, errors.Join(err, rollback)
	}
	return RuntimeResult{
		Status: "ACTIVE", Changed: true, Generation: generation,
		Version: version, BinarySHA256: digest, Rollback: "NOT_NEEDED",
	}, nil
}

// RollbackFreshRuntime removes a runtime created by the current install after
// a later installation step fails. Existing and idempotent runtimes are never
// removed through this path.
func RollbackFreshRuntime(runtimeRoot string, installed RuntimeResult) error {
	if !installed.Changed || installed.Status != "ACTIVE" || installed.Generation == "" ||
		installed.Version == "" || installed.BinarySHA256 == "" {
		return errors.New("fresh runtime rollback binding is invalid")
	}
	active, _, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil {
		return err
	}
	if active.Generation != installed.Generation || active.Version != installed.Version ||
		active.BinarySHA256 != installed.BinarySHA256 || !launcherMatches(runtimeRoot, installed.BinarySHA256) {
		return errors.New("fresh runtime changed after installation")
	}
	return rollbackRuntimeInstall(runtimeRoot, installed.Generation, runtimeentry.Manifest{}, true)
}

// ValidateRuntimeUninstall rejects an unbound or self-hosted uninstall before
// any plugin, guidance, or runtime mutation begins.
func ValidateRuntimeUninstall(runtimeRoot, caller string) error {
	active, target, exists, err := runtimeUninstallBinding(runtimeRoot)
	if err != nil || !exists {
		return err
	}
	if !launcherMatches(runtimeRoot, active.BinarySHA256) {
		return errors.New("runtime launcher differs from the active manifest")
	}
	for _, path := range []string{runtimeentry.LauncherPath(runtimeRoot), target} {
		same, sameErr := sameFile(caller, path)
		if sameErr != nil {
			return sameErr
		}
		if same {
			return errors.New("runtime uninstall must run from a verified installer bootstrap")
		}
	}
	return nil
}

func UninstallRuntime(runtimeRoot, caller string) (RuntimeResult, error) {
	active, target, exists, err := runtimeUninstallBinding(runtimeRoot)
	if err != nil {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, err
	}
	if !exists {
		return RuntimeResult{Status: "ABSENT", Changed: false, Rollback: "NOT_NEEDED"}, nil
	}
	if err := ValidateRuntimeUninstall(runtimeRoot, caller); err != nil {
		return RuntimeResult{
			Status: "FAILED", Generation: active.Generation, Version: active.Version,
			BinarySHA256: active.BinarySHA256, Rollback: "NOT_NEEDED",
		}, err
	}
	manifestPath := runtimeentry.ActiveManifestPath(runtimeRoot)
	tombstone := manifestPath + ".uninstall-" + active.Generation + ".pending"
	if _, err := os.Lstat(tombstone); !errors.Is(err, os.ErrNotExist) {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"},
			errors.Join(errors.New("runtime uninstall tombstone already exists"), err)
	}
	if err := os.Rename(manifestPath, tombstone); err != nil {
		return RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}, err
	}
	restore := func() error {
		launcherErr := runtimeentry.InstallLauncher(runtimeRoot, target, active.BinarySHA256)
		manifestErr := os.Rename(tombstone, manifestPath)
		return errors.Join(launcherErr, manifestErr)
	}
	if err := os.Remove(runtimeentry.LauncherPath(runtimeRoot)); err != nil {
		rollbackErr := restore()
		return RuntimeResult{Status: "FAILED", Rollback: runtimeRollbackStatus(rollbackErr)},
			errors.Join(err, rollbackErr)
	}
	if err := os.Remove(target); err != nil {
		rollbackErr := restore()
		return RuntimeResult{Status: "FAILED", Rollback: runtimeRollbackStatus(rollbackErr)},
			errors.Join(err, rollbackErr)
	}
	if err := os.Remove(tombstone); err != nil {
		return RuntimeResult{
			Status: "FAILED", Changed: true, Generation: active.Generation,
			Version: active.Version, BinarySHA256: active.BinarySHA256, Rollback: "NOT_POSSIBLE",
		}, err
	}
	for _, path := range []string{
		filepath.Dir(target), filepath.Dir(filepath.Dir(target)), filepath.Dir(runtimeentry.LauncherPath(runtimeRoot)),
	} {
		_ = os.Remove(path)
	}
	return RuntimeResult{
		Status: "REMOVED", Changed: true, Generation: active.Generation,
		Version: active.Version, BinarySHA256: active.BinarySHA256, Rollback: "NOT_NEEDED",
	}, nil
}

func runtimeRollbackStatus(err error) string {
	if err == nil {
		return "SUCCEEDED"
	}
	return "FAILED"
}

func runtimeUninstallBinding(runtimeRoot string) (runtimeentry.Manifest, string, bool, error) {
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return runtimeentry.Manifest{}, "", false, err
	}
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return runtimeentry.Manifest{}, "", false, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return runtimeentry.Manifest{}, "", false, errors.Join(errors.New("runtime root is unsafe"), err)
	}
	active, target, err := runtimeentry.ReadActive(root)
	if errors.Is(err, os.ErrNotExist) {
		if _, launcherErr := os.Lstat(runtimeentry.LauncherPath(root)); errors.Is(launcherErr, os.ErrNotExist) {
			return runtimeentry.Manifest{}, "", false, nil
		}
		return runtimeentry.Manifest{}, "", false, errors.New("unbound runtime launcher cannot be uninstalled")
	}
	return active, target, err == nil, err
}

func sameFile(left, right string) (bool, error) {
	leftInfo, err := os.Stat(left)
	if err != nil {
		return false, err
	}
	rightInfo, err := os.Stat(right)
	if err != nil {
		return false, err
	}
	return os.SameFile(leftInfo, rightInfo), nil
}

func prepareRuntimeRoot(runtimeRoot string) (string, error) {
	root, err := filepath.Abs(runtimeRoot)
	if err != nil {
		return "", err
	}
	root = filepath.Clean(root)
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(root, 0o700); err != nil {
			return "", err
		}
		info, err = os.Lstat(root)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.Join(errors.New("runtime root is unsafe"), err)
	}
	return root, nil
}

func installGeneration(version, digest string) string {
	value := sha256.Sum256([]byte("ytqjk-install-generation/v1\x00" + version + "\x00" + digest))
	return hex.EncodeToString(value[:])
}

func launcherMatches(runtimeRoot, expected string) bool {
	info, err := os.Lstat(runtimeentry.LauncherPath(runtimeRoot))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	digest, err := safeio.FileSHA256(runtimeentry.LauncherPath(runtimeRoot))
	return err == nil && digest == expected
}

func rollbackRuntimeInstall(runtimeRoot, generation string, previous runtimeentry.Manifest, fresh bool) error {
	if !fresh {
		return runtimeentry.Activate(runtimeRoot, previous)
	}
	var result error
	if err := os.Remove(runtimeentry.ActiveManifestPath(runtimeRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	if err := os.Remove(runtimeentry.LauncherPath(runtimeRoot)); err != nil && !errors.Is(err, os.ErrNotExist) {
		result = errors.Join(result, err)
	}
	binary, pathErr := runtimeentry.GenerationBinaryPath(runtimeRoot, generation)
	if pathErr != nil {
		return errors.Join(result, pathErr)
	}
	for _, path := range []string{binary, filepath.Dir(binary), filepath.Dir(filepath.Dir(binary))} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}
	return result
}

package platform

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// KnowledgeRoot returns the explicit environment override or the platform
// default. It never derives a data directory from an install target.
func KnowledgeRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	if value := strings.TrimSpace(os.Getenv("YTQJK_KNOWLEDGE_ROOT")); value != "" {
		return filepath.Abs(value)
	}
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "YTQJK", "knowledge"), nil
		}
	}
	if data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); data != "" {
		return filepath.Join(data, "ytqjk"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ytqjk"), nil
}

func CodexRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		return filepath.Abs(explicit)
	}
	if value := strings.TrimSpace(os.Getenv("CODEX_HOME")); value != "" {
		return filepath.Abs(value)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

func RuntimeRoot() (string, error) {
	if runtime.GOOS == "windows" {
		if local := strings.TrimSpace(os.Getenv("LOCALAPPDATA")); local != "" {
			return filepath.Join(local, "YTQJK", "runtime"), nil
		}
	}
	if data := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); data != "" {
		return filepath.Join(data, "ytqjk", "runtime"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "ytqjk", "runtime"), nil
}

// MaintenanceControlRoot returns the stable per-user control plane shared by
// the runtime, Codex, and knowledge roots. Explicit install targets do not
// change this coordination root.
func MaintenanceControlRoot() (string, error) {
	runtimeRoot, err := RuntimeRoot()
	if err != nil {
		return "", err
	}
	return filepath.Dir(runtimeRoot), nil
}

func BinPath() (string, error) {
	root, err := RuntimeRoot()
	if err != nil {
		return "", err
	}
	name := "ytqjk"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(root, "bin", name), nil
}

// SourceRoot finds the repository/distribution root without trusting the
// caller's current directory alone.
func SourceRoot(explicit string) (string, error) {
	candidates := []string{explicit, os.Getenv("YTQJK_SOURCE_ROOT")}
	if executable, err := os.Executable(); err == nil {
		resolved, resolveErr := filepath.EvalSymlinks(executable)
		if resolveErr == nil {
			executable = resolved
		}
		directory := filepath.Dir(executable)
		candidates = append(candidates, directory, filepath.Dir(directory), filepath.Dir(filepath.Dir(directory)))
	}
	if cwd, err := os.Getwd(); err == nil {
		candidates = append(candidates, cwd)
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		absolute, err := filepath.Abs(candidate)
		if err != nil || seen[absolute] {
			continue
		}
		seen[absolute] = true
		if regular(filepath.Join(absolute, ".agents", "plugins", "marketplace.json")) &&
			directory(filepath.Join(absolute, "plugins", "ytqjk-agentic-orchestrator")) {
			return absolute, nil
		}
	}
	return "", errors.New("YTQJK source root could not be located; set YTQJK_SOURCE_ROOT")
}

func Executable(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	absolute, err := filepath.Abs(path)
	if err == nil {
		return absolute
	}
	return path
}

func regular(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func directory(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

package handoff

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func ensureWorkerIndexClean(repo string) error {
	status, err := gitOutput(repo, "status", "--porcelain=v1", "-z", "--untracked-files=no", "--no-renames")
	if err != nil {
		return err
	}
	for _, record := range bytes.Split(status, []byte{0}) {
		if len(record) > 1 && (record[0] != ' ' || record[1] == 'A') {
			return errors.New("worker index is not clean; workers must not stage changes")
		}
	}
	if result := runGit(repo, "diff", "--cached", "--quiet", "--exit-code", "--"); result.err != nil {
		return errors.New("worker index is not clean; workers must not stage changes")
	}
	unresolved, err := gitOutput(repo, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return err
	}
	if len(unresolved) > 0 {
		return errors.New("worker has unresolved paths")
	}
	return nil
}

type gitResult struct {
	stdout, stderr []byte
	err            error
	exitCode       int
}

func runGit(repo string, arguments ...string) gitResult {
	command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
	command.Env = append(os.Environ(), "LANG=C", "LC_ALL=C")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitError *exec.ExitError
		if errors.As(err, &exitError) {
			exitCode = exitError.ExitCode()
		}
	}
	return gitResult{stdout: stdout.Bytes(), stderr: stderr.Bytes(), err: err, exitCode: exitCode}
}

func (result gitResult) detail() string {
	value := strings.TrimSpace(string(result.stderr))
	if value == "" {
		value = strings.TrimSpace(string(result.stdout))
	}
	return value
}

func gitOutput(repo string, arguments ...string) ([]byte, error) {
	result := runGit(repo, arguments...)
	if result.err != nil {
		detail := result.detail()
		if detail == "" {
			detail = "git " + strings.Join(arguments, " ") + " failed"
		}
		return nil, errors.New(detail)
	}
	return result.stdout, nil
}

func gitText(repo string, arguments ...string) (string, error) {
	value, err := gitOutput(repo, arguments...)
	return strings.TrimSpace(string(value)), err
}

func gitPaths(repo string, arguments ...string) ([]string, error) {
	value, err := gitOutput(repo, arguments...)
	if err != nil {
		return nil, err
	}
	var result []string
	for _, item := range bytes.Split(value, []byte{0}) {
		if len(item) == 0 {
			continue
		}
		path, err := normalizePath(string(item))
		if err != nil {
			return nil, err
		}
		result = append(result, path)
	}
	return uniqueSorted(result), nil
}

func safeParents(root, target string) error {
	for parent := filepath.Dir(target); parent != root; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe parent")
		}
		if next := filepath.Dir(parent); next == parent {
			return errors.New("unsafe parent")
		}
	}
	return nil
}

func removeEmptyParents(root, directory string) {
	for directory != root {
		if err := os.Remove(directory); err != nil {
			return
		}
		directory = filepath.Dir(directory)
	}
}

func union(left, right []string) []string {
	return uniqueSorted(append(append([]string{}, left...), right...))
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	return unique(values)
}

func unique(values []string) []string {
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

func stringSet(values []string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

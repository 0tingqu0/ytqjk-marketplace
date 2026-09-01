package ciguard

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

var (
	activeReference = regexp.MustCompile(
		`(?i)(?:\bpython(?:3|w)?(?:\.exe)?\b|\bpip(?:3)?(?:\.exe)?\b|` +
			`\bvirtualenv\b|(?:^|[^[:alnum:]_])\.?venv(?:[^[:alnum:]_]|$)|` +
			`\brequirements(?:[-_.][[:alnum:]_-]+)?(?:\.txt|\.in)?\b|` +
			`\.py(?:c|o|d)?\b|__pycache__|\.pytest_cache|\bpytest\b)`,
	)
	fileReference = regexp.MustCompile(
		`(?i)(?:\.py(?:c|o|d)?\b|\brequirements(?:[-_.][[:alnum:]_-]+)?` +
			`(?:\.txt|\.in)?\b)`,
	)
)

func TestTrackedRuntimeIsGoOnly(t *testing.T) {
	root := repositoryRoot(t)
	paths := trackedPaths(t, root)
	violations := make([]string, 0)
	for _, path := range paths {
		if reason := forbiddenPath(path); reason != "" {
			violations = append(violations, fmt.Sprintf("%s: %s", path, reason))
			continue
		}
		content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("read tracked file %q: %v", path, err)
		}
		violations = append(violations, contentViolations(path, content)...)
	}
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	const reportLimit = 50
	reported := violations
	if len(reported) > reportLimit {
		reported = reported[:reportLimit]
	}
	message := strings.Join(reported, "\n")
	if len(violations) > reportLimit {
		message += fmt.Sprintf("\n... and %d more", len(violations)-reportLimit)
	}
	t.Fatalf("tracked non-Go runtime entries found:\n%s", message)
}

func TestPolicyClassification(t *testing.T) {
	pathCases := map[string]bool{
		"internal/service.go":               false,
		"legacy/task.py":                    true,
		"legacy/__pycache__/task.cache":     true,
		"release/requirements-runtime.txt":  true,
		"tools/.venv/pyvenv.cfg":            true,
		"release/ytqjk-python-rollback.zip": true,
	}
	for path, wantForbidden := range pathCases {
		if got := forbiddenPath(path) != ""; got != wantForbidden {
			t.Errorf("forbiddenPath(%q) = %t, want %t", path, got, wantForbidden)
		}
	}
	contentCases := map[string]bool{
		"go run ./cmd/ytqjk":                 false,
		"python3 ./legacy.py":                true,
		"pip install -r requirements.txt":    true,
		"set -euo pipefail":                  false,
		"signed rollback artifact is remote": false,
	}
	for content, wantForbidden := range contentCases {
		if got := activeReference.MatchString(content); got != wantForbidden {
			t.Errorf("activeReference(%q) = %t, want %t", content, got, wantForbidden)
		}
	}
	sourceCases := []struct {
		name      string
		path      string
		content   string
		forbidden bool
	}{
		{"Go data word", "internal/words.go", `package words; var values = []string{"python"}`, false},
		{"Go process", "internal/process.go", `package process; import "os/exec"; var command = exec.Command("python3", "job.py")`, true},
		{"workflow command", ".github/workflows/check.yml", "run: pip install -r requirements.txt", true},
		{"frozen rollback asset", ".github/workflows/release.yml", "ytqjk-python-rollback.zip \\", false},
		{"frozen rollback source count", ".github/workflows/release.yml", `| awk '/\.py$/ { count++ } END { print count + 0 }'`, false},
		{"release Python command", ".github/workflows/release.yml", "run: python3 ./rollback.py", true},
		{"unapproved rollback asset", ".github/workflows/release.yml", "custom-python-rollback.zip", true},
		{"documentation", "README.md", "python3 ./legacy.py", false},
	}
	for _, testCase := range sourceCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := len(contentViolations(testCase.path, []byte(testCase.content))) != 0
			if got != testCase.forbidden {
				t.Errorf("contentViolations(%q) = %t, want %t", testCase.path, got, testCase.forbidden)
			}
		})
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	command := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return strings.TrimSpace(string(output))
}

func trackedPaths(t *testing.T, root string) []string {
	t.Helper()
	command := exec.Command("git", "-C", root, "ls-files", "-z", "--cached")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list tracked files: %v", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			paths = append(paths, filepath.ToSlash(string(part)))
		}
	}
	return paths
}

func forbiddenPath(path string) string {
	normalized := strings.ToLower(filepath.ToSlash(path))
	parts := strings.Split(normalized, "/")
	for _, part := range parts {
		switch part {
		case "__pycache__", ".venv", "venv", ".pytest_cache":
			return "forbidden runtime/cache directory"
		}
	}
	base := parts[len(parts)-1]
	if activeReference.MatchString(base) {
		return "forbidden runtime path"
	}
	switch filepath.Ext(base) {
	case ".py", ".pyc", ".pyo", ".pyd":
		return "forbidden runtime file"
	}
	switch base {
	case ".python-version", "pipfile", "pipfile.lock", "poetry.lock", "pyproject.toml",
		"pytest.ini", "tox.ini", "uv.lock":
		return "forbidden runtime manifest"
	}
	if strings.HasPrefix(base, "requirements") &&
		(strings.HasSuffix(base, ".txt") || strings.HasSuffix(base, ".in")) {
		return "forbidden dependency manifest"
	}
	return ""
}

func contentViolations(path string, content []byte) []string {
	if operationalText(path) {
		return matchingLines(path, content, activeReference)
	}
	if productionGo(path) {
		return goSourceViolations(path, content)
	}
	return nil
}

func operationalText(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	base := filepath.Base(normalized)
	extension := filepath.Ext(base)
	switch extension {
	case ".sh", ".ps1", ".cmd", ".bat":
		return true
	case ".yml", ".yaml":
		return strings.HasPrefix(normalized, ".github/workflows/")
	case ".json":
		return base == "hooks.json" || strings.Contains(normalized, "/.codex-plugin/")
	default:
		return false
	}
}

func productionGo(path string) bool {
	normalized := strings.ToLower(filepath.ToSlash(path))
	return strings.HasSuffix(normalized, ".go") &&
		!strings.HasSuffix(normalized, "_test.go") &&
		(strings.HasPrefix(normalized, "cmd/") || strings.HasPrefix(normalized, "internal/"))
}

func matchingLines(path string, content []byte, pattern *regexp.Regexp) []string {
	lines := strings.Split(string(content), "\n")
	violations := make([]string, 0)
	for index, line := range lines {
		if pattern.MatchString(line) && !approvedFrozenRollbackLine(path, line) {
			violations = append(violations, fmt.Sprintf("%s:%d: forbidden active reference", path, index+1))
		}
	}
	return violations
}

func approvedFrozenRollbackLine(path, line string) bool {
	if filepath.ToSlash(path) != ".github/workflows/release.yml" {
		return false
	}
	_, approved := map[string]struct{}{
		`echo "::error::Frozen Python rollback tag moved"`:                                        {},
		`echo "::error::Frozen Python rollback source is empty"`:                                  {},
		`| awk '/\.py$/ { count++ } END { print count + 0 }'`:                                     {},
		`--prefix="ytqjk-python-rollback-v0.6.10/" \`:                                             {},
		`--output="${dist}/ytqjk-python-rollback.zip" \`:                                          {},
		`ytqjk-python-rollback.zip`:                                                               {},
		`ytqjk-python-rollback.zip \`:                                                             {},
		`rollback:{asset:"ytqjk-python-rollback.zip",tag:$rollback_tag,commit:$rollback_commit},`: {},
		`.rollback == {asset:"ytqjk-python-rollback.zip",tag:"v0.6.10",`:                          {},
		`echo "REAL_ACCEPTANCE_REQUIRED: clean install, old-to-Go, Go-to-Python, Python-to-same-Go, zero-loss receipts, and JIT authorization remain mandatory." >> "${GITHUB_STEP_SUMMARY}"`: {},
	}[strings.TrimSpace(line)]
	return approved
}

func goSourceViolations(path string, content []byte) []string {
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, content, parser.ParseComments)
	if err != nil {
		return []string{fmt.Sprintf("%s: parse Go source: %v", path, err)}
	}
	usesProcessPackage := false
	for _, imported := range file.Imports {
		if imported.Path.Value == `"os/exec"` {
			usesProcessPackage = true
			break
		}
	}
	violations := make([]string, 0)
	ast.Inspect(file, func(node ast.Node) bool {
		literal, ok := node.(*ast.BasicLit)
		if !ok || literal.Kind != token.STRING {
			return true
		}
		value, unquoteErr := strconv.Unquote(literal.Value)
		if unquoteErr != nil {
			return true
		}
		if fileReference.MatchString(value) || usesProcessPackage && activeReference.MatchString(value) {
			position := fileSet.Position(literal.Pos())
			violations = append(violations,
				fmt.Sprintf("%s:%d: forbidden source reference", path, position.Line))
		}
		return true
	})
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if strings.Contains(comment.Text, "go:generate") && activeReference.MatchString(comment.Text) {
				position := fileSet.Position(comment.Pos())
				violations = append(violations,
					fmt.Sprintf("%s:%d: forbidden generator reference", path, position.Line))
			}
		}
	}
	return violations
}

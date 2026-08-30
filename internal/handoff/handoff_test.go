package handoff

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExportAndApply(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	worker := filepath.Join(root, "worker")
	integration := filepath.Join(root, "integration")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, origin, "init")
	git(t, origin, "config", "user.name", "YTQJK Test")
	git(t, origin, "config", "user.email", "ytqjk@example.invalid")
	git(t, origin, "config", "core.autocrlf", "false")
	writeTestFile(t, filepath.Join(origin, "tracked.txt"), "base\n")
	git(t, origin, "add", "tracked.txt")
	git(t, origin, "commit", "-m", "base")
	clone(t, root, origin, worker)
	clone(t, root, origin, integration)
	writeTestFile(t, filepath.Join(worker, "tracked.txt"), "base\nworker change\n")
	writeTestFile(t, filepath.Join(worker, "new.txt"), "new payload\n")
	bundle := filepath.Join(root, "handoff-bundle")
	exported, err := Export(worker, bundle, []string{"tracked.txt", "new.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if len(exported.Paths) != 2 || exported.BundleSHA256 == "" {
		t.Fatalf("export = %#v", exported)
	}
	applied, err := Apply(integration, bundle)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(applied.StagedPaths, ",") != "new.txt,tracked.txt" || applied.StagedSnapshotHash == "" {
		t.Fatalf("apply = %#v", applied)
	}
	status := gitOutputForTest(t, integration, "status", "--porcelain=v1")
	if !strings.Contains(status, "A  new.txt") || !strings.Contains(status, "M  tracked.txt") {
		t.Fatalf("unexpected integration status: %q", status)
	}
}

func TestNormalizePathRejectsMetadata(t *testing.T) {
	for _, value := range []string{"../secret", "/absolute", ".git/config", "C:\\secret"} {
		if _, err := normalizePath(value); err == nil {
			t.Fatalf("normalizePath(%q) accepted unsafe input", value)
		}
	}
}

func clone(t *testing.T, directory, source, destination string) {
	t.Helper()
	command := exec.Command("git", "-c", "core.autocrlf=false", "clone", "--quiet", source, destination)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v: %s", err, output)
	}
	git(t, destination, "config", "core.autocrlf", "false")
}

func git(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func gitOutputForTest(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func writeTestFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

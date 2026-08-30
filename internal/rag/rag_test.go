package rag

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestBootstrapAndSessionQuery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "config", "user.name", "YTQJK Test")
	runTestGit(t, repo, "config", "user.email", "ytqjk@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Architecture\n\nThe runtime uses the Go programming language.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "notes.md")
	runTestGit(t, repo, "commit", "-m", "initial")
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	bootstrap, err := Bootstrap(knowledgeRoot, repo, "off")
	if err != nil || bootstrap.Project.Stats.Files != 1 {
		t.Fatalf("bootstrap = %#v, %v", bootstrap, err)
	}
	identity, err := IdentifyProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Query(knowledgeRoot, repo, "Go language", "session-1", identity.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "PROJECT_CACHE_HIT" || receipt.ResultCount != 1 || !receipt.AnchorCreated {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

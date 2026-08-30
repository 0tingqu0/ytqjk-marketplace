package install

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKnowledgeOnlyApplyAndUninstall(t *testing.T) {
	source := t.TempDir()
	skill := filepath.Join(source, "plugins", "ytqjk-knowledge", "skills", "ytqjk-knowledge")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte("---\nname: ytqjk-knowledge\ndescription: test\n---\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target")
	plan, err := BuildPlan("knowledge-only", source, target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := Apply(ApplyOptions{Plan: plan, Target: target, SourceRoot: source, CodexRoot: filepath.Join(t.TempDir(), "codex")})
	if err != nil {
		t.Fatal(err)
	}
	installed := filepath.Join(target, "skills", "ytqjk-knowledge", "SKILL.md")
	if !result.Changed {
		t.Fatal("first apply did not report a change")
	}
	if _, err := os.Stat(installed); err != nil {
		t.Fatal(err)
	}
	second, err := Apply(ApplyOptions{Plan: plan, Target: target, SourceRoot: source, CodexRoot: filepath.Join(t.TempDir(), "codex")})
	if err != nil || second.Changed {
		t.Fatalf("idempotent apply = %#v, %v", second, err)
	}
	uninstallPlan, err := BuildUninstallPlan("knowledge-only", target)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := Uninstall(ApplyOptions{Plan: uninstallPlan, Target: target, SourceRoot: source, CodexRoot: filepath.Join(t.TempDir(), "codex")})
	if err != nil || !removed.Changed {
		t.Fatalf("uninstall = %#v, %v", removed, err)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("managed skill remains: %v", err)
	}
}

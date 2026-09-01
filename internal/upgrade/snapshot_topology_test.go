package upgrade

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestSnapshotRestoreAllowsMissingKnowledgeRoot(t *testing.T) {
	for _, restoreData := range []bool{false, true} {
		name := "activation-only"
		if restoreData {
			name = "activation-and-data"
		}
		t.Run(name, func(t *testing.T) {
			plan := newSnapshotTestPlan(t)
			binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
			writeFixture(t, binary, "snapshot-binary")
			snapshot, err := captureSnapshot(context.Background(), plan)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, binary, "live-binary")
			if err := os.RemoveAll(plan.KnowledgeRoot); err != nil {
				t.Fatal(err)
			}
			if err := restoreSnapshot(plan, snapshot, restoreData); err != nil {
				t.Fatal(err)
			}
			assertFixture(t, binary, "snapshot-binary")
			if _, err := os.Lstat(plan.KnowledgeRoot); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("missing knowledge root was created: %v", err)
			}
		})
	}
}

func TestPrepareRejectsNestedRootsBeforeCreatingThem(t *testing.T) {
	base := t.TempDir()
	runtimeRoot := filepath.Join(base, "runtime")
	codexRoot := filepath.Join(runtimeRoot, "codex")
	_, err := absolutePrepareRoots(PrepareOptions{
		RuntimeRoot: runtimeRoot, CodexRoot: codexRoot,
		KnowledgeRoot: filepath.Join(base, "knowledge"), Port: 8765,
	})
	if errorCodeOf(err) != "UPGRADE_OPTIONS_INVALID" {
		t.Fatalf("nested root error = %v", err)
	}
	for _, path := range []string{runtimeRoot, filepath.Join(base, "knowledge")} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("invalid root was created at %s: %v", path, err)
		}
	}
}

func TestSnapshotRestoreRejectsEveryTargetOverlap(t *testing.T) {
	base := t.TempDir()
	items := []restoreItem{
		{Target: filepath.Join(base, "a")},
		{Target: filepath.Join(base, "a-b")},
		{Target: filepath.Join(base, "a", "nested")},
	}
	if err := validateRestoreTargetSet(items); err == nil {
		t.Fatal("restore accepted non-adjacent ancestor targets")
	}
}

func TestSnapshotRestoreRejectsWindowsCaseDuplicate(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path comparison only")
	}
	target := filepath.Join(t.TempDir(), "CaseTarget")
	items := []restoreItem{{Target: target}, {Target: strings.ToUpper(target)}}
	if err := validateRestoreTargetSet(items); err == nil {
		t.Fatal("restore accepted a case-only duplicate target")
	}
}

package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestExtraRootsAreCanonicalSortedAndDeduplicated(t *testing.T) {
	scope := newTestScope(t)
	first := t.TempDir()
	second := t.TempDir()
	scope.ExtraRoots = []string{second, first, second, filepath.Clean(first)}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	resources := permit.Fence().Resources
	if !sort.StringsAreSorted(resources) || len(resources) != 5 {
		t.Fatalf("resources = %v", resources)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := lease.Complete(OutcomeAborted)
	if err != nil {
		t.Fatal(err)
	}
	if !sameStrings(receipt.Resources, resources) {
		t.Fatalf("receipt resources = %v, want %v", receipt.Resources, resources)
	}
}

func TestRecoveryRejectsExtraRootMismatch(t *testing.T) {
	scope := newTestScope(t)
	scope.ExtraRoots = []string{t.TempDir()}
	writeStaleRecord(t, scope, staleRecord(deadOwner(), StateDraining, 3, false))
	other := scope
	other.ExtraRoots = []string{t.TempDir()}
	_, err := RecoverExclusive(context.Background(), other, operationA, 3)
	assertCode(t, err, CodeRecoveryRequired)
}

func TestProspectiveRootAndFileMayBeCreatedInsidePermit(t *testing.T) {
	scope := newTestScope(t)
	base := t.TempDir()
	root := filepath.Join(base, "future", "knowledge")
	file := filepath.Join(root, "service", "knowledge.sqlite3")
	scope.ProspectiveRoots = []string{root}
	scope.FilePaths = []string{file}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	shared, err := WithShared(context.Background(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Commit(func(Fence) error {
		if _, err := SharedFenceFromContext(shared, Scope{
			ControlRoot:      scope.ControlRoot,
			ProspectiveRoots: []string{root},
			FilePaths:        []string{file},
		}); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		return os.WriteFile(file, []byte("database"), 0o600)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("prospective file was not created: %v", err)
	}
}

func TestProspectiveRootAncestorReplacementFailsClosed(t *testing.T) {
	scope := newTestScope(t)
	base := t.TempDir()
	ancestor := filepath.Join(base, "bound")
	if err := os.Mkdir(ancestor, 0o755); err != nil {
		t.Fatal(err)
	}
	scope.ProspectiveRoots = []string{filepath.Join(ancestor, "future")}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = permit.Release() })
	if err := os.Rename(ancestor, ancestor+".retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(ancestor, 0o755); err != nil {
		t.Fatal(err)
	}
	assertCode(t, permit.CheckFence(context.Background()), CodeRecoveryRequired)
}

func TestExactFileReplacementFailsClosed(t *testing.T) {
	scope := newTestScope(t)
	file := filepath.Join(t.TempDir(), "database.sqlite3")
	if err := os.WriteFile(file, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	scope.FilePaths = []string{file}
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = permit.Release() })
	if err := os.Rename(file, file+".retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertCode(t, permit.CheckFence(context.Background()), CodeRecoveryRequired)
}

func TestProspectiveRootRejectsSymlinkAncestor(t *testing.T) {
	scope := newTestScope(t)
	base := t.TempDir()
	realDirectory := filepath.Join(base, "real")
	link := filepath.Join(base, "link")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDirectory, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	scope.ProspectiveRoots = []string{filepath.Join(link, "future")}
	_, err := AcquireShared(context.Background(), scope)
	assertCode(t, err, CodeInvalid)
}

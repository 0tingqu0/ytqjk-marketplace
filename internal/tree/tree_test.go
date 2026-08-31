package tree

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func fixture(t *testing.T) *Tree {
	t.Helper()
	value, err := New([]Node{
		{NodeID: "global", Title: "Global", Kind: "global"},
		{NodeID: "group", Title: "Group", Kind: "group"},
		{NodeID: "project", Title: "Project", Kind: "project"},
		{NodeID: "middle", Title: "Middle", Kind: "group"},
	}, []Edge{{Parent: "global", Child: "group"}, {Parent: "group", Child: "project"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestPreviewBoundMutationsAndCycleGuard(t *testing.T) {
	value := fixture(t)
	preview, err := value.PreviewInsertBetween("group", "project", "middle")
	if err != nil {
		t.Fatal(err)
	}
	if preview.BaseRevision != 0 || preview.SubtreeSize != 1 || len(preview.PreviewDigest) != 64 {
		t.Fatalf("preview = %#v", preview)
	}
	if err := value.InsertBetween("group", "project", "middle", preview, 0); err != nil {
		t.Fatal(err)
	}
	ancestors, _ := value.Ancestors("project")
	if got := stringsJoin(ancestors); got != "project/middle/group/global" {
		t.Fatalf("ancestors = %s", got)
	}
	move, err := value.PreviewMove("group", "project")
	if err == nil || move.PreviewDigest != "" {
		t.Fatal("cycle-producing move was accepted")
	}
	if err := value.Detach("project", preview, 1); !errors.Is(err, ErrPreviewMismatch) {
		t.Fatalf("mismatched preview error = %v", err)
	}
}

func TestStoreRejectsConcurrentStaleRevision(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tree.sqlite3")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	initial, err := first.BootstrapProjects(ctx, []Node{{NodeID: "project", Title: "Project", Kind: "project"}})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := first.Load(ctx)
	right, _ := second.Load(ctx)
	leftPreview, _ := left.PreviewDetach("project")
	rightPreview, _ := right.PreviewDetach("project")
	if err := left.Detach("project", leftPreview, initial.Revision()); err != nil {
		t.Fatal(err)
	}
	if err := right.Detach("project", rightPreview, initial.Revision()); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	errorsFound := make(chan error, 2)
	for _, candidate := range []struct {
		store *Store
		value *Tree
	}{{first, left}, {second, right}} {
		wait.Add(1)
		go func(store *Store, value *Tree) {
			defer wait.Done()
			errorsFound <- store.Save(ctx, value, initial.Revision())
		}(candidate.store, candidate.value)
	}
	wait.Wait()
	close(errorsFound)
	succeeded, conflicted := 0, 0
	for err := range errorsFound {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicted++
		} else {
			t.Fatalf("save error = %v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("save outcomes success=%d conflict=%d", succeeded, conflicted)
	}
}

func TestMountedNodeRejectsCredentialMetadata(t *testing.T) {
	if _, err := New([]Node{{NodeID: "mount", Title: "token=very-secret-value", Kind: "mounted", MountID: "remote", Capability: "read"}}, nil, 0); err == nil {
		t.Fatal("unsafe mount metadata was accepted")
	}
}

func TestSubtreeMembership(t *testing.T) {
	value := fixture(t)
	members, err := value.Subtree("group")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].NodeID != "group" || members[1].NodeID != "project" {
		t.Fatalf("subtree = %#v", members)
	}
	contained, err := value.Contains("group", "project")
	if err != nil || !contained {
		t.Fatalf("contains = %v, %v", contained, err)
	}
	contained, err = value.Contains("project", "group")
	if err != nil || contained {
		t.Fatalf("reverse contains = %v, %v", contained, err)
	}
}

func stringsJoin(values []string) string {
	result := ""
	for index, value := range values {
		if index > 0 {
			result += "/"
		}
		result += value
	}
	return result
}

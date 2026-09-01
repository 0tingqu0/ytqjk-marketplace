package library

import (
	"errors"
	"sync"
	"testing"
)

func TestCreateRequiresMatchingPreviewAndRevision(t *testing.T) {
	tree := testTree(t)
	request := validGroupRequest()
	request.NodeID = "team"
	request.Title = "Team"
	request.ParentID = stringPointer("global")
	preview, err := tree.PreviewCreate(request)
	if err != nil {
		t.Fatalf("PreviewCreate() error = %v", err)
	}
	if preview.NewChain[0] != "team" || preview.SubtreeSize != 1 {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	assertCode(t, tree.Create(request, preview, 1), "REVISION_CONFLICT")
	forged := preview
	forged.NewChain = []string{"team", "other"}
	assertCode(t, tree.Create(request, forged, 0), "PREVIEW_MISMATCH")
	if err := tree.Create(request, preview, 0); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if tree.Revision() != 1 {
		t.Fatalf("revision = %d", tree.Revision())
	}
	chain, err := tree.Ancestors("team")
	if err != nil || len(chain) != 2 || chain[1] != "global" {
		t.Fatalf("unexpected ancestors: %#v, %v", chain, err)
	}
}

func TestAttachPreviewBindsFullConfiguration(t *testing.T) {
	source := testTree(t)
	preview, err := source.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	nodes := testNodes()
	for index := range nodes {
		if nodes[index].ID == "bridge" {
			nodes[index].CapacityBytes++
		}
	}
	target := mustTree(t, nodes)
	targetPreview, err := target.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	if preview.PreviewDigest == targetPreview.PreviewDigest {
		t.Fatal("configuration change did not invalidate preview")
	}
	assertCode(t, target.Attach("orphan", "alpha", preview, 0), "PREVIEW_MISMATCH")
}

func TestStatisticsDoNotInvalidateTopologyPreview(t *testing.T) {
	source := testTree(t)
	preview, err := source.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	nodes := testNodes()
	nodes[0].Stats = Statistics{UsedBytes: 99}
	target := mustTree(t, nodes)
	targetPreview, err := target.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	if preview.PreviewDigest != targetPreview.PreviewDigest {
		t.Fatal("statistics unexpectedly invalidated preview")
	}
}

func TestDetachMoveAndInsertPreserveSubtrees(t *testing.T) {
	t.Run("detach", func(t *testing.T) {
		tree := testTree(t)
		preview, err := tree.PreviewDetach("alpha")
		if err != nil {
			t.Fatalf("PreviewDetach() error = %v", err)
		}
		if preview.SubtreeSize != 2 {
			t.Fatalf("subtree size = %d", preview.SubtreeSize)
		}
		if err := tree.Detach("alpha", preview, 0); err != nil {
			t.Fatalf("Detach() error = %v", err)
		}
		chain, _ := tree.Ancestors("leaf")
		if len(chain) != 2 || chain[1] != "alpha" {
			t.Fatalf("unexpected chain: %#v", chain)
		}
	})

	t.Run("move", func(t *testing.T) {
		tree := testTree(t)
		preview, err := tree.PreviewMove("alpha", "other")
		if err != nil {
			t.Fatalf("PreviewMove() error = %v", err)
		}
		if err := tree.Move("alpha", "other", preview, 0); err != nil {
			t.Fatalf("Move() error = %v", err)
		}
		chain, _ := tree.Ancestors("leaf")
		if len(chain) != 3 || chain[2] != "other" {
			t.Fatalf("unexpected chain: %#v", chain)
		}
	})

	t.Run("insert between", func(t *testing.T) {
		tree := testTree(t)
		preview, err := tree.PreviewInsertBetween("global", "alpha", "bridge")
		if err != nil {
			t.Fatalf("PreviewInsertBetween() error = %v", err)
		}
		if err := tree.InsertBetween("global", "alpha", "bridge", preview, 0); err != nil {
			t.Fatalf("InsertBetween() error = %v", err)
		}
		chain, _ := tree.Ancestors("alpha")
		if len(chain) != 3 || chain[1] != "bridge" || chain[2] != "global" {
			t.Fatalf("unexpected chain: %#v", chain)
		}
	})
}

func TestMutationsRejectInvalidTopology(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Tree) error
		code string
	}{
		{
			name: "cycle",
			run: func(tree *Tree) error {
				_, err := tree.PreviewMove("alpha", "leaf")
				return err
			},
			code: "CYCLE_DETECTED",
		},
		{
			name: "self parent",
			run: func(tree *Tree) error {
				_, err := tree.PreviewAttach("other", "other")
				return err
			},
			code: "SELF_PARENT",
		},
		{
			name: "unknown",
			run: func(tree *Tree) error {
				_, err := tree.PreviewDetach("missing")
				return err
			},
			code: "UNKNOWN_NODE",
		},
		{
			name: "duplicate edge",
			run: func(tree *Tree) error {
				_, err := tree.PreviewAttach("alpha", "global")
				return err
			},
			code: "DUPLICATE_EDGE",
		},
		{
			name: "multiple parents",
			run: func(tree *Tree) error {
				_, err := tree.PreviewAttach("alpha", "other")
				return err
			},
			code: "MULTIPLE_PARENTS",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertCode(t, test.run(testTree(t)), test.code)
		})
	}
}

func TestRevisionCannotOverflow(t *testing.T) {
	tree, err := NewTree(testNodes(), MaxRevision)
	if err != nil {
		t.Fatalf("NewTree() error = %v", err)
	}
	preview, err := tree.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	assertCode(t, tree.Attach("orphan", "alpha", preview, MaxRevision), "REVISION_EXHAUSTED")
}

func TestConcurrentCASAllowsExactlyOneCommit(t *testing.T) {
	tree := testTree(t)
	alpha, err := tree.PreviewAttach("orphan", "alpha")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}
	other, err := tree.PreviewAttach("orphan", "other")
	if err != nil {
		t.Fatalf("PreviewAttach() error = %v", err)
	}

	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for _, candidate := range []struct {
		parent  string
		preview Preview
	}{{"alpha", alpha}, {"other", other}} {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			results <- tree.Attach("orphan", candidate.parent, candidate.preview, 0)
		}()
	}
	close(start)
	workers.Wait()
	close(results)

	succeeded := 0
	conflicted := 0
	for result := range results {
		if result == nil {
			succeeded++
			continue
		}
		var contractErr *Error
		if errors.As(result, &contractErr) && contractErr.Code == "REVISION_CONFLICT" {
			conflicted++
			continue
		}
		t.Fatalf("unexpected commit result: %v", result)
	}
	if succeeded != 1 || conflicted != 1 || tree.Revision() != 1 {
		t.Fatalf("succeeded=%d conflicted=%d revision=%d", succeeded, conflicted, tree.Revision())
	}
}

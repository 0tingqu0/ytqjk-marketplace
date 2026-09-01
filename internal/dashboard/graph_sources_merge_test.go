package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestGraphProjectIndexCannotOverrideDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	writeMergedGraphProjectIndex(t, root, "project-a", "project-b")
	sources, _, _ := loadGraphSources(root)
	if len(sources) != 0 {
		t.Fatalf("mismatched project identity was accepted: %#v", sources)
	}
}

func TestGraphProjectIndexRequiresCanonicalIdentity(t *testing.T) {
	root := t.TempDir()
	writeMergedGraphProjectIndex(t, root, "project-a", "")
	sources, _, _ := loadGraphSources(root)
	if len(sources) != 0 {
		t.Fatalf("empty project identity was accepted: %#v", sources)
	}
}

func TestGraphProjectDirectoryBindsCanonicalIdentity(t *testing.T) {
	root := t.TempDir()
	writeMergedGraphProjectIndex(t, root, "project-a", "project-a")
	sources, _, _ := loadGraphSources(root)
	if len(sources) != 1 || sources[0].ProjectID != "project-a" || sources[0].Scope != "project:project-a" {
		t.Fatalf("canonical project identity was not loaded: %#v", sources)
	}
}

func TestGraphSourceMergeRemovesChunkOverlap(t *testing.T) {
	documents := groupGraphDocuments([]graphSource{
		{Scope: "project:a", ProjectID: "a", Path: "doc.md", Start: 0, End: 3, Content: "abc"},
		{Scope: "project:a", ProjectID: "a", Path: "doc.md", Start: 2, End: 5, Content: "cde"},
	})
	if len(documents) != 1 || documents[0].Content != "abcde" {
		t.Fatalf("overlapping chunks were not merged: %#v", documents)
	}
}

func TestGraphSourceBudgetBoundsCountAndBytes(t *testing.T) {
	byteBudget := graphSourceBudget{}
	if !byteBudget.allow(maxGraphIndexBytes) || !byteBudget.allow(maxGraphIndexBytes) {
		t.Fatal("valid total byte budget was rejected")
	}
	if byteBudget.allow(1) {
		t.Fatal("total index byte budget was exceeded")
	}
	countBudget := graphSourceBudget{}
	for index := 0; index < maxGraphProjectIndexes; index++ {
		if !countBudget.allow(1) {
			t.Fatalf("index %d rejected before count limit", index)
		}
	}
	if countBudget.allow(1) {
		t.Fatal("index count budget was exceeded")
	}
}

func writeMergedGraphProjectIndex(t *testing.T, root, directoryID, declaredID string) {
	t.Helper()
	directory := filepath.Join(root, "projects", directoryID)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# 归属测试\n[[知识图谱]]"
	digest := safeio.SHA256([]byte(content))
	index := rag.Index{
		SchemaVersion: rag.SchemaVersion,
		ProjectID:     declaredID,
		Chunks: []rag.Chunk{{
			ID: digest, Path: "docs/test.md", Start: 0, End: len([]rune(content)),
			Content: content, Digest: digest, LineStart: 1, LineEnd: 2,
		}},
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "index.json"), index); err != nil {
		t.Fatal(err)
	}
	manifest := rag.Manifest{
		SchemaVersion: rag.SchemaVersion,
		Identity:      rag.ProjectIdentity{ID: directoryID, Name: directoryID},
		Vector:        map[string]any{"enabled": false},
		IndexedAt:     "2026-09-01T00:00:00Z",
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		t.Fatal(err)
	}
}

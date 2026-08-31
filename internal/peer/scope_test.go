package peer

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

func TestPeerQueryReadsOnlyConfiguredSubtree(t *testing.T) {
	root := t.TempDir()
	projectID := "shared-project"
	value := peerTree(t, projectID)
	writeCatalog(t, root, projectID, "foreign-project", "peer-project")
	insideID := writePeerIndex(t, filepath.Join(root, "libraries", "inside"), "inside", "AUTHORIZED_MARKER")
	writePeerIndex(t, filepath.Join(root, "libraries", "sibling"), "sibling", "SIBLING_MARKER")
	writePeerIndex(t, filepath.Join(root, "projects", "foreign-project"), "foreign-project", "FOREIGN_MARKER")
	writePeerIndex(t, filepath.Join(root, "libraries", "foreign-child"), "foreign-child", "FOREIGN_CHILD_MARKER")
	writePeerIndex(t, filepath.Join(root, "libraries", "transitive"), "transitive", "TRANSITIVE_MARKER")

	result, err := QueryLibrarySubtree(root, projectID, "export", "AUTHORIZED_MARKER", 5, value)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "PEER_HIT" || len(result.Results) != 1 || result.Results[0].LibraryNode != "inside" {
		t.Fatalf("result = %#v", result)
	}
	material, err := FetchSubtreeMaterial(root, projectID, "export", "inside", "library:"+insideID, value)
	if err != nil || material.Content != "AUTHORIZED_MARKER" {
		t.Fatalf("material = %#v, %v", material, err)
	}
	for _, marker := range []string{"SIBLING_MARKER", "FOREIGN_MARKER", "FOREIGN_CHILD_MARKER", "TRANSITIVE_MARKER"} {
		miss, err := QueryLibrarySubtree(root, projectID, "export", marker, 5, value)
		if err != nil || miss.Status != "PEER_MISS" || len(miss.Results) != 0 {
			t.Fatalf("marker %s escaped scope: %#v, %v", marker, miss, err)
		}
	}
	if _, err := FetchSubtreeMaterial(root, projectID, "export", "sibling", "library:"+insideID, value); err == nil || err.Error() != "PEER_LIBRARY_OUTSIDE_EXPORT" {
		t.Fatalf("outside material error = %v", err)
	}
}

func TestExportCatalogListsOnlyExplicitRoots(t *testing.T) {
	root := t.TempDir()
	projectID := "shared-project"
	value := peerTree(t, projectID)
	writeCatalog(t, root, projectID, "foreign-project", "peer-project")
	exports, count, err := ExportCatalog(root, projectID, []string{"export", "peer-project"}, value)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 2 || exports[0].ID != "export" || exports[1].ID != "peer-project" || count != 3 {
		t.Fatalf("exports=%#v count=%d", exports, count)
	}
	if _, _, err := ExportCatalog(root, projectID, []string{"sibling", "sibling"}, value); err == nil {
		t.Fatal("duplicate export roots accepted")
	}
}

func peerTree(t *testing.T, projectID string) *tree.Tree {
	t.Helper()
	value, err := tree.New([]tree.Node{
		{NodeID: "global", Title: "Global", Kind: "global"},
		{NodeID: projectID, Title: "Shared", Kind: "project"},
		{NodeID: "export", Title: "Export", Kind: "group"},
		{NodeID: "inside", Title: "Inside", Kind: "group"},
		{NodeID: "sibling", Title: "Sibling", Kind: "group"},
		{NodeID: "foreign-project", Title: "Foreign", Kind: "project"},
		{NodeID: "peer-project", Title: "Peer project", Kind: "project"},
		{NodeID: "foreign-child", Title: "Foreign child", Kind: "group"},
		{NodeID: "peer-mount", Title: "Peer mount", Kind: "mounted", MountID: "third-peer", Capability: "query-v1"},
		{NodeID: "transitive", Title: "Transitive", Kind: "group"},
	}, []tree.Edge{
		{Parent: "global", Child: projectID},
		{Parent: projectID, Child: "export"},
		{Parent: projectID, Child: "sibling"},
		{Parent: "export", Child: "inside"},
		{Parent: "export", Child: "foreign-project"},
		{Parent: "foreign-project", Child: "foreign-child"},
		{Parent: "export", Child: "peer-mount"},
		{Parent: "peer-mount", Child: "transitive"},
		{Parent: "global", Child: "peer-project"},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func writeCatalog(t *testing.T, root string, projects ...string) {
	t.Helper()
	catalog := rag.Catalog{SchemaVersion: rag.SchemaVersion, Projects: map[string]rag.CatalogProject{}}
	for _, project := range projects {
		catalog.Projects[project] = rag.CatalogProject{Name: project}
	}
	if err := safeio.WriteJSON(filepath.Join(root, "catalog.json"), catalog); err != nil {
		t.Fatal(err)
	}
}

func writePeerIndex(t *testing.T, directory, projectID, content string) string {
	t.Helper()
	contentDigest := sha256.Sum256([]byte(content))
	idDigest := sha256.Sum256([]byte(projectID + ":" + content))
	identifier := hex.EncodeToString(idDigest[:])
	chunk := rag.Chunk{
		ID: identifier, Path: "verified/material.md", Start: 0, End: len([]rune(content)),
		Content: content, Digest: hex.EncodeToString(contentDigest[:]),
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "index.json"), rag.Index{SchemaVersion: rag.SchemaVersion, ProjectID: projectID, Chunks: []rag.Chunk{chunk}}); err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "manifest.json"), rag.Manifest{SchemaVersion: rag.SchemaVersion, SourceFingerprint: identifier}); err != nil {
		t.Fatal(err)
	}
	return identifier
}

package dashboard

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func TestDashboardSearchUsesGoHybridVectors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "personal-experience", "approved", "migration.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("transactional migration rollback snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := rag.BuildGlobal(root, "auto"); err != nil {
		t.Fatal(err)
	}
	results, err := searchAll(root, "migratoin", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 || results[0].Mode != "HYBRID" || results[0].VectorScore <= 0 {
		t.Fatalf("hybrid results = %#v", results)
	}
}

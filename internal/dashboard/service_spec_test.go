package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPrepareServiceSpecBindsRegularPaths(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "bin", "ytqjk")
	assets := filepath.Join(root, "dashboard")
	knowledge := filepath.Join(root, "knowledge")
	if err := os.MkdirAll(filepath.Dir(binary), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(assets, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assets, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	spec, err := prepareServiceSpec(binary, knowledge, assets, 8765)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Port != 8765 || spec.Binary != binary || spec.Assets != assets || spec.KnowledgeRoot != knowledge {
		t.Fatalf("spec=%#v", spec)
	}
	if _, err := prepareServiceSpec(binary+"\n", knowledge, assets, 8765); err == nil {
		t.Fatal("service path with a control character was accepted")
	}
}

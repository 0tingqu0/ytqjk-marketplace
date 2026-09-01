package platform

import (
	"path/filepath"
	"testing"
)

func TestMaintenanceControlRootIsStableRuntimeParent(t *testing.T) {
	runtimeRoot, err := RuntimeRoot()
	if err != nil {
		t.Fatal(err)
	}
	controlRoot, err := MaintenanceControlRoot()
	if err != nil {
		t.Fatal(err)
	}
	if controlRoot != filepath.Dir(runtimeRoot) {
		t.Fatalf("control root = %q, want %q", controlRoot, filepath.Dir(runtimeRoot))
	}
}

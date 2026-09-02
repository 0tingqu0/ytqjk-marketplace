package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestRuntimeLauncherExecutesActiveGeneration(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source", runtimeentry.BinaryName())
	if output, err := exec.Command("go", "build", "-o", source, ".").CombinedOutput(); err != nil {
		t.Fatalf("build runtime fixture: %v: %s", err, output)
	}
	digest, err := safeio.FileSHA256(source)
	if err != nil {
		t.Fatal(err)
	}
	generation := strings.Repeat("a", 64)
	manifest, err := runtimeentry.MaterializeGeneration(
		runtimeRoot, generation, buildinfo.Version, source, digest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtimeentry.InstallLauncher(runtimeRoot, source, digest); err != nil {
		t.Fatal(err)
	}
	if err := runtimeentry.Activate(runtimeRoot, manifest); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(runtimeentry.LauncherPath(runtimeRoot), "version").CombinedOutput()
	if err != nil || strings.TrimSpace(string(output)) != buildinfo.Version {
		t.Fatalf("launcher version: %v: %q", err, output)
	}
}

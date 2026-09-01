package install

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
)

func TestInstallRuntimeMaterializesImmutableGeneration(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	source := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(source, []byte("go-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := InstallRuntime(runtimeRoot, source, "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "ACTIVE" || !first.Changed || first.Generation == "" {
		t.Fatalf("first=%#v", first)
	}
	active, target, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if active.Generation != first.Generation || target == runtimeentry.LauncherPath(runtimeRoot) {
		t.Fatalf("active=%#v target=%q", active, target)
	}
	second, err := InstallRuntime(runtimeRoot, source, "0.7.0")
	if err != nil || second.Changed {
		t.Fatalf("second=%#v error=%v", second, err)
	}
}

func TestInstallRuntimeRejectsUnboundLegacyLauncher(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(filepath.Join(runtimeRoot, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := runtimeentry.LauncherPath(runtimeRoot)
	if err := os.WriteFile(launcher, []byte("legacy"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(source, []byte("new"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRuntime(runtimeRoot, source, "0.7.0"); err == nil {
		t.Fatal("legacy launcher was overwritten without upgrade binding")
	}
	data, err := os.ReadFile(launcher)
	if err != nil || string(data) != "legacy" {
		t.Fatalf("legacy launcher=%q error=%v", data, err)
	}
}

func TestInstallRuntimeRejectsUpdateOutsideUpgradeWorkflow(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	firstSource := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	secondSource := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(firstSource, []byte("first"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondSource, []byte("second"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := InstallRuntime(runtimeRoot, firstSource, "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InstallRuntime(runtimeRoot, secondSource, "0.7.0"); err == nil {
		t.Fatal("runtime update bypassed the authenticated upgrade workflow")
	}
	active, _, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil || active.Generation != first.Generation {
		t.Fatalf("active=%#v error=%v", active, err)
	}
}

func TestRollbackFreshRuntimeRemovesBoundRuntime(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	source := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(source, []byte("go-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := InstallRuntime(runtimeRoot, source, "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	if err := RollbackFreshRuntime(runtimeRoot, installed); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		runtimeentry.ActiveManifestPath(runtimeRoot),
		runtimeentry.LauncherPath(runtimeRoot),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("path %q remains after rollback: %v", path, err)
		}
	}
}

func TestUninstallRuntimeRequiresExternalBootstrap(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	source := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(source, []byte("go-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := InstallRuntime(runtimeRoot, source, "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	launcher := runtimeentry.LauncherPath(runtimeRoot)
	if err := ValidateRuntimeUninstall(runtimeRoot, launcher); err == nil {
		t.Fatal("self-hosted runtime uninstall was accepted")
	}
	active, _, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil || active.Generation != installed.Generation {
		t.Fatalf("active=%#v error=%v", active, err)
	}
}

func TestUninstallRuntimeRemovesActiveGeneration(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	source := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	caller := filepath.Join(t.TempDir(), runtimeentry.BinaryName())
	if err := os.WriteFile(source, []byte("go-runtime"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caller, []byte("bootstrap"), 0o700); err != nil {
		t.Fatal(err)
	}
	installed, err := InstallRuntime(runtimeRoot, source, "0.7.0")
	if err != nil {
		t.Fatal(err)
	}
	result, err := UninstallRuntime(runtimeRoot, caller)
	if err != nil || result.Status != "REMOVED" || result.Generation != installed.Generation {
		t.Fatalf("result=%#v error=%v", result, err)
	}
	for _, path := range []string{
		runtimeentry.ActiveManifestPath(runtimeRoot),
		runtimeentry.LauncherPath(runtimeRoot),
	} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("path %q remains after uninstall: %v", path, err)
		}
	}
}

package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
)

func TestTopLevelHelpDoesNotInvokeInstaller(t *testing.T) {
	for _, argument := range []string{"--help", "-h", "help"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if exitCode := Main([]string{argument}, strings.NewReader(""), &output, &output); exitCode != 0 {
				t.Fatalf("Main(%q) exit code = %d, output = %q", argument, exitCode, output.String())
			}
			if !strings.Contains(output.String(), "YTQJK local orchestration and knowledge runtime (Go)") {
				t.Fatalf("help output missing heading: %q", output.String())
			}
			if strings.Contains(output.String(), `"ok":false`) {
				t.Fatalf("help was routed through installer: %q", output.String())
			}
		})
	}
}

func TestUpgradeSchemaVersionIsMachineReadable(t *testing.T) {
	var output bytes.Buffer
	if exitCode := Main([]string{"upgrade", "schema-version"}, strings.NewReader(""), &output, &output); exitCode != 0 {
		t.Fatalf("schema-version exit code = %d, output = %q", exitCode, output.String())
	}
	if strings.TrimSpace(output.String()) != strconv.Itoa(knowledge.LatestSchema) {
		t.Fatalf("schema-version output = %q", output.String())
	}
}

func TestVersionAliasesMatchBuildInfo(t *testing.T) {
	for _, argument := range []string{"version", "--version", "-version"} {
		t.Run(argument, func(t *testing.T) {
			var output bytes.Buffer
			if exitCode := Main([]string{argument}, strings.NewReader(""), &output, &output); exitCode != 0 {
				t.Fatalf("Main(%q) exit code = %d, output = %q", argument, exitCode, output.String())
			}
			if strings.TrimSpace(output.String()) != buildinfo.Version {
				t.Fatalf("Main(%q) version = %q, want %q", argument, output.String(), buildinfo.Version)
			}
		})
	}
}

func TestInstallActivatesStableRuntime(t *testing.T) {
	dataRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", dataRoot)
	} else {
		t.Setenv("XDG_DATA_HOME", dataRoot)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	arguments := []string{
		"install", "--mode", "knowledge-only",
		"--source-root", repositoryRoot,
		"--target-root", filepath.Join(t.TempDir(), "target"),
		"--codex-root", filepath.Join(t.TempDir(), "codex"),
		"--knowledge-root", filepath.Join(t.TempDir(), "knowledge"),
		"--codex-import", "off", "--project-bootstrap", "off",
		"--dashboard-service", "off", "--apply", "--yes", "--json",
	}
	var output bytes.Buffer
	if exitCode := Main(arguments, strings.NewReader(""), &output, &output); exitCode != 0 {
		t.Fatalf("install exit code = %d, output = %q", exitCode, output.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	runtimeReceipt, ok := receipt["runtime"].(map[string]any)
	if !ok || runtimeReceipt["status"] != "ACTIVE" || runtimeReceipt["changed"] != true {
		t.Fatalf("runtime receipt = %#v", receipt["runtime"])
	}
	runtimeRoot, err := platform.RuntimeRoot()
	if err != nil {
		t.Fatal(err)
	}
	active, target, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if active.Version != buildinfo.Version || target == runtimeentry.LauncherPath(runtimeRoot) {
		t.Fatalf("active = %#v, target = %q", active, target)
	}
}

func TestUninstallSkipsDashboardRemovalWhenDisabled(t *testing.T) {
	dataRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("LOCALAPPDATA", dataRoot)
	} else {
		t.Setenv("XDG_DATA_HOME", dataRoot)
	}
	repositoryRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	targetRoot := filepath.Join(t.TempDir(), "target")
	codexRoot := filepath.Join(t.TempDir(), "codex")
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	common := []string{
		"--mode", "knowledge-only", "--source-root", repositoryRoot,
		"--target-root", targetRoot, "--codex-root", codexRoot,
		"--knowledge-root", knowledgeRoot, "--codex-import", "off",
		"--project-bootstrap", "off", "--dashboard-service", "off",
		"--apply", "--yes", "--json",
	}
	var installOutput bytes.Buffer
	if exitCode := Main(append([]string{"install"}, common...), strings.NewReader(""), &installOutput, &installOutput); exitCode != 0 {
		t.Fatalf("install exit code = %d, output = %q", exitCode, installOutput.String())
	}
	var uninstallOutput bytes.Buffer
	arguments := append([]string{"install", "--uninstall"}, common...)
	if exitCode := Main(arguments, strings.NewReader(""), &uninstallOutput, &uninstallOutput); exitCode != 0 {
		t.Fatalf("uninstall exit code = %d, output = %q", exitCode, uninstallOutput.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(uninstallOutput.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	dashboardReceipt, ok := receipt["dashboard_service"].(map[string]any)
	if !ok || dashboardReceipt["status"] != "SKIPPED_OFF" {
		t.Fatalf("dashboard receipt = %#v", receipt["dashboard_service"])
	}
	runtimeReceipt, ok := receipt["runtime"].(map[string]any)
	if !ok || runtimeReceipt["status"] != "REMOVED" {
		t.Fatalf("runtime receipt = %#v", receipt["runtime"])
	}
}

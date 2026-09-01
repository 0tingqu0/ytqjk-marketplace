package maintenance

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestMaintenanceProcessHelper(t *testing.T) {
	mode := os.Getenv("YTQJK_MAINTENANCE_HELPER")
	if mode == "" {
		return
	}
	scope := Scope{
		ControlRoot:   os.Getenv("YTQJK_MAINTENANCE_CONTROL"),
		RuntimeRoot:   os.Getenv("YTQJK_MAINTENANCE_RUNTIME"),
		CodexRoot:     os.Getenv("YTQJK_MAINTENANCE_CODEX"),
		KnowledgeRoot: os.Getenv("YTQJK_MAINTENANCE_KNOWLEDGE"),
	}
	switch mode {
	case "shared":
		permit, err := AcquireShared(context.Background(), scope)
		if err != nil {
			t.Fatal(err)
		}
		writeHelperReady(t)
		waitHelperRelease(t)
		if err := permit.Release(); err != nil {
			t.Fatal(err)
		}
	case "claim":
		generation, err := strconv.ParseUint(os.Getenv("YTQJK_MAINTENANCE_GENERATION"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		lease, err := ClaimExclusive(
			context.Background(), scope, os.Getenv("YTQJK_MAINTENANCE_OPERATION"), generation,
		)
		if err != nil {
			t.Fatal(err)
		}
		writeHelperReady(t)
		if _, err := lease.Complete(OutcomeAborted); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func TestSingleControlPlaneConflictsAcrossDifferentResources(t *testing.T) {
	root := t.TempDir()
	control := filepath.Join(root, "control")
	if err := os.Mkdir(control, 0o700); err != nil {
		t.Fatal(err)
	}
	helper := makeScope(t, control, filepath.Join(root, "helper"))
	parent := makeScope(t, control, filepath.Join(root, "parent"))
	command, output, release := startSharedHelper(t, helper)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := BeginExclusive(ctx, parent, exclusiveOptions(operationA))
	assertCode(t, err, CodeWriterDrainTimeout)
	release()
	if err := command.Wait(); err != nil {
		t.Fatalf("shared helper: %v\n%s", err, output.String())
	}
}

func TestExclusiveTransferAndHelperClaim(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	command, output := helperCommand(t, "claim", scope, ready, "")
	command.Env = append(command.Env,
		"YTQJK_MAINTENANCE_OPERATION="+operationA,
		fmt.Sprintf("YTQJK_MAINTENANCE_GENERATION=%d", lease.Generation()),
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Transfer(command.Process.Pid); err != nil {
		_ = command.Process.Kill()
		t.Fatalf("transfer: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("claim helper: %v\n%s", err, output.String())
	}
	waitForPath(t, ready)
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 1 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func makeScope(t *testing.T, controlRoot, prefix string) Scope {
	t.Helper()
	scope := Scope{
		ControlRoot: controlRoot, RuntimeRoot: prefix + "-runtime",
		CodexRoot: prefix + "-codex", KnowledgeRoot: prefix + "-knowledge",
	}
	for _, path := range []string{scope.RuntimeRoot, scope.CodexRoot, scope.KnowledgeRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := BootstrapControlRoot(context.Background(), scope.ControlRoot); err != nil {
		t.Fatal(err)
	}
	return scope
}

func startSharedHelper(t *testing.T, scope Scope) (*exec.Cmd, *bytes.Buffer, func()) {
	t.Helper()
	directory := t.TempDir()
	ready := filepath.Join(directory, "ready")
	releasePath := filepath.Join(directory, "release")
	command, output := helperCommand(t, "shared", scope, ready, releasePath)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		if command.ProcessState == nil {
			_ = command.Process.Kill()
		}
	})
	waitForPath(t, ready)
	released := false
	return command, output, func() {
		if released {
			return
		}
		released = true
		if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func helperCommand(t *testing.T, mode string, scope Scope, ready, release string) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestMaintenanceProcessHelper$")
	command.Env = append(os.Environ(),
		"YTQJK_MAINTENANCE_HELPER="+mode,
		"YTQJK_MAINTENANCE_CONTROL="+scope.ControlRoot,
		"YTQJK_MAINTENANCE_RUNTIME="+scope.RuntimeRoot,
		"YTQJK_MAINTENANCE_CODEX="+scope.CodexRoot,
		"YTQJK_MAINTENANCE_KNOWLEDGE="+scope.KnowledgeRoot,
		"YTQJK_MAINTENANCE_READY="+ready,
		"YTQJK_MAINTENANCE_RELEASE="+release,
	)
	output := &bytes.Buffer{}
	command.Stdout, command.Stderr = output, output
	return command, output
}

func writeHelperReady(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(os.Getenv("YTQJK_MAINTENANCE_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitHelperRelease(t *testing.T) {
	t.Helper()
	path := os.Getenv("YTQJK_MAINTENANCE_RELEASE")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("helper release timed out")
}

func waitForPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

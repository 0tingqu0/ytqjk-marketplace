package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

func TestSharedKnowledgeBindsNestedFence(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	result, err := withSharedKnowledge(context.Background(), knowledgeRoot, func(ctx context.Context) (any, error) {
		fence, err := maintenance.SharedFenceFromContext(ctx, maintenance.Scope{
			ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
		})
		return fence.Generation, err
	})
	if err != nil || result != uint64(0) {
		t.Fatalf("shared result=%v error=%v", result, err)
	}
}

func TestKnowledgeFileScopeBindsExactExternalDatabasePath(t *testing.T) {
	knowledgeRoot := t.TempDir()
	externalRoot := t.TempDir()
	database := filepath.Join(externalRoot, "not-created", "knowledge.sqlite3")
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	scope, err := knowledgeFileScope(knowledgeRoot, database)
	if err != nil {
		t.Fatal(err)
	}
	result, err := withSharedScope(context.Background(), scope, func(ctx context.Context) (any, error) {
		fence, err := maintenance.SharedFenceFromContext(ctx, maintenance.Scope{
			ControlRoot: controlRoot,
			FilePaths:   []string{database},
		})
		return fence.Resources, err
	})
	if err != nil {
		t.Fatalf("external scope result=%v error=%v", result, err)
	}
	resources, ok := result.([]string)
	if !ok || !slices.Contains(resources, testCanonicalResource(t, database)) ||
		slices.Contains(resources, testCanonicalResource(t, externalRoot)) {
		t.Fatalf("external scope resources=%v", result)
	}
}

func TestSharedKnowledgeCreatesProspectiveRootInsidePermit(t *testing.T) {
	base := t.TempDir()
	knowledgeRoot := filepath.Join(base, "not-created", "knowledge")
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	result, err := withSharedKnowledge(context.Background(), knowledgeRoot, func(context.Context) (any, error) {
		if err := os.MkdirAll(filepath.Join(knowledgeRoot, "service"), 0o755); err != nil {
			return nil, err
		}
		return "created", nil
	})
	if err != nil || result != "created" {
		t.Fatalf("prospective root result=%v error=%v", result, err)
	}
	if info, err := os.Stat(knowledgeRoot); err != nil || !info.IsDir() {
		t.Fatalf("prospective root was not created safely: info=%v error=%v", info, err)
	}
}

func TestFreshKnowledgeRootCommandsCreateInsidePermit(t *testing.T) {
	projectRoot := t.TempDir()
	tests := []struct {
		name      string
		arguments func(string) []string
	}{
		{
			name: "rag",
			arguments: func(root string) []string {
				return []string{"rag", "init", "--knowledge-root", root, "--project-root", projectRoot}
			},
		},
		{
			name: "session",
			arguments: func(root string) []string {
				return []string{
					"session", "anchor", "--knowledge-root", root,
					"--project-root", projectRoot, "--session-id", "session",
				}
			},
		},
		{
			name: "knowledge",
			arguments: func(root string) []string {
				return []string{"knowledge", "create-project", "--knowledge-root", root, "--alias", "project"}
			},
		},
		{
			name: "orchestration",
			arguments: func(root string) []string {
				return []string{
					"orchestration", "start-run", "--knowledge-root", root,
					"--project-id", "project", "--objective-hash", strings.Repeat("c", 64),
					"--session-key", strings.Repeat("d", 64),
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			knowledgeRoot := filepath.Join(t.TempDir(), "fresh", "knowledge")
			withTestMaintenanceControlRoot(t, t.TempDir())
			var output bytes.Buffer
			if code := Main(test.arguments(knowledgeRoot), strings.NewReader(""), &output, &output); code != 0 {
				t.Fatalf("exit=%d output=%q", code, output.String())
			}
			if info, err := os.Stat(knowledgeRoot); err != nil || !info.IsDir() {
				t.Fatalf("knowledge root info=%v error=%v", info, err)
			}
		})
	}
}

func TestSessionMutationFailsClosedDuringMaintenance(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	if err := maintenance.BootstrapControlRoot(context.Background(), controlRoot); err != nil {
		t.Fatal(err)
	}
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("a", 64), Purpose: "CLI_SESSION_ADMISSION_TEST",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = lease.Complete(maintenance.OutcomeAborted) })
	var output bytes.Buffer
	code := Main([]string{
		"session", "anchor", "--knowledge-root", knowledgeRoot,
		"--project-id", "project", "--session-id", "session",
	}, strings.NewReader(""), &output, io.Discard)
	if code != 1 || !strings.Contains(output.String(), maintenance.CodeActive) {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	if matches, err := filepath.Glob(filepath.Join(knowledgeRoot, "sessions", "*")); err != nil || len(matches) != 0 {
		t.Fatalf("session mutation escaped admission: matches=%v error=%v", matches, err)
	}
}

func TestKnowledgeMutationFailsClosedBeforeDatabaseOpen(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	lease := beginTestMaintenance(t, controlRoot, knowledgeRoot, "CLI_KNOWLEDGE_ADMISSION_TEST")
	t.Cleanup(func() { _, _ = lease.Complete(maintenance.OutcomeAborted) })
	var output bytes.Buffer
	code := Main([]string{
		"knowledge", "create-project", "--knowledge-root", knowledgeRoot, "--alias", "project",
	}, strings.NewReader(""), &output, io.Discard)
	if code != 1 || !strings.Contains(output.String(), maintenance.CodeActive) {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	databasePath := filepath.Join(knowledgeRoot, "service", "knowledge.sqlite3")
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("knowledge database opened during maintenance: %v", err)
	}
}

func TestOrchestrationMutationFailsClosedBeforeDatabaseOpen(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	lease := beginTestMaintenance(t, controlRoot, knowledgeRoot, "CLI_ORCHESTRATION_ADMISSION_TEST")
	t.Cleanup(func() { _, _ = lease.Complete(maintenance.OutcomeAborted) })
	var output bytes.Buffer
	code := Main([]string{
		"orchestration", "start-run", "--knowledge-root", knowledgeRoot,
		"--project-id", "project", "--objective-hash", strings.Repeat("c", 64),
		"--session-key", strings.Repeat("d", 64),
	}, strings.NewReader(""), &output, io.Discard)
	if code != 1 || !strings.Contains(output.String(), maintenance.CodeActive) {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	databasePath := filepath.Join(knowledgeRoot, "service", "orchestration.sqlite3")
	if _, err := os.Stat(databasePath); !os.IsNotExist(err) {
		t.Fatalf("orchestration database opened during maintenance: %v", err)
	}
}

func TestSessionStartHookDefersWithoutMutationDuringMaintenance(t *testing.T) {
	knowledgeRoot := t.TempDir()
	controlRoot := t.TempDir()
	projectRoot := t.TempDir()
	withTestMaintenanceControlRoot(t, controlRoot)
	t.Setenv("YTQJK_KNOWLEDGE_ROOT", knowledgeRoot)
	lease := beginTestMaintenance(t, controlRoot, knowledgeRoot, "CLI_HOOK_ADMISSION_TEST")
	t.Cleanup(func() { _, _ = lease.Complete(maintenance.OutcomeAborted) })
	payload, err := json.Marshal(map[string]string{"session_id": "session", "cwd": projectRoot})
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	code := Main([]string{"hook", "session-start"}, bytes.NewReader(payload), &output, io.Discard)
	if code != 0 || !strings.Contains(output.String(), "maintenance admission unavailable") {
		t.Fatalf("exit=%d output=%q", code, output.String())
	}
	if matches, err := filepath.Glob(filepath.Join(knowledgeRoot, "sessions", "*")); err != nil || len(matches) != 0 {
		t.Fatalf("hook mutation escaped admission: matches=%v error=%v", matches, err)
	}
}

func beginTestMaintenance(t *testing.T, controlRoot, knowledgeRoot, purpose string) *maintenance.Lease {
	t.Helper()
	if err := maintenance.BootstrapControlRoot(context.Background(), controlRoot); err != nil {
		t.Fatal(err)
	}
	lease, err := maintenance.BeginExclusive(context.Background(), maintenance.Scope{
		ControlRoot: controlRoot, KnowledgeRoot: knowledgeRoot,
	}, maintenance.ExclusiveOptions{
		OperationID: strings.Repeat("b", 64), Purpose: purpose,
	})
	if err != nil {
		t.Fatal(err)
	}
	return lease
}

func withTestMaintenanceControlRoot(t *testing.T, root string) {
	t.Helper()
	previous := maintenanceControlRoot
	maintenanceControlRoot = func() (string, error) { return root, nil }
	t.Cleanup(func() { maintenanceControlRoot = previous })
}

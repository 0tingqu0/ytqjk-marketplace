package orchestration

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoffMutationBindingAndAudit(t *testing.T) {
	root := t.TempDir()
	ledger, _, err := Open(filepath.Join(root, "ledger.sqlite3"), filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	session := strings.Repeat("9", 64)
	projectID := "project-id"
	run, err := ledger.StartRun(projectID, strings.Repeat("8", 64), session, session)
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"internal/cli/handoff.go"}
	if err := ledger.Grant(Grant{
		RunID: run.RunID, SessionKey: session, Role: "git", Mutation: true,
		ReadScope: paths, WriteScope: paths,
	}, session); err != nil {
		t.Fatal(err)
	}
	bundleHash := strings.Repeat("7", 64)
	token, err := ledger.AttestHandoffApply(run.RunID, session, "git", paths, paths, bundleHash, 60)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.ExecuteMutation(token, session, func() error { return nil }); err == nil {
		t.Fatal("bound token executed through the generic mutation API")
	}
	calls := 0
	if err := ledger.ExecuteHandoffApply(token, session, projectID, bundleHash, paths, func() error {
		calls++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.ExecuteHandoffApply(token, session, projectID, bundleHash, paths, func() error {
		calls++
		return nil
	}); err == nil || calls != 1 {
		t.Fatalf("replay error = %v, calls = %d", err, calls)
	}
	for _, kind := range []string{"mutation_started", "mutation_completed"} {
		var raw string
		if err := ledger.database.QueryRow(
			"SELECT detail FROM audit_events WHERE lease_id=? AND kind=?", token.LeaseID, kind,
		).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var detail map[string]any
		if err := json.Unmarshal([]byte(raw), &detail); err != nil {
			t.Fatal(err)
		}
		if detail["capability"] != HandoffApplyCapability || detail["operation"] != HandoffApplyOperation || detail["resource"] != projectID {
			t.Fatalf("%s detail = %#v", kind, detail)
		}
	}
}

package orchestration

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGrantAndAttestation(t *testing.T) {
	root := t.TempDir()
	ledger, databaseID, err := Open(filepath.Join(root, "ledger.sqlite3"), filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if databaseID == "" {
		t.Fatal("database id is empty")
	}
	session := strings.Repeat("b", 64)
	run, err := ledger.StartRun("project-id", strings.Repeat("a", 64), session, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Grant(Grant{RunID: run.RunID, SessionKey: session, Role: "director", Capabilities: []string{"run:lifecycle"}}, session); err != nil {
		t.Fatal(err)
	}
	grant := Grant{RunID: run.RunID, SessionKey: session, Role: "worker", ReadScope: []string{"internal"}, WriteScope: []string{"internal/knowledge"}, Mutation: true}
	if err := ledger.Grant(grant, session); err != nil {
		t.Fatal(err)
	}
	token, err := ledger.Attest(run.RunID, session, "worker", []string{"internal"}, []string{"internal/knowledge"}, true, strings.Repeat("c", 64), 60)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.ExecuteMutation(token, session, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Transition(run.RunID, session, "DONE", 0); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Verify(token, session); err == nil {
		t.Fatal("terminal transition did not revoke the active lease")
	}
}

func TestOpensLegacyPythonLedgerSchema(t *testing.T) {
	root := t.TempDir()
	databasePath := filepath.Join(root, "legacy.sqlite3")
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(databasePath))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range orchestrationSchema[:6] {
		if _, err := database.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	session := strings.Repeat("1", 64)
	objective := strings.Repeat("2", 64)
	if _, err := database.Exec("INSERT INTO metadata(key,value) VALUES ('database_id','legacy-database')"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO runs(run_id,session_key,project_id,objective_hash,database_id,created_at) VALUES ('legacy-run',?,?,?,'legacy-database',1)", session, "project", objective); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO run_events(run_id,version,state,created_at) VALUES ('legacy-run',0,'RUNNING',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO role_ledger(run_id,session_key,role,read_scope,write_scope,mutation,capabilities,created_at)
VALUES ('legacy-run',?,'director','[]','[]',0,'["run:lifecycle"]',1)`, session); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "identity.key")
	if err := os.WriteFile(keyPath, []byte(strings.Repeat("k", 32)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := secureKeyPermissions(keyPath, true); err != nil {
		t.Fatal(err)
	}
	ledger, databaseID, err := Open(databasePath, keyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	if databaseID != "legacy-database" {
		t.Fatalf("database id = %q", databaseID)
	}
	run, err := ledger.Run("legacy-run")
	if err != nil || run.Version != 0 || run.State != "RUNNING" {
		t.Fatalf("legacy run = %#v, %v", run, err)
	}
	if _, err := ledger.Transition("legacy-run", session, "DONE", 0); err != nil {
		t.Fatal(err)
	}
}

func TestDirectorAndSensitiveScopeGuards(t *testing.T) {
	root := t.TempDir()
	ledger, _, err := Open(filepath.Join(root, "ledger.sqlite3"), filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer ledger.Close()
	session := strings.Repeat("d", 64)
	run, err := ledger.StartRun("project-id", strings.Repeat("e", 64), session, session)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Transition(run.RunID, session, "DONE", 0); err == nil {
		t.Fatal("lifecycle transition without coordinator grant succeeded")
	}
	if err := ledger.Grant(Grant{RunID: run.RunID, SessionKey: session, Role: "director", WriteScope: []string{"src"}, Mutation: true}, session); err == nil {
		t.Fatal("director received task mutation power")
	}
	if err := ledger.Grant(Grant{RunID: run.RunID, SessionKey: session, Role: "worker", WriteScope: []string{"src"}, Mutation: true, Capabilities: []string{"code:write"}}, session); err == nil {
		t.Fatal("unknown capability was accepted")
	}
	if err := ledger.Grant(Grant{RunID: run.RunID, SessionKey: session, Role: "worker", WriteScope: []string{"src"}, Mutation: true}, session); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Attest(run.RunID, session, "worker", nil, []string{"src"}, true, "", 60); err == nil {
		t.Fatal("mutation attestation without staged hash succeeded")
	}
	if err := ledger.Grant(Grant{RunID: run.RunID, SessionKey: session, Role: "reviewer", ReadScope: []string{"src"}}, session); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.Attest(run.RunID, session, "reviewer", []string{"src"}, nil, false, strings.Repeat("f", 64), 60); err == nil {
		t.Fatal("read-only attestation accepted a staged hash")
	}
	for _, path := range []string{".cargo/config", ".m2/settings.xml", ".my.cnf", ".pgpass", "_netrc", "settings-security.xml", "client.ovpn", "state.tfstate.backup", "node_modules/package/index.js", "credentials.toml", "service_account.json", "token.yaml"} {
		if _, err := canonicalScope([]string{path}); err == nil {
			t.Fatalf("sensitive scope %q was accepted", path)
		}
	}
	for _, path := range []string{"src/auth.go", "src/token_parser.go", "config/app.yaml", "config/settings.xml", "go.mod", "tests/fixtures/sample.json"} {
		if _, err := canonicalScope([]string{path}); err != nil {
			t.Fatalf("normal scope %q was rejected: %v", path, err)
		}
	}
	if _, err := ledger.database.Exec(`INSERT INTO role_ledger(run_id,session_key,role,read_scope,write_scope,mutation,capabilities,created_at)
VALUES (?,?,'controller','[]','["src"]',1,'[]',0)`, run.RunID, session); err == nil {
		t.Fatal("database accepted a privileged controller row")
	}
}

func TestPythonAttestationSignatureCompatibility(t *testing.T) {
	ledger := &Ledger{key: []byte(strings.Repeat("k", 32))}
	token := Attestation{
		DatabaseID: "db", ExpiresAt: 1234567890, LeaseID: "lease", Mutation: true,
		ObjectiveHash: strings.Repeat("a", 64), ProjectID: "project", ReadScope: []string{"src/read.go"},
		Role: "worker", RunID: "run", SessionKey: strings.Repeat("b", 64),
		StagedHash: strings.Repeat("c", 64), WriteScope: []string{"src/write.go"},
	}
	const expected = "769ab737fe9bede9fefffd9027f6ac3dc3e2a0da74d1dcf38d880b9611e43b6d"
	if signature := ledger.sign(token); signature != expected {
		t.Fatalf("signature = %s, want Python-compatible %s", signature, expected)
	}
}

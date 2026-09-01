package orchestration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMutationAttestationExecutesAndConsumesOnce(t *testing.T) {
	ledger, session, token := newTestAttestation(t, true)
	operations := 0

	if err := ledger.Verify(token, session); err == nil || err.Error() != mutationExecutionMessage {
		t.Fatalf("Verify() error = %v, want %q", err, mutationExecutionMessage)
	}
	if err := ledger.ExecuteMutation(token, session, func() error {
		operations++
		return nil
	}); err != nil {
		t.Fatalf("first ExecuteMutation() error = %v", err)
	}
	if err := ledger.ExecuteMutation(token, session, func() error {
		operations++
		return nil
	}); err == nil || err.Error() != inactiveLeaseMessage {
		t.Fatalf("second ExecuteMutation() error = %v, want %q", err, inactiveLeaseMessage)
	}
	if operations != 1 {
		t.Fatalf("operation count = %d, want 1", operations)
	}

	var state string
	if err := ledger.database.QueryRow(
		"SELECT state FROM lease_events WHERE lease_id=? ORDER BY version DESC LIMIT 1",
		token.LeaseID,
	).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "CONSUMED" {
		t.Fatalf("lease state = %q, want CONSUMED", state)
	}
	var auditCount int
	if err := ledger.database.QueryRow(
		"SELECT COUNT(*) FROM audit_events WHERE kind IN ('mutation_started','mutation_completed') AND lease_id=?",
		token.LeaseID,
	).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 2 {
		t.Fatalf("mutation lifecycle audit count = %d, want 2", auditCount)
	}
}

func TestMutationAttestationRejectsExpiredAndRevokedLeases(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		ledger, session, token := newTestAttestation(t, true)
		token.ExpiresAt = time.Now().Unix() - 1
		token.Signature = ledger.sign(token)

		err := ledger.ExecuteMutation(token, session, func() error { return nil })
		if err == nil || err.Error() != "attestation is expired or session-bound" {
			t.Fatalf("Verify() error = %v, want expired error", err)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		ledger, session, token := newTestAttestation(t, true)
		if _, err := ledger.database.Exec(`INSERT INTO lease_events(
lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
SELECT lease_id,run_id,session_key,role,version+1,'REVOKED',expires_at,binding_hash,?
FROM lease_events WHERE lease_id=? AND version=0`, time.Now().Unix(), token.LeaseID); err != nil {
			t.Fatal(err)
		}

		err := ledger.ExecuteMutation(token, session, func() error { return nil })
		if err == nil || err.Error() != inactiveLeaseMessage {
			t.Fatalf("Verify() error = %v, want %q", err, inactiveLeaseMessage)
		}
	})
}

func TestMutationAttestationConcurrentExecutionHasOneWinner(t *testing.T) {
	ledger, session, token := newTestAttestation(t, true)
	const attempts = 16
	start := make(chan struct{})
	results := make(chan error, attempts)
	for range attempts {
		go func() {
			<-start
			results <- ledger.ExecuteMutation(token, session, func() error { return nil })
		}()
	}
	close(start)

	successes := 0
	for range attempts {
		err := <-results
		if err == nil {
			successes++
			continue
		}
		if err.Error() != inactiveLeaseMessage {
			t.Fatalf("ExecuteMutation() error = %v, want %q", err, inactiveLeaseMessage)
		}
	}
	if successes != 1 {
		t.Fatalf("successful Verify() calls = %d, want 1", successes)
	}
}

func TestReadOnlyAttestationRemainsReusable(t *testing.T) {
	ledger, session, token := newTestAttestation(t, false)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ledger.Verify(token, session); err != nil {
			t.Fatalf("Verify() attempt %d error = %v", attempt+1, err)
		}
	}
}

func TestInsertActiveLeaseRejectsStoppedRun(t *testing.T) {
	ledger, _, token := newTestAttestation(t, true)
	if _, err := ledger.database.Exec(`INSERT INTO lease_events(
lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
SELECT lease_id,run_id,session_key,role,version+1,'REVOKED',expires_at,binding_hash,?
FROM lease_events WHERE lease_id=? AND version=0`, time.Now().Unix(), token.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.database.Exec(
		"INSERT INTO run_events(run_id,version,state,created_at) VALUES (?,1,'STOPPED',?)",
		token.RunID,
		time.Now().Unix(),
	); err != nil {
		t.Fatal(err)
	}
	token.LeaseID = strings.Repeat("6", 32)
	claims, err := json.Marshal(tokenClaims(token))
	if err != nil {
		t.Fatal(err)
	}
	binding := sha256.Sum256(claims)
	tx, err := ledger.database.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	err = insertActiveLease(tx, token, hex.EncodeToString(binding[:]), time.Now().Unix())
	if err == nil || err.Error() != "run identity mismatch" {
		t.Fatalf("insertActiveLease() error = %v, want run identity mismatch", err)
	}
	var count int
	if err := tx.QueryRow("SELECT COUNT(*) FROM lease_events WHERE lease_id=?", token.LeaseID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stopped run lease count = %d, want 0", count)
	}
}

func newTestAttestation(t *testing.T, mutation bool) (*Ledger, string, Attestation) {
	t.Helper()
	root := t.TempDir()
	ledger, _, err := Open(filepath.Join(root, "ledger.sqlite3"), filepath.Join(root, "identity.key"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ledger.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	session := strings.Repeat("9", 64)
	run, err := ledger.StartRun("project-id", strings.Repeat("8", 64), session, session)
	if err != nil {
		t.Fatal(err)
	}
	grant := Grant{RunID: run.RunID, SessionKey: session, Role: "worker", ReadScope: []string{"internal"}, Mutation: mutation}
	var writes []string
	var stagedHash string
	if mutation {
		writes = []string{"internal/orchestration"}
		grant.WriteScope = writes
		stagedHash = strings.Repeat("7", 64)
	}
	if err := ledger.Grant(grant, session); err != nil {
		t.Fatal(err)
	}
	token, err := ledger.Attest(run.RunID, session, "worker", grant.ReadScope, writes, mutation, stagedHash, 60)
	if err != nil {
		t.Fatal(err)
	}
	return ledger, session, token
}

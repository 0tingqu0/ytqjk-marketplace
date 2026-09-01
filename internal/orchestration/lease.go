package orchestration

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	inactiveLeaseMessage     = "attestation lease is not active"
	mutationExecutionMessage = "mutation attestation requires integrated execution"
)

// Attest issues a lease only while the bound run is still RUNNING in the same
// transaction. This closes the check/issue race with lifecycle transitions.
func (l *Ledger) Attest(runID, currentSessionKey, role string, reads, writes []string, mutation bool, stagedHash string, leaseSeconds int) (Attestation, error) {
	return l.attest(runID, currentSessionKey, role, reads, writes, mutation, stagedHash, leaseSeconds, "", "")
}

// AttestHandoffApply issues a mutation lease that can only be consumed by the
// integrated handoff apply path. The git role is the immutable capability
// grant; capability and operation are repeated in the signed token claims.
func (l *Ledger) AttestHandoffApply(runID, currentSessionKey, role string, reads, writes []string, stagedHash string, leaseSeconds int) (Attestation, error) {
	if role != "git" {
		return Attestation{}, errors.New("handoff apply requires the git role")
	}
	return l.attest(runID, currentSessionKey, role, reads, writes, true, stagedHash, leaseSeconds, HandoffApplyCapability, HandoffApplyOperation)
}

func (l *Ledger) attest(runID, currentSessionKey, role string, reads, writes []string, mutation bool, stagedHash string, leaseSeconds int, capability, operation string) (Attestation, error) {
	run, err := l.Run(runID)
	if err != nil {
		return Attestation{}, err
	}
	if run.State != "RUNNING" || !hmac.Equal([]byte(run.SessionKey), []byte(currentSessionKey)) {
		return Attestation{}, errors.New("run identity mismatch")
	}
	if leaseSeconds < 1 || leaseSeconds > DefaultLeaseSeconds {
		return Attestation{}, errors.New("invalid lease")
	}
	if mutation && !hashPattern.MatchString(stagedHash) {
		return Attestation{}, errors.New("mutation attestation requires a staged hash")
	}
	if !mutation && stagedHash != "" {
		return Attestation{}, errors.New("read-only attestation cannot bind a staged hash")
	}
	reads, err = canonicalScope(reads)
	if err != nil {
		return Attestation{}, err
	}
	writes, err = canonicalScope(writes)
	if err != nil {
		return Attestation{}, err
	}
	var rawReads, rawWrites, rawCapabilities string
	var grantedMutation bool
	if err := l.database.QueryRow("SELECT read_scope,write_scope,mutation,capabilities FROM role_ledger WHERE run_id=? AND role=?", runID, role).Scan(&rawReads, &rawWrites, &grantedMutation, &rawCapabilities); err != nil {
		return Attestation{}, errors.New("role ledger does not allow attestation")
	}
	var grantReads, grantWrites, grantCapabilities []string
	if json.Unmarshal([]byte(rawReads), &grantReads) != nil || json.Unmarshal([]byte(rawWrites), &grantWrites) != nil ||
		json.Unmarshal([]byte(rawCapabilities), &grantCapabilities) != nil ||
		directorGrantInvalid(role, grantReads, grantWrites, grantedMutation, grantCapabilities) ||
		grantedMutation != mutation || !subset(reads, grantReads) || !subset(writes, grantWrites) {
		return Attestation{}, errors.New("scope exceeds role ledger")
	}
	leaseID, err := randomHex(16)
	if err != nil {
		return Attestation{}, err
	}
	token := Attestation{
		RunID: runID, SessionKey: run.SessionKey, ProjectID: run.ProjectID, ObjectiveHash: run.ObjectiveHash,
		Role: role, Capability: capability, Operation: operation,
		ReadScope: reads, WriteScope: writes, Mutation: mutation, StagedHash: stagedHash,
		DatabaseID: run.DatabaseID, LeaseID: leaseID, ExpiresAt: time.Now().Unix() + int64(leaseSeconds),
	}
	token.Signature = l.sign(token)
	claims, _ := json.Marshal(tokenClaims(token))
	binding := sha256.Sum256(claims)
	tx, err := l.database.Begin()
	if err != nil {
		return Attestation{}, err
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	if err := insertActiveLease(tx, token, hex.EncodeToString(binding[:]), now); err != nil {
		return Attestation{}, err
	}
	if err := appendAudit(tx, "lease_issued", runID, run.SessionKey, leaseID, map[string]any{"role": role, "mutation": mutation}, now); err != nil {
		return Attestation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Attestation{}, err
	}
	return token, nil
}

func insertActiveLease(tx *sql.Tx, token Attestation, bindingHash string, now int64) error {
	result, err := tx.Exec(`INSERT INTO lease_events(
lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
SELECT ?,run.run_id,run.session_key,?,0,'ACTIVE',?,?,?
FROM runs run JOIN run_events event ON event.run_id=run.run_id
WHERE run.run_id=? AND run.session_key=? AND run.project_id=?
AND run.objective_hash=? AND run.database_id=? AND event.state='RUNNING'
AND event.version=(SELECT MAX(latest.version) FROM run_events latest WHERE latest.run_id=run.run_id)`,
		token.LeaseID, token.Role, token.ExpiresAt, bindingHash, now,
		token.RunID, token.SessionKey, token.ProjectID, token.ObjectiveHash, token.DatabaseID)
	if err != nil {
		return err
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("run identity mismatch")
	}
	return nil
}

// Verify validates a reusable read-only attestation. Mutation attestations
// must be bound to their side effect through ExecuteMutation.
func (l *Ledger) Verify(token Attestation, currentSessionKey string) error {
	bindingHash, err := l.validateAttestation(token, currentSessionKey)
	if err != nil {
		return err
	}
	if token.Mutation {
		return errors.New(mutationExecutionMessage)
	}
	return l.verifyActiveLease(token, bindingHash)
}

func (l *Ledger) validateAttestation(token Attestation, currentSessionKey string) (string, error) {
	if token.ExpiresAt <= time.Now().Unix() || !hmac.Equal([]byte(token.SessionKey), []byte(currentSessionKey)) {
		return "", errors.New("attestation is expired or session-bound")
	}
	expected := l.sign(token)
	if !hmac.Equal([]byte(expected), []byte(token.Signature)) {
		return "", errors.New("attestation signature is invalid")
	}
	var databaseID string
	if err := l.database.QueryRow("SELECT value FROM metadata WHERE key='database_id'").Scan(&databaseID); err != nil || databaseID != token.DatabaseID {
		return "", errors.New("attestation database binding is invalid")
	}
	run, err := l.Run(token.RunID)
	if err != nil || run.State != "RUNNING" || run.ProjectID != token.ProjectID || run.ObjectiveHash != token.ObjectiveHash || run.SessionKey != token.SessionKey || run.DatabaseID != token.DatabaseID {
		return "", errors.New("attestation run binding is invalid")
	}
	claims, _ := json.Marshal(tokenClaims(token))
	binding := sha256.Sum256(claims)
	return hex.EncodeToString(binding[:]), nil
}

func (l *Ledger) verifyActiveLease(token Attestation, bindingHash string) error {
	var state, runID, sessionKey, role, storedBinding string
	var expiresAt int64
	err := l.database.QueryRow(`SELECT state,run_id,session_key,role,expires_at,binding_hash FROM lease_events
WHERE lease_id=? ORDER BY version DESC LIMIT 1`, token.LeaseID).
		Scan(&state, &runID, &sessionKey, &role, &expiresAt, &storedBinding)
	if err != nil || state != "ACTIVE" || runID != token.RunID || sessionKey != token.SessionKey || role != token.Role || expiresAt != token.ExpiresAt || storedBinding != bindingHash {
		return errors.New(inactiveLeaseMessage)
	}
	return nil
}

// ExecuteMutation durably consumes the lease before invoking operation. A
// lifecycle transition that commits first prevents the callback; a consume
// that commits first admits exactly one callback and remains visible as
// in-flight until its terminal audit record is committed.
func (l *Ledger) ExecuteMutation(token Attestation, currentSessionKey string, operation func() error) error {
	if token.Capability != "" || token.Operation != "" {
		return errors.New("bound mutation requires its integrated operation")
	}
	return l.executeMutation(token, currentSessionKey, operation)
}

func (l *Ledger) executeMutation(token Attestation, currentSessionKey string, operation func() error) error {
	if operation == nil {
		return errors.New("mutation operation is required")
	}
	bindingHash, err := l.validateAttestation(token, currentSessionKey)
	if err != nil {
		return err
	}
	if !token.Mutation {
		return errors.New("read-only attestation cannot execute a mutation")
	}
	if err := l.beginMutation(token, bindingHash); err != nil {
		return err
	}
	operationErr := operation()
	resultKind := "mutation_completed"
	if operationErr != nil {
		resultKind = "mutation_failed"
	}
	if err := l.finishMutation(token, resultKind); err != nil {
		return errors.Join(operationErr, err)
	}
	return operationErr
}

func (l *Ledger) beginMutation(token Attestation, bindingHash string) error {
	tx, err := l.database.Begin()
	if err != nil {
		return errors.New(inactiveLeaseMessage)
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if token.ExpiresAt <= now {
		return errors.New("attestation is expired or session-bound")
	}
	result, err := tx.Exec(`INSERT INTO lease_events(
lease_id,run_id,session_key,role,version,state,expires_at,binding_hash,created_at)
SELECT current.lease_id,current.run_id,current.session_key,current.role,current.version+1,
'CONSUMED',current.expires_at,current.binding_hash,?
FROM lease_events current
WHERE current.lease_id=?
AND current.version=(SELECT MAX(latest.version) FROM lease_events latest WHERE latest.lease_id=current.lease_id)
AND current.state='ACTIVE' AND current.run_id=? AND current.session_key=? AND current.role=?
AND current.expires_at=? AND current.expires_at>? AND current.binding_hash=?
AND EXISTS (SELECT 1 FROM run_events event WHERE event.run_id=current.run_id
AND event.state='RUNNING' AND event.version=(SELECT MAX(latest.version) FROM run_events latest
WHERE latest.run_id=current.run_id))`,
		now, token.LeaseID, token.RunID, token.SessionKey, token.Role,
		token.ExpiresAt, now, bindingHash)
	if err != nil {
		return errors.New(inactiveLeaseMessage)
	}
	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return errors.New(inactiveLeaseMessage)
	}
	if err := appendAudit(tx, "mutation_started", token.RunID, token.SessionKey,
		token.LeaseID, mutationAuditDetail(token), now); err != nil {
		return errors.New(inactiveLeaseMessage)
	}
	if err := tx.Commit(); err != nil {
		return errors.New(inactiveLeaseMessage)
	}
	return nil
}

func (l *Ledger) finishMutation(token Attestation, resultKind string) error {
	tx, err := l.database.Begin()
	if err != nil {
		return fmt.Errorf("persist mutation outcome: %w", err)
	}
	defer tx.Rollback()
	if err := appendAudit(tx, resultKind, token.RunID, token.SessionKey,
		token.LeaseID, mutationAuditDetail(token), time.Now().Unix()); err != nil {
		return fmt.Errorf("persist mutation outcome: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("persist mutation outcome: %w", err)
	}
	return nil
}

func mutationAuditDetail(token Attestation) map[string]any {
	detail := map[string]any{"role": token.Role}
	if token.Operation != "" {
		detail["capability"] = token.Capability
		detail["operation"] = token.Operation
		detail["resource"] = token.ProjectID
		detail["staged_hash"] = token.StagedHash
	}
	return detail
}

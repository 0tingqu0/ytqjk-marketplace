package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDeadExpiredDrainingCanAbortWithoutGenerationAdvance(t *testing.T) {
	scope := newTestScope(t)
	writeStaleRecord(t, scope, staleRecord(deadOwner(), StateDraining, 4, false))
	lease, err := RecoverExclusive(context.Background(), scope, operationA, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 3 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func TestDeadExpiredPreMutationMaintenanceCanAbort(t *testing.T) {
	scope := newTestScope(t)
	writeStaleRecord(t, scope, staleRecord(deadOwner(), StateMaintenance, 4, false))
	lease, err := RecoverExclusive(context.Background(), scope, operationA, 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 4 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeAborted
	})
}

func TestDeadMutationBeforeDeadlineRequiresRestoreAndPreservesDeadline(t *testing.T) {
	scope := newTestScope(t)
	record := staleRecord(deadOwner(), StateMaintenance, 4, true)
	record.Intent.ExpiresAt = clockNow().Add(2 * time.Minute)
	expectedDeadline := record.Intent.ExpiresAt
	writeStaleRecord(t, scope, record)
	lease, err := RecoverExclusive(context.Background(), scope, operationA, 4)
	if err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateRestoring && record.Generation == 4 &&
			record.Intent.ExpiresAt.Equal(expectedDeadline)
	})
	if _, err := lease.Complete(OutcomeAborted); !IsCode(err, CodeInvalid) {
		t.Fatalf("unsafe recovery completion error = %v", err)
	}
	if _, err := lease.Complete(OutcomeRolledBack); err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateOpen && record.Generation == 4 &&
			record.Receipt != nil && record.Receipt.Outcome == OutcomeRolledBack
	})
}

func TestDeadExpiredMutationRequiresManualRecovery(t *testing.T) {
	scope := newTestScope(t)
	writeStaleRecord(t, scope, staleRecord(deadOwner(), StateMaintenance, 4, true))
	_, err := RecoverExclusive(context.Background(), scope, operationA, 4)
	assertCode(t, err, CodeRecoveryRequired)
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateRecoveryRequired && record.Generation == 4
	})
}

func TestDeadOwnerCanBeRecoveredBeforeExpiry(t *testing.T) {
	scope := newTestScope(t)
	record := staleRecord(deadOwner(), StateMaintenance, 2, false)
	record.Intent.ExpiresAt = clockNow().Add(2 * time.Minute)
	writeStaleRecord(t, scope, record)
	lease, err := RecoverExclusive(context.Background(), scope, operationA, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Complete(OutcomeAborted); err != nil {
		t.Fatal(err)
	}
}

func TestExpiredLiveOwnerCannotBeRecovered(t *testing.T) {
	scope := newTestScope(t)
	owner, err := currentOwner()
	if err != nil {
		t.Fatal(err)
	}
	writeStaleRecord(t, scope, staleRecord(owner, StateMaintenance, 2, false))
	_, err = RecoverExclusive(context.Background(), scope, operationA, 2)
	assertCode(t, err, CodeActive)
}

func TestRecoveryIdentityMustMatch(t *testing.T) {
	scope := newTestScope(t)
	writeStaleRecord(t, scope, staleRecord(deadOwner(), StateMaintenance, 3, false))
	_, err := RecoverExclusive(context.Background(), scope, operationB, 3)
	assertCode(t, err, CodeRecoveryRequired)
	_, err = RecoverExclusive(context.Background(), scope, operationA, 2)
	assertCode(t, err, CodeRecoveryRequired)
}

func TestRecoveryResourcesMustMatchScope(t *testing.T) {
	scope := newTestScope(t)
	record := staleRecord(deadOwner(), StateDraining, 3, false)
	writeStaleRecord(t, scope, record)
	otherRoot := t.TempDir()
	other := Scope{
		ControlRoot:   scope.ControlRoot,
		RuntimeRoot:   filepath.Join(otherRoot, "runtime"),
		CodexRoot:     filepath.Join(otherRoot, "codex"),
		KnowledgeRoot: filepath.Join(otherRoot, "knowledge"),
	}
	for _, path := range []string{other.RuntimeRoot, other.CodexRoot, other.KnowledgeRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_, err := RecoverExclusive(context.Background(), other, operationA, 3)
	assertCode(t, err, CodeRecoveryRequired)
}

func staleRecord(owner Owner, state State, targetGeneration uint64, mutated bool) Record {
	started := clockNow().Add(-20 * time.Minute)
	updated := started.Add(2 * time.Minute)
	expires := started.Add(10 * time.Minute)
	intent := &Intent{
		OperationID: operationA, Purpose: "UPGRADE", Resources: nil, Owner: owner,
		BaseGeneration: targetGeneration - 1, TargetGeneration: targetGeneration,
		StartedAt: started, UpdatedAt: updated, ExpiresAt: expires,
		DrainDeadline: started.Add(time.Minute),
	}
	if mutated {
		mutation := started.Add(3 * time.Minute)
		intent.MutationStarted = &mutation
	}
	generation := targetGeneration
	if state == StateDraining {
		generation = targetGeneration - 1
	}
	return Record{
		Schema: recordSchema, State: state, Generation: generation,
		Revision: 7, UpdatedAt: updated, Intent: intent,
	}
}

func deadOwner() Owner {
	return Owner{PID: 2_000_000_000, Identity: "dead:2000000000"}
}

func writeStaleRecord(t *testing.T, scope Scope, record Record) {
	t.Helper()
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record.Intent.Resources = cloneStrings(control.resources)
	if !validRecord(record) {
		t.Fatalf("test record is invalid: %#v", record)
	}
	if err := writeRecordJSON(control, record); err != nil {
		t.Fatal(err)
	}
}

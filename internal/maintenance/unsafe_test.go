package maintenance

import (
	"context"
	"testing"
)

func TestMarkRecoveryRequiredPersistsUnsafeTerminalAndReleasesLock(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	if err := lease.MarkRecoveryRequired(
		"MIGRATION_TARGET_HEALTH_UNKNOWN", "target health result is unknown",
	); err != nil {
		t.Fatal(err)
	}
	assertRecord(t, scope, func(record Record) bool {
		return record.State == StateRecoveryRequired && record.Generation == 1 &&
			record.Intent.Recovery != nil &&
			record.Intent.Recovery.Code == "MIGRATION_TARGET_HEALTH_UNKNOWN" &&
			record.Intent.Recovery.Cause == "target health result is unknown"
	})
	if _, err := AcquireShared(context.Background(), scope); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("shared admission error = %v", err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := acquirePlaneLock(
		context.Background(), control, control.writersPath, true, lockDeadline(context.Background()),
	)
	if err != nil {
		t.Fatalf("writer process lock was not released: %v", err)
	}
	if err := joinUnlock(nil, writer); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.Complete(OutcomeRolledBack); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("closed lease completion error = %v", err)
	}
}

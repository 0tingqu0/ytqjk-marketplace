package maintenance

import (
	"context"
	"testing"
	"time"
)

func TestReadyBindingRejectsEveryBoundFieldTamper(t *testing.T) {
	scope := newTestScope(t)
	canaryLease := beginClaimedCanary(t, scope)
	if err := canaryLease.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	baseline := readTestRecord(t, control)
	readyAt := func(record *Record) *time.Time { return record.Intent.Canary.ReadyAt }
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{"operation", func(r *Record) { r.Intent.OperationID = operationB }},
		{"purpose", func(r *Record) { r.Intent.Purpose = "OTHER_PURPOSE" }},
		{"base generation", func(r *Record) { r.Intent.BaseGeneration++ }},
		{"generation", func(r *Record) { r.Intent.TargetGeneration++ }},
		{"resources", func(r *Record) { r.Intent.Resources[0] += "-tamper" }},
		{"started time", func(r *Record) { r.Intent.StartedAt = r.Intent.StartedAt.Add(time.Nanosecond) }},
		{"expiry", func(r *Record) { r.Intent.ExpiresAt = r.Intent.ExpiresAt.Add(time.Nanosecond) }},
		{"drain deadline", func(r *Record) { r.Intent.DrainDeadline = r.Intent.DrainDeadline.Add(time.Nanosecond) }},
		{"mutation time", func(r *Record) {
			changed := r.Intent.MutationStarted.Add(time.Nanosecond)
			r.Intent.MutationStarted = &changed
		}},
		{"record revision", func(r *Record) { r.Revision++ }},
		{"record updated", func(r *Record) { r.UpdatedAt = r.UpdatedAt.Add(time.Nanosecond) }},
		{"intent updated", func(r *Record) { r.Intent.UpdatedAt = r.Intent.UpdatedAt.Add(time.Nanosecond) }},
		{"transfer phase", func(r *Record) { r.Intent.TransferPending = !r.Intent.TransferPending }},
		{"capability", func(r *Record) { r.Intent.Canary.CapabilitySHA256 = testHash("0") }},
		{"base binding", func(r *Record) { r.Intent.Canary.BaseBindingSHA256 = testHash("0") }},
		{"ready binding", func(r *Record) { r.Intent.Canary.ReadyBindingSHA256 = testHash("0") }},
		{"active binding", func(r *Record) { r.Intent.Canary.ActiveBindingSHA256 = testHash("0") }},
		{"plan", func(r *Record) { r.Intent.Canary.PlanSHA256 = testHash("4") }},
		{"snapshot", func(r *Record) { r.Intent.Canary.SnapshotManifestSHA256 = testHash("5") }},
		{"binary", func(r *Record) { r.Intent.Canary.TargetBinarySHA256 = testHash("6") }},
		{"version", func(r *Record) { r.Intent.Canary.TargetVersion = "v0.7.1" }},
		{"port", func(r *Record) { r.Intent.Canary.Port++ }},
		{"owner", func(r *Record) { r.Intent.Canary.Owner.Identity += "-tamper" }},
		{"attempt", func(r *Record) { r.Intent.Canary.Attempt++ }},
		{"outcome", func(r *Record) { r.Intent.Canary.ExpectedOutcome = OutcomeFailedSafe }},
		{"fallback outcome", func(r *Record) { r.Intent.Canary.FallbackOutcome = OutcomeRolledBack }},
		{"deadline", func(r *Record) { r.Intent.Canary.Deadline = r.Intent.Canary.Deadline.Add(time.Second) }},
		{"ready receipt", func(r *Record) { r.Intent.Canary.ReadyReceiptSHA256 = testHash("f") }},
		{"ready time", func(r *Record) { changed := readyAt(r).Add(time.Nanosecond); r.Intent.Canary.ReadyAt = &changed }},
	}
	assertCanaryTamperCorrupt(t, control, baseline, tests)
}

func TestOpenReceiptBindingRejectsEveryBoundFieldTamper(t *testing.T) {
	scope := newTestScope(t)
	canaryLease := beginClaimedCanary(t, scope)
	if err := canaryLease.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	if _, err := canaryLease.Complete(OutcomeSucceeded, testHash("e")); err != nil {
		t.Fatal(err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	baseline := readTestRecord(t, control)
	tests := []struct {
		name   string
		mutate func(*Record)
	}{
		{"record revision", func(r *Record) { r.Revision++ }},
		{"record updated", func(r *Record) { r.UpdatedAt = r.UpdatedAt.Add(time.Nanosecond) }},
		{"operation", func(r *Record) { r.Receipt.OperationID = operationB }},
		{"generation", func(r *Record) { r.Receipt.Generation++ }},
		{"resources", func(r *Record) { r.Receipt.Resources[0] += "-tamper" }},
		{"outcome", func(r *Record) { r.Receipt.Outcome = OutcomeFailedSafe }},
		{"finished time", func(r *Record) { r.Receipt.FinishedAt = r.Receipt.FinishedAt.Add(time.Nanosecond) }},
		{"owner", func(r *Record) { r.Receipt.Canary.Owner.Identity += "-tamper" }},
		{"purpose", func(r *Record) { r.Receipt.Canary.Purpose = "OTHER_PURPOSE" }},
		{"base generation", func(r *Record) { r.Receipt.Canary.BaseGeneration++ }},
		{"started time", func(r *Record) { r.Receipt.Canary.StartedAt = r.Receipt.Canary.StartedAt.Add(time.Nanosecond) }},
		{"expiry", func(r *Record) { r.Receipt.Canary.ExpiresAt = r.Receipt.Canary.ExpiresAt.Add(time.Nanosecond) }},
		{"drain deadline", func(r *Record) { r.Receipt.Canary.DrainDeadline = r.Receipt.Canary.DrainDeadline.Add(time.Nanosecond) }},
		{"mutation time", func(r *Record) {
			r.Receipt.Canary.MutationStarted = r.Receipt.Canary.MutationStarted.Add(time.Nanosecond)
		}},
		{"capability", func(r *Record) { r.Receipt.Canary.CapabilitySHA256 = testHash("0") }},
		{"base binding", func(r *Record) { r.Receipt.Canary.BaseBindingSHA256 = testHash("0") }},
		{"ready binding", func(r *Record) { r.Receipt.Canary.ReadyBindingSHA256 = testHash("0") }},
		{"active binding", func(r *Record) { r.Receipt.Canary.ActiveBindingSHA256 = testHash("0") }},
		{"receipt binding", func(r *Record) { r.Receipt.Canary.ReceiptBindingSHA256 = testHash("0") }},
		{"plan", func(r *Record) { r.Receipt.Canary.PlanSHA256 = testHash("4") }},
		{"snapshot", func(r *Record) { r.Receipt.Canary.SnapshotManifestSHA256 = testHash("5") }},
		{"binary", func(r *Record) { r.Receipt.Canary.TargetBinarySHA256 = testHash("6") }},
		{"version", func(r *Record) { r.Receipt.Canary.TargetVersion = "v0.7.1" }},
		{"port", func(r *Record) { r.Receipt.Canary.Port++ }},
		{"attempt", func(r *Record) { r.Receipt.Canary.Attempt++ }},
		{"expected outcome", func(r *Record) { r.Receipt.Canary.ExpectedOutcome = OutcomeFailedSafe }},
		{"fallback outcome", func(r *Record) { r.Receipt.Canary.FallbackOutcome = OutcomeRolledBack }},
		{"deadline", func(r *Record) { r.Receipt.Canary.Deadline = r.Receipt.Canary.Deadline.Add(time.Second) }},
		{"ready receipt", func(r *Record) { r.Receipt.Canary.ReadyReceiptSHA256 = testHash("f") }},
		{"ready time", func(r *Record) { r.Receipt.Canary.ReadyAt = r.Receipt.Canary.ReadyAt.Add(time.Nanosecond) }},
		{"final state", func(r *Record) { r.Receipt.Canary.FinalStateSHA256 = testHash("f") }},
	}
	assertCanaryTamperCorrupt(t, control, baseline, tests)
	permit, err := AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
}

func assertCanaryTamperCorrupt(
	t *testing.T,
	control controlPlane,
	baseline Record,
	tests []struct {
		name   string
		mutate func(*Record)
	},
) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := cloneRecord(baseline)
			test.mutate(&changed)
			if err := writeRecordJSON(control, changed); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readRecord(control); !IsCode(err, CodeStateCorrupt) {
				t.Fatalf("tamper error = %v", err)
			}
			if err := writeRecordJSON(control, baseline); err != nil {
				t.Fatal(err)
			}
		})
	}
}

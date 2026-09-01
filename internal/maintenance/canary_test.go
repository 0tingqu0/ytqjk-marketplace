package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var testCanaryCapability = []byte("0123456789abcdef0123456789abcdef")

func TestCanaryBindingCoversAuthenticatedInputs(t *testing.T) {
	scope := newTestScope(t)
	lease, _ := beginPendingCanary(t, scope)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record := readTestRecord(t, control)
	original := record.Intent.Canary.BaseBindingSHA256
	tests := []struct {
		name   string
		mutate func(*controlPlane, *Intent, *Canary)
	}{
		{"operation", func(_ *controlPlane, intent *Intent, _ *Canary) { intent.OperationID = operationB }},
		{"generation", func(_ *controlPlane, intent *Intent, _ *Canary) { intent.TargetGeneration++ }},
		{"resources", func(_ *controlPlane, intent *Intent, _ *Canary) { intent.Resources[0] += "-other" }},
		{"directory", func(control *controlPlane, _ *Intent, _ *Canary) { control.directoryID += "-other" }},
		{"capability", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.CapabilitySHA256 = testHash("0") }},
		{"plan", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.PlanSHA256 = testHash("4") }},
		{"snapshot", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.SnapshotManifestSHA256 = testHash("5") }},
		{"binary", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.TargetBinarySHA256 = testHash("6") }},
		{"version", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.TargetVersion = "v0.7.1" }},
		{"port", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.Port++ }},
		{"owner", func(_ *controlPlane, intent *Intent, canary *Canary) {
			intent.Owner.Identity += "-other"
			canary.Owner = intent.Owner
		}},
		{"attempt", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.Attempt++ }},
		{"outcome", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.ExpectedOutcome = OutcomeFailedSafe }},
		{"fallback outcome", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.FallbackOutcome = OutcomeRolledBack }},
		{"deadline", func(_ *controlPlane, _ *Intent, canary *Canary) { canary.Deadline = canary.Deadline.Add(time.Second) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changedControl := control
			changedIntent := *record.Intent
			changedIntent.Resources = cloneStrings(record.Intent.Resources)
			changedCanary := *record.Intent.Canary
			changedIntent.Canary = &changedCanary
			test.mutate(&changedControl, &changedIntent, &changedCanary)
			observed, err := canaryBaseBindingSHA256(changedControl, &changedIntent, &changedCanary)
			if err != nil {
				t.Fatal(err)
			}
			if observed == original {
				t.Fatalf("binding did not cover %s", test.name)
			}
		})
	}
	if _, err := lease.Complete(OutcomeSucceeded); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("source lease bypassed REOPENING: %v", err)
	}
}

func TestCanaryClaimRejectsWrongCapabilityGenerationScopeAndBinding(t *testing.T) {
	scope := newTestScope(t)
	_, _ = beginPendingCanary(t, scope)
	wrong := append([]byte(nil), testCanaryCapability...)
	wrong[0] ^= 0xff
	if _, err := ClaimCanary(context.Background(), scope, operationA, 1, wrong); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("wrong capability error = %v", err)
	}
	if _, err := ClaimCanary(context.Background(), scope, operationA, 2, testCanaryCapability); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("wrong generation error = %v", err)
	}
	other := Scope{ControlRoot: scope.ControlRoot, RuntimeRoot: t.TempDir()}
	if _, err := ClaimCanary(context.Background(), other, operationA, 1, testCanaryCapability); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("wrong scope error = %v", err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record := readTestRecord(t, control)
	record.Intent.Canary.Port++
	if err := writeRecordJSON(control, record); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimCanary(context.Background(), scope, operationA, 1, testCanaryCapability); !IsCode(err, CodeStateCorrupt) {
		t.Fatalf("tampered binding error = %v", err)
	}
}

func TestCanaryClaimRejectsDifferentProcessIdentity(t *testing.T) {
	scope := newTestScope(t)
	_, _ = beginPendingCanary(t, scope)
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record := readTestRecord(t, control)
	record.Intent.Owner = deadOwner()
	record.Intent.Canary.Owner = record.Intent.Owner
	record.Intent.Canary.BaseBindingSHA256, err = canaryBaseBindingSHA256(control, record.Intent, record.Intent.Canary)
	if err != nil {
		t.Fatal(err)
	}
	if err := refreshCanaryActiveBinding(&record); err != nil {
		t.Fatal(err)
	}
	if err := writeRecordJSON(control, record); err != nil {
		t.Fatal(err)
	}
	if _, err := ClaimCanary(context.Background(), scope, operationA, 1, testCanaryCapability); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("wrong process error = %v", err)
	}
}

func TestCanaryRequiresReadyBeforeOpenAndCannotBeReused(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	if _, err := canary.Complete(OutcomeSucceeded, testHash("e")); !IsCode(err, CodeInvalid) {
		t.Fatalf("completion before ready error = %v", err)
	}
	copyGuard := &CanaryLease{self: canary}
	if err := copyGuard.MarkReady(testHash("d")); !IsCode(err, CodeInvalid) {
		t.Fatalf("copied lease error = %v", err)
	}
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	if err := canary.MarkReady(testHash("d")); !IsCode(err, CodeInvalid) {
		t.Fatalf("reused readiness error = %v", err)
	}
	receipt, err := canary.Complete(OutcomeSucceeded, testHash("e"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Canary == nil || receipt.Canary.ReadyReceiptSHA256 != testHash("d") ||
		receipt.Canary.FinalStateSHA256 != testHash("e") {
		t.Fatalf("canary receipt = %#v", receipt)
	}
	if _, err := canary.Complete(OutcomeSucceeded, testHash("e")); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("closed lease error = %v", err)
	}
}

func TestCanaryPersistsOpenBeforeWriterUnlock(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	original := writeRecordJSON
	observedLockedOpen := false
	writeRecordJSON = func(control controlPlane, value any) error {
		if err := original(control, value); err != nil {
			return err
		}
		record, ok := value.(Record)
		if !ok || record.State != StateOpen {
			return nil
		}
		unexpected, lockErr := acquirePlaneLock(
			context.Background(), control, control.writersPath, true, clockNow(),
		)
		observedLockedOpen = IsCode(lockErr, CodeActive)
		if lockErr == nil {
			_ = joinUnlock(nil, unexpected)
		}
		return nil
	}
	t.Cleanup(func() { writeRecordJSON = original })
	if _, err := canary.Complete(OutcomeSucceeded, testHash("e")); err != nil {
		t.Fatal(err)
	}
	writeRecordJSON = original
	if !observedLockedOpen {
		t.Fatal("OPEN was not observed while the writer lock remained held")
	}
}

func TestCanaryContextIsProcessLocalScopedAndTerminal(t *testing.T) {
	scope := newTestScope(t)
	extra := t.TempDir()
	scope.ExtraRoots = []string{extra}
	canary := beginClaimedCanary(t, scope)
	ctx, err := WithCanary(context.Background(), canary)
	if err != nil {
		t.Fatal(err)
	}
	fence, err := CanaryFenceFromContext(ctx, Scope{ControlRoot: scope.ControlRoot, RuntimeRoot: extra})
	if err != nil {
		t.Fatal(err)
	}
	if len(fence.Resources) != 1 || fence.Resources[0] != canonicalKey(extra) ||
		fence.BaseBindingSHA256 == "" || fence.ActiveBindingSHA256 == "" {
		t.Fatalf("canary fence = %#v", fence)
	}
	forged := context.WithValue(context.Background(), canaryContextKey{}, &canaryToken{})
	if _, err := CanaryFenceFromContext(forged, scope); !IsCode(err, CodeInvalid) {
		t.Fatalf("forged context error = %v", err)
	}
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	if _, err := canary.Complete(OutcomeSucceeded, testHash("e")); err != nil {
		t.Fatal(err)
	}
	if _, err := CanaryFenceFromContext(ctx, scope); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("terminal context error = %v", err)
	}
}

func TestCanaryReceiptBindingRejectsTamper(t *testing.T) {
	scope := newTestScope(t)
	canary := beginClaimedCanary(t, scope)
	if err := canary.MarkReady(testHash("d")); err != nil {
		t.Fatal(err)
	}
	if _, err := canary.Complete(OutcomeSucceeded, testHash("e")); err != nil {
		t.Fatal(err)
	}
	control, err := normalizeScope(scope)
	if err != nil {
		t.Fatal(err)
	}
	record := readTestRecord(t, control)
	record.Receipt.Canary.Port++
	if err := writeRecordJSON(control, record); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireShared(context.Background(), scope); !IsCode(err, CodeStateCorrupt) {
		t.Fatalf("tampered receipt error = %v", err)
	}
}

func TestExpiredCanaryFailsClosedAndDeadOwnerRecoversToRestoring(t *testing.T) {
	t.Run("expiry", func(t *testing.T) {
		base := time.Now().UTC()
		setTestClock(t, &base)
		scope := newTestScope(t)
		canary := beginClaimedCanary(t, scope)
		base = base.Add(2 * time.Minute)
		_, err := canary.Complete(OutcomeSucceeded, testHash("e"))
		assertCode(t, err, CodeRecoveryRequired)
		assertRecord(t, scope, func(record Record) bool { return record.State == StateRecoveryRequired })
	})
	t.Run("dead owner", func(t *testing.T) {
		scope := newTestScope(t)
		_, _ = beginPendingCanary(t, scope)
		control, err := normalizeScope(scope)
		if err != nil {
			t.Fatal(err)
		}
		record := readTestRecord(t, control)
		record.Intent.Owner = deadOwner()
		record.Intent.Canary.Owner = record.Intent.Owner
		record.Intent.Canary.BaseBindingSHA256, err = canaryBaseBindingSHA256(control, record.Intent, record.Intent.Canary)
		if err != nil {
			t.Fatal(err)
		}
		if err := refreshCanaryActiveBinding(&record); err != nil {
			t.Fatal(err)
		}
		if err := writeRecordJSON(control, record); err != nil {
			t.Fatal(err)
		}
		recovered, err := RecoverExclusive(context.Background(), scope, operationA, 1)
		if err != nil {
			t.Fatal(err)
		}
		assertRecord(t, scope, func(record Record) bool {
			return record.State == StateRestoring && record.Intent.Canary == nil
		})
		options := testCanaryOptions(recovered)
		options.ExpectedOutcome = OutcomeRolledBack
		if err := recovered.BeginReopening(os.Getpid(), options); err != nil {
			t.Fatal(err)
		}
		rollbackCanary, err := ClaimCanary(context.Background(), scope, operationA, 1, testCanaryCapability)
		if err != nil {
			t.Fatal(err)
		}
		if err := rollbackCanary.MarkReady(testHash("d")); err != nil {
			t.Fatal(err)
		}
		if _, err := rollbackCanary.Complete(OutcomeRolledBack, testHash("e")); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCanaryCapabilityIsNotPersistedAndDeadlineIsBounded(t *testing.T) {
	scope := newTestScope(t)
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BeginReopening(os.Getpid(), testCanaryOptions(lease)); !IsCode(err, CodeRecoveryRequired) {
		t.Fatalf("pre-mutation reopening error = %v", err)
	}
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	invalid := []struct {
		name   string
		mutate func(*CanaryOptions)
	}{
		{"uppercase hash", func(options *CanaryOptions) { options.PlanSHA256 = testHash("A") }},
		{"port", func(options *CanaryOptions) { options.Port = 0 }},
		{"attempt", func(options *CanaryOptions) { options.Attempt = 0 }},
		{"outcome", func(options *CanaryOptions) { options.ExpectedOutcome = OutcomeAborted }},
		{"state outcome", func(options *CanaryOptions) { options.ExpectedOutcome = OutcomeRolledBack }},
		{"duplicate fallback", func(options *CanaryOptions) { options.FallbackOutcome = OutcomeSucceeded }},
		{"invalid fallback", func(options *CanaryOptions) { options.FallbackOutcome = OutcomeAborted }},
	}
	for _, test := range invalid {
		options := testCanaryOptions(lease)
		test.mutate(&options)
		if err := lease.BeginReopening(os.Getpid(), options); !IsCode(err, CodeInvalid) {
			t.Fatalf("%s validation error = %v", test.name, err)
		}
	}
	options := testCanaryOptions(lease)
	options.Deadline = lease.ExpiresAt().Add(-RecoveryReserve + time.Second)
	if err := lease.BeginReopening(os.Getpid(), options); !IsCode(err, CodeInvalid) {
		t.Fatalf("unbounded deadline error = %v", err)
	}
	options = testCanaryOptions(lease)
	if err := lease.BeginReopening(os.Getpid(), options); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(scope.ControlRoot, "maintenance", recordFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), string(testCanaryCapability)) {
		t.Fatal("plaintext canary capability was persisted")
	}
}

func beginPendingCanary(t *testing.T, scope Scope) (*Lease, CanaryOptions) {
	t.Helper()
	lease, err := BeginExclusive(context.Background(), scope, exclusiveOptions(operationA))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.BeginMutation(); err != nil {
		t.Fatal(err)
	}
	options := testCanaryOptions(lease)
	if err := lease.BeginReopening(os.Getpid(), options); err != nil {
		t.Fatal(err)
	}
	return lease, options
}

func beginClaimedCanary(t *testing.T, scope Scope) *CanaryLease {
	t.Helper()
	_, _ = beginPendingCanary(t, scope)
	canary, err := ClaimCanary(context.Background(), scope, operationA, 1, testCanaryCapability)
	if err != nil {
		t.Fatal(err)
	}
	return canary
}

func testCanaryOptions(lease *Lease) CanaryOptions {
	return CanaryOptions{
		Capability: append([]byte(nil), testCanaryCapability...), PlanSHA256: testHash("1"),
		SnapshotManifestSHA256: testHash("2"), TargetBinarySHA256: testHash("3"),
		TargetVersion: "v0.7.0", Port: 8765, Attempt: 1, ExpectedOutcome: OutcomeSucceeded,
		Deadline: clockNow().Add(time.Minute),
	}
}

func readTestRecord(t *testing.T, control controlPlane) Record {
	t.Helper()
	record, exists, err := readRecord(control)
	if err != nil || !exists {
		t.Fatalf("read record: exists=%v err=%v", exists, err)
	}
	return record
}

func testHash(character string) string { return strings.Repeat(character, 64) }

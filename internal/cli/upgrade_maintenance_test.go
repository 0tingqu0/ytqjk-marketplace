package cli

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	upgradepkg "github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

func TestReadMaintenanceHandoffRequiresExactCapability(t *testing.T) {
	controlRoot := filepath.Clean(t.TempDir())
	capability := []byte(strings.Repeat("c", 32))
	t.Setenv(maintenanceControlRootEnv, controlRoot)
	t.Setenv(maintenanceOperationIDEnv, strings.Repeat("a", 64))
	t.Setenv(maintenanceGenerationEnv, "7")
	t.Setenv(maintenanceCapabilityEnv, base64.RawStdEncoding.EncodeToString(capability))
	actualRoot, operationID, generation, actualCapability, err := readMaintenanceHandoff()
	if err != nil {
		t.Fatal(err)
	}
	if actualRoot != controlRoot || operationID != strings.Repeat("a", 64) || generation != 7 ||
		string(actualCapability) != string(capability) {
		t.Fatalf("handoff root=%q operation=%q generation=%d capability=%q", actualRoot, operationID, generation, actualCapability)
	}
	clearBytes(actualCapability)
	t.Setenv(maintenanceCapabilityEnv, base64.RawStdEncoding.EncodeToString(capability[:31]))
	if _, _, _, _, err := readMaintenanceHandoff(); err == nil {
		t.Fatal("short maintenance capability was accepted")
	}
}

func TestActivationResultOutcomeRequiresSafeTerminalEvidence(t *testing.T) {
	tests := []struct {
		name     string
		result   upgradepkg.ActivateResult
		rollback bool
		outcome  maintenance.Outcome
		ok       bool
	}{
		{"upgrade", upgradepkg.ActivateResult{Status: "ACTIVE"}, false, maintenance.OutcomeSucceeded, true},
		{"ambiguous upgrade rollback", upgradepkg.ActivateResult{Status: "ACTIVE", Rollback: "UNKNOWN"}, false, "", false},
		{"automatic rollback", upgradepkg.ActivateResult{Status: "ROLLED_BACK", Rollback: "SUCCEEDED"}, false, maintenance.OutcomeRolledBack, true},
		{"explicit rollback", upgradepkg.ActivateResult{Status: "ACTIVE", Rollback: "SUCCEEDED"}, true, maintenance.OutcomeRolledBack, true},
		{"unknown rollback", upgradepkg.ActivateResult{Status: "ROLLED_BACK", Rollback: "UNKNOWN"}, false, "", false},
		{"failed", upgradepkg.ActivateResult{Status: "FAILED"}, false, "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			outcome, ok := activationResultOutcome(test.result, test.rollback)
			if outcome != test.outcome || ok != test.ok {
				t.Fatalf("outcome=%q ok=%v", outcome, ok)
			}
		})
	}
}

func TestClearMaintenanceHandoffEnvironmentRemovesCapability(t *testing.T) {
	for _, key := range []string{
		maintenanceControlRootEnv,
		maintenanceOperationIDEnv,
		maintenanceGenerationEnv,
		maintenanceCapabilityEnv,
	} {
		t.Setenv(key, "secret")
	}
	if err := clearMaintenanceHandoffEnvironment(); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		maintenanceControlRootEnv,
		maintenanceOperationIDEnv,
		maintenanceGenerationEnv,
		maintenanceCapabilityEnv,
	} {
		if _, exists := os.LookupEnv(key); exists {
			t.Fatalf("%s remains in the child environment", key)
		}
	}
}

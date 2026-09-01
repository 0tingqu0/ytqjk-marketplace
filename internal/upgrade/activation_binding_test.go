package upgrade

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestReadActivationBindingRequiresBoundSnapshot(t *testing.T) {
	root := operationTempDir(t)
	plan := operationUpgradePlan(root)
	if err := os.MkdirAll(plan.SourceRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	plan.SnapshotID = strings.Repeat("c", 64)
	plan.SnapshotManifestSHA256 = strings.Repeat("d", 64)
	plan.RestartDashboard = true
	if err := safeio.WriteJSON(planPath(plan), plan); err != nil {
		t.Fatal(err)
	}
	digest, err := safeio.FileSHA256(planPath(plan))
	if err != nil {
		t.Fatal(err)
	}
	binding, err := ReadActivationBinding(planPath(plan), digest)
	if err != nil {
		t.Fatal(err)
	}
	if binding.OperationID != plan.ID || binding.PlanSHA256 != digest ||
		binding.SnapshotManifestSHA256 != plan.SnapshotManifestSHA256 || binding.TargetVersion != plan.ToVersion ||
		!binding.RestartDashboard {
		t.Fatalf("binding=%#v", binding)
	}
}

func TestReadActivationStateEvidenceRequiresReleasedOperation(t *testing.T) {
	root := operationTempDir(t)
	if err := acquireOperation(root, testOperationA, phasePrepared); err != nil {
		t.Fatal(err)
	}
	state := State{Status: "ACTIVE", OperationID: testOperationA, CurrentVersion: "0.7.0"}
	if err := writeState(root, state); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadActivationStateEvidence(root, testOperationA); err == nil {
		t.Fatal("active operation lock was accepted as terminal evidence")
	}
	if err := releaseTerminalOperation(root, testOperationA, nil); err != nil {
		t.Fatal(err)
	}
	actual, digest, err := ReadActivationStateEvidence(root, testOperationA)
	if err != nil {
		t.Fatal(err)
	}
	if actual.Status != "ACTIVE" || len(digest) != 64 {
		t.Fatalf("state=%#v digest=%q", actual, digest)
	}
	if _, err := os.Stat(filepath.Join(root, "upgrade", "operation.json")); err != nil {
		t.Fatal(err)
	}
}

package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestWriteFailureCodesDistinguishPostCommitDurability(t *testing.T) {
	ordinary := errors.New("write failed")
	committed := &safeio.PostCommitError{Err: errors.New("directory sync failed")}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"state ordinary", stateWriteFailure(ordinary), "UPGRADE_STATE_WRITE_FAILED"},
		{"state committed", stateWriteFailure(committed), "UPGRADE_STATE_DURABILITY_UNKNOWN"},
		{"plan ordinary", planWriteFailure(ordinary), "UPGRADE_PLAN_WRITE_FAILED"},
		{"plan committed", planWriteFailure(committed), "UPGRADE_PLAN_DURABILITY_UNKNOWN"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := errorCodeOf(test.err); got != test.want {
				t.Fatalf("error code = %q, want %q", got, test.want)
			}
		})
	}
}

func TestWriteFailureStatePreservesBusinessCause(t *testing.T) {
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	businessCause := errors.New("helper start failed")
	err := writeFailureState(blockedRoot, State{Status: "FAILED"}, businessCause)
	if got := errorCodeOf(err); got != "UPGRADE_STATE_WRITE_FAILED" {
		t.Fatalf("error code = %q", got)
	}
	if !errors.Is(err, businessCause) {
		t.Fatalf("business cause was lost: %v", err)
	}
}

func TestRollbackStateWriteFailureIsNotReportedAsSucceeded(t *testing.T) {
	plan := Plan{FromVersion: "0.6.10", ToVersion: "0.7.0"}
	snapshot := Snapshot{ID: "snapshot-id"}
	result, err := rollbackStateWriteResult(
		plan,
		snapshot,
		errors.New("activation failed"),
		&safeio.PostCommitError{Err: errors.New("directory sync failed")},
	)
	if result.Status != "ROLLED_BACK" || result.Rollback != "UNKNOWN" {
		t.Fatalf("result = %#v", result)
	}
	if got := errorCodeOf(err); got != "UPGRADE_STATE_DURABILITY_UNKNOWN" {
		t.Fatalf("error code = %q", got)
	}
}

package upgrade

import (
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	planSchema  = "ytqjk-upgrade-plan/v1"
	stateSchema = "ytqjk-upgrade-state/v1"
)

type Plan struct {
	Schema            string    `json:"schema"`
	ID                string    `json:"id"`
	PreparedAt        time.Time `json:"prepared_at"`
	FromVersion       string    `json:"from_version"`
	ToVersion         string    `json:"to_version"`
	DatabaseSchema    int       `json:"database_schema"`
	PreviousMaxSchema int       `json:"previous_max_schema"`
	TargetMaxSchema   int       `json:"target_max_schema"`
	RuntimeRoot       string    `json:"runtime_root"`
	CodexRoot         string    `json:"codex_root"`
	KnowledgeRoot     string    `json:"knowledge_root"`
	StageRoot         string    `json:"stage_root"`
	SourceRoot        string    `json:"source_root"`
	BinaryPath        string    `json:"binary_path"`
	BinarySHA256      string    `json:"binary_sha256"`
	Port              int       `json:"port"`
	RestartDashboard  bool      `json:"restart_dashboard"`
	ReleaseURL        string    `json:"release_url"`
}

type State struct {
	Schema          string    `json:"schema"`
	Status          string    `json:"status"`
	OperationID     string    `json:"operation_id,omitempty"`
	CurrentVersion  string    `json:"current_version,omitempty"`
	PreviousVersion string    `json:"previous_version,omitempty"`
	TargetVersion   string    `json:"target_version,omitempty"`
	SnapshotID      string    `json:"snapshot_id,omitempty"`
	ErrorCode       string    `json:"error_code,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func Status(runtimeRoot, currentVersion string) State {
	var state State
	if safeio.ReadJSON(statePath(runtimeRoot), &state) != nil || state.Schema != stateSchema {
		return State{Schema: stateSchema, Status: "IDLE", CurrentVersion: currentVersion, UpdatedAt: time.Now().UTC()}
	}
	if state.CurrentVersion == "" {
		state.CurrentVersion = currentVersion
	}
	return state
}

func writeState(runtimeRoot string, state State) error {
	state.Schema = stateSchema
	state.UpdatedAt = time.Now().UTC()
	return safeio.WriteJSON(statePath(runtimeRoot), state)
}

func statePath(runtimeRoot string) string {
	return filepath.Join(runtimeRoot, "upgrade", "state.json")
}

func planPath(plan Plan) string {
	return filepath.Join(plan.StageRoot, "plan.json")
}

func readPlan(path string) (Plan, error) {
	var plan Plan
	if err := safeio.ReadJSON(path, &plan); err != nil {
		return Plan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	if err := validatePlan(plan, path); err != nil {
		return Plan{}, err
	}
	return plan, nil
}

func validatePlan(plan Plan, path string) error {
	if plan.Schema != planSchema || !hexDigestPattern.MatchString(plan.ID) || !hexDigestPattern.MatchString(plan.BinarySHA256) {
		return failure("UPGRADE_PLAN_INVALID", nil)
	}
	if _, err := parseVersion(plan.FromVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := parseVersion(plan.ToVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if plan.DatabaseSchema < 0 || plan.PreviousMaxSchema < plan.DatabaseSchema || plan.TargetMaxSchema < plan.DatabaseSchema || plan.Port < 1 || plan.Port > 65535 {
		return failure("UPGRADE_SCHEMA_INCOMPATIBLE", nil)
	}
	for _, root := range []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot, plan.StageRoot, plan.SourceRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return failure("UPGRADE_PLAN_INVALID", nil)
		}
	}
	upgradeRoot := filepath.Join(plan.RuntimeRoot, "upgrade")
	if _, err := safeio.Contained(upgradeRoot, plan.StageRoot); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := safeio.Contained(plan.StageRoot, plan.SourceRoot); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := safeio.Contained(plan.StageRoot, plan.BinaryPath); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	expectedPath, err := safeio.Contained(plan.StageRoot, path)
	if err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	actualPath, err := filepath.Abs(path)
	if err != nil || actualPath != expectedPath || filepath.Clean(path) != actualPath {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	return nil
}

func cleanupStage(plan Plan) error {
	upgradeRoot := filepath.Join(plan.RuntimeRoot, "upgrade")
	contained, err := safeio.Contained(upgradeRoot, plan.StageRoot)
	if err != nil {
		return err
	}
	return os.RemoveAll(contained)
}

func errorCodeOf(err error) string {
	var value *Error
	if errors.As(err, &value) {
		return value.Code
	}
	return "UPGRADE_FAILED"
}

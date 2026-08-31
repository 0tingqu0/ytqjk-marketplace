package upgrade

import (
	"context"
	"crypto/subtle"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const rollbackPlanSchema = "ytqjk-rollback-plan/v1"

type RollbackPlan struct {
	Schema           string    `json:"schema"`
	ID               string    `json:"id"`
	PreparedAt       time.Time `json:"prepared_at"`
	CurrentVersion   string    `json:"current_version"`
	TargetVersion    string    `json:"target_version"`
	TargetSnapshotID string    `json:"target_snapshot_id"`
	RuntimeRoot      string    `json:"runtime_root"`
	CodexRoot        string    `json:"codex_root"`
	KnowledgeRoot    string    `json:"knowledge_root"`
	StageRoot        string    `json:"stage_root"`
	BinaryPath       string    `json:"binary_path"`
	BinarySHA256     string    `json:"binary_sha256"`
	Port             int       `json:"port"`
	RestartDashboard bool      `json:"restart_dashboard"`
}

type RollbackOptions struct {
	RuntimeRoot      string
	CodexRoot        string
	KnowledgeRoot    string
	CurrentVersion   string
	CurrentBinary    string
	Port             int
	RestartDashboard bool
}

func PrepareRollback(ctx context.Context, options RollbackOptions) (RollbackPlan, error) {
	roots, err := absolutePrepareRoots(PrepareOptions{
		RuntimeRoot: options.RuntimeRoot, CodexRoot: options.CodexRoot,
		KnowledgeRoot: options.KnowledgeRoot, Port: options.Port,
	})
	if err != nil {
		return RollbackPlan{}, err
	}
	state := Status(roots.Runtime, options.CurrentVersion)
	if state.Status != "ACTIVE" || state.CurrentVersion != options.CurrentVersion || state.SnapshotID == "" || state.PreviousVersion == "" {
		return RollbackPlan{}, failure("ROLLBACK_NOT_AVAILABLE", nil)
	}
	target, err := readSnapshot(roots.Runtime, state.SnapshotID)
	if err != nil {
		return RollbackPlan{}, err
	}
	if !target.RuntimeBinary || target.FromVersion != state.PreviousVersion {
		return RollbackPlan{}, failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	databaseSchema, err := databaseSchemaVersion(filepath.Join(roots.Knowledge, "service", "knowledge.sqlite3"))
	if err != nil {
		return RollbackPlan{}, failure("KNOWLEDGE_SCHEMA_READ_FAILED", err)
	}
	if databaseSchema > target.PreviousMaxSchema {
		return RollbackPlan{}, failure("ROLLBACK_SCHEMA_INCOMPATIBLE", nil)
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return RollbackPlan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	stageRoot := filepath.Join(roots.Runtime, "upgrade", "staging", identifier)
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return RollbackPlan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	helpBinary := filepath.Join(stageRoot, "rollback-helper")
	if filepath.Ext(options.CurrentBinary) == ".exe" {
		helpBinary += ".exe"
	}
	if !regularFile(options.CurrentBinary) {
		return RollbackPlan{}, failure("ROLLBACK_HELPER_INVALID", nil)
	}
	if err := safeio.CopyFile(options.CurrentBinary, helpBinary, 0o700); err != nil {
		return RollbackPlan{}, failure("ROLLBACK_HELPER_INVALID", err)
	}
	digest, err := safeio.FileSHA256(helpBinary)
	if err != nil {
		return RollbackPlan{}, failure("ROLLBACK_HELPER_INVALID", err)
	}
	version, _, err := inspectReleaseBinary(ctx, helpBinary)
	if err != nil || version != options.CurrentVersion {
		return RollbackPlan{}, failure("ROLLBACK_HELPER_INVALID", err)
	}
	plan := RollbackPlan{
		Schema: rollbackPlanSchema, ID: identifier, PreparedAt: time.Now().UTC(),
		CurrentVersion: options.CurrentVersion, TargetVersion: state.PreviousVersion,
		TargetSnapshotID: state.SnapshotID, RuntimeRoot: roots.Runtime,
		CodexRoot: roots.Codex, KnowledgeRoot: roots.Knowledge,
		StageRoot: stageRoot, BinaryPath: helpBinary, BinarySHA256: digest,
		Port: options.Port, RestartDashboard: options.RestartDashboard,
	}
	if err := safeio.WriteJSON(rollbackPlanPath(plan), plan); err != nil {
		return RollbackPlan{}, failure("UPGRADE_PLAN_WRITE_FAILED", err)
	}
	if err := validateRollbackPlan(plan, rollbackPlanPath(plan)); err != nil {
		return RollbackPlan{}, err
	}
	if err := writeState(roots.Runtime, State{
		Status: "ROLLBACK_PREPARED", OperationID: identifier,
		CurrentVersion: options.CurrentVersion, PreviousVersion: state.PreviousVersion,
		TargetVersion: state.PreviousVersion, SnapshotID: state.SnapshotID,
	}); err != nil {
		return RollbackPlan{}, failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	complete = true
	return plan, nil
}

func LaunchRollback(plan RollbackPlan, parentPID int) error {
	path := rollbackPlanPath(plan)
	if err := validateRollbackPlan(plan, path); err != nil {
		return err
	}
	state := Status(plan.RuntimeRoot, plan.CurrentVersion)
	if state.Status != "ROLLBACK_PREPARED" || state.OperationID != plan.ID || state.SnapshotID != plan.TargetSnapshotID {
		return failure("UPGRADE_STATE_CONFLICT", nil)
	}
	logPath := filepath.Join(plan.RuntimeRoot, "upgrade", "upgrade.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return failure("UPGRADE_HELPER_START_FAILED", err)
	}
	command := exec.Command(plan.BinaryPath, "upgrade", "rollback-activate", "--plan", path, "--parent-pid", strconv.Itoa(parentPID))
	command.Stdin, command.Stdout, command.Stderr = nil, logFile, logFile
	configureDetached(command)
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ROLLBACK_PENDING", OperationID: plan.ID,
		CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
		TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
	}); err != nil {
		logFile.Close()
		return failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	if err := command.Start(); err != nil {
		logFile.Close()
		return failure("UPGRADE_HELPER_START_FAILED", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	return nil
}

func Rollback(ctx context.Context, planFile string) (ActivateResult, error) {
	plan, err := readRollbackPlan(planFile)
	if err != nil {
		return ActivateResult{}, err
	}
	state := Status(plan.RuntimeRoot, plan.CurrentVersion)
	if state.Status != "ROLLBACK_PENDING" || state.OperationID != plan.ID || state.SnapshotID != plan.TargetSnapshotID {
		return ActivateResult{}, failure("UPGRADE_STATE_CONFLICT", nil)
	}
	hash, err := safeio.FileSHA256(plan.BinaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(plan.BinarySHA256)) != 1 {
		return ActivateResult{}, failure("ROLLBACK_HELPER_INVALID", err)
	}
	if !waitForHealthState(ctx, plan.Port, "", false, 15*time.Second) {
		return ActivateResult{}, failure("DASHBOARD_STILL_RUNNING", nil)
	}
	target, err := readSnapshot(plan.RuntimeRoot, plan.TargetSnapshotID)
	if err != nil {
		return ActivateResult{}, err
	}
	databaseSchema, err := databaseSchemaVersion(filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3"))
	if err != nil || databaseSchema > target.PreviousMaxSchema {
		return ActivateResult{}, failure("ROLLBACK_SCHEMA_INCOMPATIBLE", err)
	}
	capturePlan := Plan{
		FromVersion: plan.CurrentVersion, ToVersion: plan.TargetVersion,
		DatabaseSchema: databaseSchema, PreviousMaxSchema: knowledge.LatestSchema,
		RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot, KnowledgeRoot: plan.KnowledgeRoot,
	}
	currentSnapshot, err := captureSnapshot(ctx, capturePlan)
	if err != nil {
		return ActivateResult{}, failure("UPGRADE_SNAPSHOT_FAILED", err)
	}
	if err := restoreSnapshot(capturePlan, target, false); err != nil {
		_ = restoreSnapshot(capturePlan, currentSnapshot, false)
		return ActivateResult{}, failure("UPGRADE_ROLLBACK_FAILED", err)
	}
	if plan.RestartDashboard {
		startPlan := Plan{RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot, KnowledgeRoot: plan.KnowledgeRoot, Port: plan.Port}
		if err := startDashboard(ctx, startPlan, plan.TargetVersion); err != nil {
			_ = stopDashboard(ctx, plan.BinaryPath, plan.KnowledgeRoot, plan.Port)
			_ = restoreSnapshot(capturePlan, currentSnapshot, false)
			_ = startDashboard(ctx, startPlan, plan.CurrentVersion)
			_ = writeState(plan.RuntimeRoot, State{
				Status: "ROLLBACK_FAILED", OperationID: plan.ID, CurrentVersion: plan.CurrentVersion,
				PreviousVersion: plan.TargetVersion, TargetVersion: plan.TargetVersion,
				SnapshotID: plan.TargetSnapshotID, ErrorCode: "UPGRADE_ROLLBACK_HEALTH_FAILED",
			})
			return ActivateResult{}, failure("UPGRADE_ROLLBACK_HEALTH_FAILED", err)
		}
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVE", OperationID: plan.ID, CurrentVersion: plan.TargetVersion,
		PreviousVersion: plan.CurrentVersion, TargetVersion: plan.TargetVersion,
		SnapshotID: currentSnapshot.ID,
	}); err != nil {
		return ActivateResult{}, failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	_ = pruneSnapshots(plan.RuntimeRoot, currentSnapshot.ID)
	return ActivateResult{
		Status: "ACTIVE", CurrentVersion: plan.TargetVersion,
		PreviousVersion: plan.CurrentVersion, SnapshotID: currentSnapshot.ID, Rollback: "SUCCEEDED",
	}, nil
}

func rollbackPlanPath(plan RollbackPlan) string {
	return filepath.Join(plan.StageRoot, "rollback-plan.json")
}

func readRollbackPlan(path string) (RollbackPlan, error) {
	var plan RollbackPlan
	if err := safeio.ReadJSON(path, &plan); err != nil {
		return RollbackPlan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	return plan, validateRollbackPlan(plan, path)
}

func validateRollbackPlan(plan RollbackPlan, path string) error {
	if plan.Schema != rollbackPlanSchema || !hexDigestPattern.MatchString(plan.ID) ||
		!hexDigestPattern.MatchString(plan.TargetSnapshotID) || !hexDigestPattern.MatchString(plan.BinarySHA256) ||
		plan.Port < 1 || plan.Port > 65535 {
		return failure("UPGRADE_PLAN_INVALID", nil)
	}
	if _, err := parseVersion(plan.CurrentVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := parseVersion(plan.TargetVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	for _, root := range []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot, plan.StageRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return failure("UPGRADE_PLAN_INVALID", nil)
		}
	}
	upgradeRoot := filepath.Join(plan.RuntimeRoot, "upgrade")
	if _, err := safeio.Contained(upgradeRoot, plan.StageRoot); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := safeio.Contained(plan.StageRoot, plan.BinaryPath); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	expected, err := safeio.Contained(plan.StageRoot, path)
	if err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil || expected != absolute || filepath.Clean(path) != absolute {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	return nil
}

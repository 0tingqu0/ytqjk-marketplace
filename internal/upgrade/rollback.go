package upgrade

import (
	"context"
	"crypto/subtle"
	"errors"
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
	Schema                       string    `json:"schema"`
	ID                           string    `json:"id"`
	PreparedAt                   time.Time `json:"prepared_at"`
	CurrentVersion               string    `json:"current_version"`
	TargetVersion                string    `json:"target_version"`
	TargetSnapshotID             string    `json:"target_snapshot_id"`
	TargetSnapshotManifestSHA256 string    `json:"target_snapshot_manifest_sha256"`
	TargetBinarySHA256           string    `json:"target_binary_sha256"`
	RuntimeRoot                  string    `json:"runtime_root"`
	CodexRoot                    string    `json:"codex_root"`
	KnowledgeRoot                string    `json:"knowledge_root"`
	StageRoot                    string    `json:"stage_root"`
	BinaryPath                   string    `json:"binary_path"`
	BinarySHA256                 string    `json:"binary_sha256"`
	Port                         int       `json:"port"`
	RestartDashboard             bool      `json:"restart_dashboard"`
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

func PrepareRollback(ctx context.Context, options RollbackOptions) (returned RollbackPlan, returnedErr error) {
	roots, err := absolutePrepareRoots(PrepareOptions{
		RuntimeRoot: options.RuntimeRoot, CodexRoot: options.CodexRoot,
		KnowledgeRoot: options.KnowledgeRoot, Port: options.Port,
	})
	if err != nil {
		return RollbackPlan{}, err
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return RollbackPlan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
	if err := acquireOperation(roots.Runtime, identifier, phaseRollbackPreparing); err != nil {
		return RollbackPlan{}, err
	}
	stageRoot := filepath.Join(roots.Runtime, "upgrade", "staging", identifier)
	stateStarted := false
	abandonOnFailure := false
	var state State
	defer func() {
		if returnedErr == nil {
			return
		}
		_ = os.RemoveAll(stageRoot)
		if !stateStarted {
			if abandonOnFailure {
				returnedErr = errors.Join(returnedErr, abandonOperation(roots.Runtime, identifier, phaseRollbackPreparing))
			}
			return
		}
		returnedErr = writeFailureState(roots.Runtime, State{
			Status: "FAILED", OperationID: identifier, CurrentVersion: options.CurrentVersion,
			PreviousVersion: state.PreviousVersion, TargetVersion: state.PreviousVersion,
			SnapshotID: state.SnapshotID, SnapshotManifestSHA256: state.SnapshotManifestSHA256,
			ErrorCode: errorCodeOf(returnedErr),
		}, returnedErr)
		returnedErr = errors.Join(returnedErr, releaseTerminalOperation(roots.Runtime, identifier, returnedErr))
	}()
	state, err = readOperationState(roots.Runtime)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			abandonOnFailure = true
			return RollbackPlan{}, failure("ROLLBACK_NOT_AVAILABLE", err)
		}
		return RollbackPlan{}, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	abandonOnFailure = true
	if !CanRollback(state, options.CurrentVersion) {
		return RollbackPlan{}, failure("ROLLBACK_NOT_AVAILABLE", nil)
	}
	stateStarted = true
	abandonOnFailure = false
	if err := writeState(roots.Runtime, State{
		Status: "ROLLBACK_PREPARING", OperationID: identifier,
		CurrentVersion: options.CurrentVersion, PreviousVersion: state.PreviousVersion,
		TargetVersion: state.PreviousVersion, SnapshotID: state.SnapshotID,
		SnapshotManifestSHA256: state.SnapshotManifestSHA256,
	}); err != nil {
		return RollbackPlan{}, stateWriteFailure(err)
	}
	target, err := readSnapshot(roots.Runtime, state.SnapshotID)
	if err != nil {
		return RollbackPlan{}, err
	}
	if target.ManifestSHA256 != state.SnapshotManifestSHA256 || !target.RuntimeBinary || target.FromVersion != state.PreviousVersion {
		return RollbackPlan{}, failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	targetBinarySHA256, err := snapshotRuntimeBinarySHA256(target)
	if err != nil {
		return RollbackPlan{}, err
	}
	databaseSchema, err := databaseSchemaVersion(filepath.Join(roots.Knowledge, "service", "knowledge.sqlite3"))
	if err != nil {
		return RollbackPlan{}, failure("KNOWLEDGE_SCHEMA_READ_FAILED", err)
	}
	if databaseSchema > target.PreviousMaxSchema {
		return RollbackPlan{}, failure("ROLLBACK_SCHEMA_INCOMPATIBLE", nil)
	}
	if err := os.MkdirAll(stageRoot, 0o700); err != nil {
		return RollbackPlan{}, failure("UPGRADE_STAGE_FAILED", err)
	}
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
		TargetSnapshotID: state.SnapshotID, TargetSnapshotManifestSHA256: state.SnapshotManifestSHA256,
		TargetBinarySHA256: targetBinarySHA256,
		RuntimeRoot:        roots.Runtime,
		CodexRoot:          roots.Codex, KnowledgeRoot: roots.Knowledge,
		StageRoot: stageRoot, BinaryPath: helpBinary, BinarySHA256: digest,
		Port: options.Port, RestartDashboard: options.RestartDashboard,
	}
	if err := safeio.WriteJSON(rollbackPlanPath(plan), plan); err != nil {
		return RollbackPlan{}, planWriteFailure(err)
	}
	if err := validateRollbackPlan(plan, rollbackPlanPath(plan)); err != nil {
		return RollbackPlan{}, err
	}
	if err := writeState(roots.Runtime, State{
		Status: "ROLLBACK_PREPARED", OperationID: identifier,
		CurrentVersion: options.CurrentVersion, PreviousVersion: state.PreviousVersion,
		TargetVersion: state.PreviousVersion, SnapshotID: state.SnapshotID,
		SnapshotManifestSHA256: state.SnapshotManifestSHA256,
	}); err != nil {
		return RollbackPlan{}, stateWriteFailure(err)
	}
	if err := transitionOperation(roots.Runtime, identifier, phaseRollbackPreparing, phaseRollbackPrepared); err != nil {
		return RollbackPlan{}, err
	}
	return plan, nil
}

func LaunchRollback(plan RollbackPlan, parentPID int, provided ...LaunchOptions) error {
	if parentPID != os.Getpid() {
		return failure("UPGRADE_HELPER_START_FAILED", errors.New("parent PID must match the launching process"))
	}
	options, err := resolveLaunchOptions(provided)
	if err != nil {
		return err
	}
	path := rollbackPlanPath(plan)
	if err := validateRollbackPlan(plan, path); err != nil {
		return err
	}
	if err := claimOperation(plan.RuntimeRoot, plan.ID, phaseRollbackPrepared); err != nil {
		return err
	}
	planDigest, err := rollbackPlanDigest(plan, path)
	if err != nil {
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, err)
	}
	state, err := readOperationState(plan.RuntimeRoot)
	if err != nil || state.Status != "ROLLBACK_PREPARED" || state.OperationID != plan.ID ||
		state.SnapshotID != plan.TargetSnapshotID || state.SnapshotManifestSHA256 != plan.TargetSnapshotManifestSHA256 {
		cause := failure("UPGRADE_RECOVERY_REQUIRED", err)
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, cause)
	}
	if err := transitionOperation(plan.RuntimeRoot, plan.ID, phaseRollbackPrepared, phaseRollbackPending); err != nil {
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, err)
	}
	logPath := filepath.Join(plan.RuntimeRoot, "upgrade", "upgrade.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		cause := failure("UPGRADE_HELPER_START_FAILED", err)
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, cause)
	}
	command := exec.Command(
		plan.BinaryPath, "upgrade", "rollback-activate", "--plan", path,
		"--plan-sha256", planDigest, "--parent-pid", strconv.Itoa(parentPID),
	)
	command.Stdin, command.Stdout, command.Stderr = nil, logFile, logFile
	applyLaunchOptions(command, options)
	configureDetached(command)
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ROLLBACK_PENDING", OperationID: plan.ID,
		CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
		TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
		SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
	}); err != nil {
		logFile.Close()
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, stateWriteFailure(err))
	}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		cause := failure("UPGRADE_HELPER_START_FAILED", err)
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, cause)
	}
	if err := transferOperation(plan.RuntimeRoot, plan.ID, phaseRollbackPending, os.Getpid(), command.Process.Pid); err != nil {
		stopErr := stopStartedHelper(command)
		_ = logFile.Close()
		if stopErr != nil {
			return errors.Join(err, failure("UPGRADE_RECOVERY_REQUIRED", stopErr))
		}
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollbackPrepared, State{
			CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
			TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		}, err)
	}
	if options.Transfer != nil {
		if err := options.Transfer(command.Process.Pid); err != nil {
			stopErr := stopStartedHelper(command)
			_ = logFile.Close()
			if stopErr != nil {
				return errors.Join(err, failure("UPGRADE_RECOVERY_REQUIRED", stopErr))
			}
			if reclaimErr := reclaimOperationOwnerFromStoppedChild(
				plan.RuntimeRoot, plan.ID, phaseRollbackPending, command.Process.Pid,
			); reclaimErr != nil {
				return errors.Join(err, failure("UPGRADE_RECOVERY_REQUIRED", reclaimErr))
			}
			return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollbackPrepared, State{
				CurrentVersion: plan.CurrentVersion, PreviousVersion: plan.TargetVersion,
				TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
				SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
			}, err)
		}
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	return nil
}

func Rollback(
	ctx context.Context,
	planFile, expectedPlanSHA256 string,
	hooks ...ActivationHooks,
) (returned ActivateResult, returnedErr error) {
	plan, err := readAuthenticatedRollbackPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return ActivateResult{}, errors.Join(err, abortPendingFromPlanPath(
			planFile, "rollback-plan.json", phaseRollbackPending, "ROLLBACK_PENDING", errorCodeOf(err),
		))
	}
	if err := claimOperation(plan.RuntimeRoot, plan.ID, phaseRollbackPending); err != nil {
		return ActivateResult{}, err
	}
	defer func() {
		returnedErr = errors.Join(returnedErr, releaseTerminalOperation(plan.RuntimeRoot, plan.ID, returnedErr))
	}()
	state := Status(plan.RuntimeRoot, plan.CurrentVersion)
	if state.Status != "ROLLBACK_PENDING" || state.OperationID != plan.ID || state.SnapshotID != plan.TargetSnapshotID ||
		state.SnapshotManifestSHA256 != plan.TargetSnapshotManifestSHA256 {
		cause := failure("UPGRADE_STATE_CONFLICT", nil)
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	hash, err := safeio.FileSHA256(plan.BinaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(hash), []byte(plan.BinarySHA256)) != 1 {
		cause := failure("ROLLBACK_HELPER_INVALID", err)
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	if !waitForHealthState(ctx, plan.Port, "", false, 15*time.Second) {
		cause := failure("DASHBOARD_STILL_RUNNING", nil)
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	target, err := readSnapshot(plan.RuntimeRoot, plan.TargetSnapshotID)
	if err != nil {
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", err)
	}
	if target.ManifestSHA256 != plan.TargetSnapshotManifestSHA256 {
		cause := failure("UPGRADE_SNAPSHOT_INVALID", errors.New("rollback snapshot manifest digest mismatch"))
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	databaseSchema, err := databaseSchemaVersion(filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3"))
	if err != nil || databaseSchema > target.PreviousMaxSchema {
		cause := failure("ROLLBACK_SCHEMA_INCOMPATIBLE", err)
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	capturePlan := Plan{
		FromVersion: plan.CurrentVersion, ToVersion: plan.TargetVersion,
		DatabaseSchema: databaseSchema, PreviousMaxSchema: knowledge.LatestSchema,
		RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot, KnowledgeRoot: plan.KnowledgeRoot,
	}
	if err := transitionOperation(plan.RuntimeRoot, plan.ID, phaseRollbackPending, phaseRollingBack); err != nil {
		return ActivateResult{}, err
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ROLLING_BACK", OperationID: plan.ID, CurrentVersion: plan.CurrentVersion,
		PreviousVersion: plan.TargetVersion, TargetVersion: plan.TargetVersion, SnapshotID: plan.TargetSnapshotID,
		SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
	}); err != nil {
		return ActivateResult{}, stateWriteFailure(err)
	}
	currentSnapshot, err := captureSnapshot(ctx, capturePlan)
	if err != nil {
		cause := failure("UPGRADE_SNAPSHOT_FAILED", err)
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	if err := restoreSnapshot(capturePlan, target, false); err != nil {
		cause := failure("UPGRADE_ROLLBACK_FAILED", err)
		if restoreErr := restoreSnapshot(capturePlan, currentSnapshot, false); restoreErr != nil {
			cause = failure("UPGRADE_ROLLBACK_FAILED", errors.Join(cause, restoreErr))
			return ActivateResult{}, writeRollbackFailureState(plan, "ROLLBACK_FAILED", cause)
		}
		return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
	}
	if plan.RestartDashboard {
		targetConfiguration := DashboardActivation{
			RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot,
			KnowledgeRoot: plan.KnowledgeRoot, Version: plan.TargetVersion, Port: plan.Port,
		}
		if err := configureDashboard(ctx, hooks, targetConfiguration); err != nil {
			cause := failure("UPGRADE_ROLLBACK_HEALTH_FAILED", err)
			restoreErr := restoreSnapshot(capturePlan, currentSnapshot, false)
			currentConfiguration := targetConfiguration
			currentConfiguration.Version = plan.CurrentVersion
			configurationErr := configureDashboard(ctx, hooks, currentConfiguration)
			if restoreErr != nil || configurationErr != nil {
				cause = failure("UPGRADE_ROLLBACK_HEALTH_FAILED", errors.Join(cause, restoreErr, configurationErr))
				return ActivateResult{}, writeRollbackFailureState(plan, "ROLLBACK_FAILED", cause)
			}
			return ActivateResult{}, writeRollbackFailureState(plan, "FAILED", cause)
		}
	}
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVE", OperationID: plan.ID, CurrentVersion: plan.TargetVersion,
		PreviousVersion: plan.CurrentVersion, TargetVersion: plan.TargetVersion,
		SnapshotID: currentSnapshot.ID, SnapshotManifestSHA256: currentSnapshot.ManifestSHA256,
	}); err != nil {
		return ActivateResult{}, stateWriteFailure(err)
	}
	_ = pruneSnapshots(plan.RuntimeRoot, currentSnapshot.ID)
	return ActivateResult{
		Status: "ACTIVE", CurrentVersion: plan.TargetVersion,
		PreviousVersion: plan.CurrentVersion, SnapshotID: currentSnapshot.ID,
		SnapshotManifestSHA256: currentSnapshot.ManifestSHA256, Rollback: "SUCCEEDED",
	}, nil
}

func writeRollbackFailureState(plan RollbackPlan, status string, cause error) error {
	return writeFailureState(plan.RuntimeRoot, State{
		Status: status, OperationID: plan.ID, CurrentVersion: plan.CurrentVersion,
		PreviousVersion: plan.TargetVersion, TargetVersion: plan.TargetVersion,
		SnapshotID: plan.TargetSnapshotID, SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		ErrorCode: errorCodeOf(cause),
	}, cause)
}

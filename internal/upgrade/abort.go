package upgrade

import (
	"errors"
	"path/filepath"
	"strings"
)

var writeAbortState = writeState

func AbortPrepared(plan Plan, causeCode string) error {
	if err := validatePlan(plan, planPath(plan)); err != nil {
		return err
	}
	return abortOwnedOperation(
		plan.RuntimeRoot,
		plan.ID,
		phasePrepared,
		"PREPARED",
		State{CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion},
		causeCode,
	)
}

func AbortPreparedRollback(plan RollbackPlan, causeCode string) error {
	if err := validateRollbackPlan(plan, rollbackPlanPath(plan)); err != nil {
		return err
	}
	return abortOwnedOperation(
		plan.RuntimeRoot,
		plan.ID,
		phaseRollbackPrepared,
		"ROLLBACK_PREPARED",
		State{
			CurrentVersion:         plan.CurrentVersion,
			PreviousVersion:        plan.TargetVersion,
			TargetVersion:          plan.TargetVersion,
			SnapshotID:             plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		},
		causeCode,
	)
}

func AbortPendingActivation(planFile, expectedPlanSHA256, causeCode string) error {
	plan, err := readAuthenticatedPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return errors.Join(err, abortPendingFromPlanPath(
			planFile, "plan.json", phaseActivationPending, "ACTIVATION_PENDING", causeCode,
		))
	}
	return abortOwnedOperation(
		plan.RuntimeRoot,
		plan.ID,
		phaseActivationPending,
		"ACTIVATION_PENDING",
		State{CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion},
		causeCode,
	)
}

func AbortPendingRollback(planFile, expectedPlanSHA256, causeCode string) error {
	plan, err := readAuthenticatedRollbackPlan(planFile, expectedPlanSHA256)
	if err != nil {
		return errors.Join(err, abortPendingFromPlanPath(
			planFile, "rollback-plan.json", phaseRollbackPending, "ROLLBACK_PENDING", causeCode,
		))
	}
	return abortOwnedOperation(
		plan.RuntimeRoot,
		plan.ID,
		phaseRollbackPending,
		"ROLLBACK_PENDING",
		State{
			CurrentVersion:         plan.CurrentVersion,
			PreviousVersion:        plan.TargetVersion,
			TargetVersion:          plan.TargetVersion,
			SnapshotID:             plan.TargetSnapshotID,
			SnapshotManifestSHA256: plan.TargetSnapshotManifestSHA256,
		},
		causeCode,
	)
}

func abortPendingFromPlanPath(planFile, planName, phase, expectedStatus, causeCode string) error {
	runtimeRoot, operationID, err := operationFromPlanPath(planFile, planName)
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	state, err := readOperationState(runtimeRoot)
	if err != nil || state.OperationID != operationID || state.Status != expectedStatus {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return abortOwnedOperation(runtimeRoot, operationID, phase, expectedStatus, state, causeCode)
}

func operationFromPlanPath(planFile, planName string) (string, string, error) {
	absolute, err := filepath.Abs(planFile)
	if err != nil || !filepath.IsAbs(planFile) || filepath.Clean(planFile) != absolute || filepath.Base(absolute) != planName {
		return "", "", failure("UPGRADE_PLAN_INVALID", err)
	}
	operationRoot := filepath.Dir(absolute)
	operationID := filepath.Base(operationRoot)
	stagingRoot := filepath.Dir(operationRoot)
	upgradeRoot := filepath.Dir(stagingRoot)
	runtimeRoot := filepath.Dir(upgradeRoot)
	if !hexDigestPattern.MatchString(operationID) || filepath.Base(stagingRoot) != "staging" ||
		filepath.Base(upgradeRoot) != "upgrade" ||
		filepath.Join(runtimeRoot, "upgrade", "staging", operationID, planName) != absolute {
		return "", "", failure("UPGRADE_PLAN_INVALID", nil)
	}
	return runtimeRoot, operationID, nil
}

func abortOwnedOperation(
	runtimeRoot, operationID, phase, expectedStatus string,
	state State,
	causeCode string,
) error {
	causeCode = strings.TrimSpace(causeCode)
	if causeCode == "" || len(causeCode) > 128 {
		return failure("UPGRADE_OPERATION_LOCK_INVALID", nil)
	}
	if err := claimOperation(runtimeRoot, operationID, phase); err != nil {
		return err
	}
	current, err := readOperationState(runtimeRoot)
	if err != nil || current.OperationID != operationID || current.Status != expectedStatus {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	state.Status = "FAILED"
	state.OperationID = operationID
	state.ErrorCode = causeCode
	if err := writeAbortState(runtimeRoot, state); err != nil {
		return stateWriteFailure(err)
	}
	return releaseTerminalOperation(runtimeRoot, operationID, nil)
}

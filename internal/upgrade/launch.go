package upgrade

import (
	"crypto/subtle"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

var (
	killStartedHelper = func(command *exec.Cmd) error { return command.Process.Kill() }
	waitStartedHelper = func(command *exec.Cmd) error { return command.Wait() }
	helperStopWait    = 5 * time.Second
)

func Launch(plan Plan, parentPID int, provided ...LaunchOptions) error {
	if parentPID != os.Getpid() {
		return failure("UPGRADE_HELPER_START_FAILED", errors.New("parent PID must match the launching process"))
	}
	options, err := resolveLaunchOptions(provided)
	if err != nil {
		return err
	}
	path := planPath(plan)
	if err := validatePlan(plan, path); err != nil {
		return err
	}
	if err := claimOperation(plan.RuntimeRoot, plan.ID, phasePrepared); err != nil {
		return err
	}
	planDigest, err := launchPlanDigest(plan, path)
	if err != nil {
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, err)
	}
	state, err := readOperationState(plan.RuntimeRoot)
	if err != nil || state.Status != "PREPARED" || state.OperationID != plan.ID {
		cause := failure("UPGRADE_RECOVERY_REQUIRED", err)
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, cause)
	}
	if err := transitionOperation(plan.RuntimeRoot, plan.ID, phasePrepared, phaseActivationPending); err != nil {
		return terminalizeOwnedPreMutation(plan.RuntimeRoot, plan.ID, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, err)
	}
	logPath := filepath.Join(plan.RuntimeRoot, "upgrade", "upgrade.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		cause := failure("UPGRADE_HELPER_START_FAILED", err)
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, cause)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		cause := failure("UPGRADE_HELPER_START_FAILED", err)
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, cause)
	}
	command := exec.Command(
		plan.BinaryPath, "upgrade", "activate", "--plan", path,
		"--plan-sha256", planDigest, "--parent-pid", strconv.Itoa(parentPID),
	)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	applyLaunchOptions(command, options)
	configureDetached(command)
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATION_PENDING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion,
	}); err != nil {
		logFile.Close()
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, stateWriteFailure(err))
	}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		cause := failure("UPGRADE_HELPER_START_FAILED", err)
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
		}, cause)
	}
	if err := transferOperation(plan.RuntimeRoot, plan.ID, phaseActivationPending, os.Getpid(), command.Process.Pid); err != nil {
		stopErr := stopStartedHelper(command)
		_ = logFile.Close()
		if stopErr != nil {
			return errors.Join(err, failure("UPGRADE_RECOVERY_REQUIRED", stopErr))
		}
		return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
			CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
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
				plan.RuntimeRoot, plan.ID, phaseActivationPending, command.Process.Pid,
			); reclaimErr != nil {
				return errors.Join(err, failure("UPGRADE_RECOVERY_REQUIRED", reclaimErr))
			}
			return terminalizeLaunchFailure(plan.RuntimeRoot, plan.ID, phaseActivationPending, phasePrepared, State{
				CurrentVersion: plan.FromVersion, TargetVersion: plan.ToVersion,
			}, err)
		}
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	return nil
}

func terminalizeLaunchFailure(
	runtimeRoot, operationID, pendingPhase, preparedPhase string,
	state State,
	cause error,
) error {
	if err := restoreOperationOwner(runtimeRoot, operationID, pendingPhase, preparedPhase); err != nil {
		return errors.Join(cause, failure("UPGRADE_RECOVERY_REQUIRED", err))
	}
	state.Status = "FAILED"
	state.OperationID = operationID
	state.ErrorCode = errorCodeOf(cause)
	result := writeFailureState(runtimeRoot, state, cause)
	return errors.Join(result, releaseTerminalOperation(runtimeRoot, operationID, result))
}

func terminalizeOwnedPreMutation(
	runtimeRoot, operationID, phase string,
	state State,
	cause error,
) error {
	if _, err := ownedOperation(runtimeRoot, operationID, phase); err != nil {
		return errors.Join(cause, failure("UPGRADE_RECOVERY_REQUIRED", err))
	}
	state.Status = "FAILED"
	state.OperationID = operationID
	state.ErrorCode = errorCodeOf(cause)
	result := writeFailureState(runtimeRoot, state, cause)
	return errors.Join(result, releaseTerminalOperation(runtimeRoot, operationID, result))
}

func stopStartedHelper(command *exec.Cmd) error {
	if err := killStartedHelper(command); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	wait := waitStartedHelper
	waited := make(chan error, 1)
	go func() { waited <- wait(command) }()
	timer := time.NewTimer(helperStopWait)
	defer timer.Stop()
	select {
	case waitErr := <-waited:
		if waitErr == nil || errors.Is(waitErr, os.ErrProcessDone) ||
			(command.ProcessState != nil && command.ProcessState.Exited()) {
			return nil
		}
		return waitErr
	case <-timer.C:
		return errors.New("upgrade helper did not stop within the bounded wait")
	}
}

func launchPlanDigest(plan Plan, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	var stored Plan
	if err := decodeStrictJSON(data, &stored); err != nil {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	if err := validatePlan(stored, path); err != nil || stored != plan {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	binaryHash, err := safeio.FileSHA256(plan.BinaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(binaryHash), []byte(plan.BinarySHA256)) != 1 {
		return "", failure("RELEASE_BINARY_INVALID", err)
	}
	return safeio.SHA256(data), nil
}

func WaitForParent(parentPID int, timeout time.Duration) error {
	if parentPID <= 0 || parentPID == os.Getpid() {
		return nil
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	if err := waitForProcessExit(parentPID, timeout); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return nil
		}
		return failure("UPGRADE_PARENT_STILL_RUNNING", err)
	}
	return nil
}

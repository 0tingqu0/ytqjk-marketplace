package upgrade

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

func Launch(plan Plan, parentPID int) error {
	path := planPath(plan)
	if err := validatePlan(plan, path); err != nil {
		return err
	}
	state := Status(plan.RuntimeRoot, plan.FromVersion)
	if state.Status != "PREPARED" || state.OperationID != plan.ID {
		return failure("UPGRADE_STATE_CONFLICT", nil)
	}
	logPath := filepath.Join(plan.RuntimeRoot, "upgrade", "upgrade.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return failure("UPGRADE_HELPER_START_FAILED", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return failure("UPGRADE_HELPER_START_FAILED", err)
	}
	command := exec.Command(plan.BinaryPath, "upgrade", "activate", "--plan", path, "--parent-pid", strconv.Itoa(parentPID))
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	configureDetached(command)
	if err := writeState(plan.RuntimeRoot, State{
		Status: "ACTIVATION_PENDING", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
		TargetVersion: plan.ToVersion,
	}); err != nil {
		logFile.Close()
		return failure("UPGRADE_STATE_WRITE_FAILED", err)
	}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		_ = writeState(plan.RuntimeRoot, State{
			Status: "PREPARED", OperationID: plan.ID, CurrentVersion: plan.FromVersion,
			TargetVersion: plan.ToVersion, ErrorCode: "UPGRADE_HELPER_START_FAILED",
		})
		return failure("UPGRADE_HELPER_START_FAILED", err)
	}
	_ = command.Process.Release()
	_ = logFile.Close()
	return nil
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

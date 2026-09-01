package cli

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	upgradepkg "github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

func (commandContext commandContext) activateUpgrade(arguments []string) error {
	flags := quietFlags("upgrade activate")
	planPath := flags.String("plan", "", "prepared upgrade plan")
	planSHA256 := flags.String("plan-sha256", "", "prepared upgrade plan SHA-256")
	parentPID := flags.Int("parent-pid", 0, "parent process to wait for")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	if !filepath.IsAbs(*planPath) || *planSHA256 == "" || *parentPID < 0 {
		return errors.New("upgrade activation arguments are invalid")
	}
	if err := upgradepkg.WaitForParent(*parentPID, 45*time.Second); err != nil {
		return errors.Join(err, upgradepkg.AbortPendingActivation(*planPath, *planSHA256, "UPGRADE_PARENT_STILL_RUNNING"))
	}
	binding, err := upgradepkg.ReadActivationBinding(*planPath, *planSHA256)
	if err != nil {
		return errors.Join(err, upgradepkg.AbortPendingActivation(*planPath, *planSHA256, "UPGRADE_PLAN_INVALID"))
	}
	claim, err := claimActivationCanary(context.Background(), binding)
	if err != nil {
		return err
	}
	result, activationErr := upgradepkg.Activate(
		claim.ctx, *planPath, *planSHA256, dashboardActivationHooks(),
	)
	outcome, terminal := activationResultOutcome(result, false)
	if !terminal {
		return errors.Join(activationErr, errors.New("upgrade activation did not reach a safe canary outcome"))
	}
	canaryErr := completeActivationCanary(
		claim, binding, result, outcome,
		maintenance.OutcomeSucceeded, maintenance.OutcomeRolledBack,
	)
	dashboardErr := error(nil)
	if canaryErr == nil {
		dashboardErr = commandContext.startDashboardAfterCanary(binding, true)
	}
	if writeErr := commandContext.write(result); writeErr != nil {
		return errors.Join(activationErr, canaryErr, dashboardErr, writeErr)
	}
	return errors.Join(activationErr, canaryErr, dashboardErr)
}

func (commandContext commandContext) activateRollback(arguments []string) error {
	flags := quietFlags("upgrade rollback-activate")
	planPath := flags.String("plan", "", "prepared rollback plan")
	planSHA256 := flags.String("plan-sha256", "", "prepared rollback plan SHA-256")
	parentPID := flags.Int("parent-pid", 0, "parent process to wait for")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	if !filepath.IsAbs(*planPath) || *planSHA256 == "" || *parentPID < 0 {
		return errors.New("rollback activation arguments are invalid")
	}
	if err := upgradepkg.WaitForParent(*parentPID, 45*time.Second); err != nil {
		return errors.Join(err, upgradepkg.AbortPendingRollback(*planPath, *planSHA256, "UPGRADE_PARENT_STILL_RUNNING"))
	}
	binding, err := upgradepkg.ReadRollbackBinding(*planPath, *planSHA256)
	if err != nil {
		return errors.Join(err, upgradepkg.AbortPendingRollback(*planPath, *planSHA256, "UPGRADE_PLAN_INVALID"))
	}
	claim, err := claimActivationCanary(context.Background(), binding)
	if err != nil {
		return err
	}
	result, rollbackErr := upgradepkg.Rollback(
		claim.ctx, *planPath, *planSHA256, dashboardActivationHooks(),
	)
	outcome, terminal := activationResultOutcome(result, true)
	if !terminal {
		return errors.Join(rollbackErr, errors.New("rollback activation did not reach a safe canary outcome"))
	}
	canaryErr := completeActivationCanary(
		claim, binding, result, outcome,
		maintenance.OutcomeRolledBack, maintenance.OutcomeFailedSafe,
	)
	dashboardErr := error(nil)
	if canaryErr == nil {
		dashboardErr = commandContext.startDashboardAfterCanary(binding, false)
	}
	if writeErr := commandContext.write(result); writeErr != nil {
		return errors.Join(rollbackErr, canaryErr, dashboardErr, writeErr)
	}
	return errors.Join(rollbackErr, canaryErr, dashboardErr)
}

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	upgradepkg "github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

func dashboardActivationHooks() upgradepkg.ActivationHooks {
	return upgradepkg.ActivationHooks{ConfigureDashboard: configureDashboardActivation}
}

func configureDashboardActivation(ctx context.Context, configuration upgradepkg.DashboardActivation) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	active, _, err := runtimeentry.ReadActive(configuration.RuntimeRoot)
	if err != nil || active.Version != configuration.Version {
		return errors.Join(errors.New("active runtime differs from dashboard configuration"), err)
	}
	assets := filepath.Join(
		configuration.CodexRoot, "plugins", "ytqjk-agentic-orchestrator", "skills", "ytqjk", "dashboard",
	)
	status := dashboard.ConfigureService(
		runtimeentry.LauncherPath(configuration.RuntimeRoot),
		configuration.KnowledgeRoot,
		assets,
		configuration.Port,
	)
	if status.Status != "CONFIGURED" {
		return fmt.Errorf("dashboard service configuration failed: status=%s", status.Status)
	}
	return nil
}

func (commandContext commandContext) startDashboardAfterCanary(
	binding upgradepkg.ActivationBinding,
	allowCompensation bool,
) error {
	if !binding.RestartDashboard {
		return nil
	}
	status := dashboard.StartConfiguredService(binding.Port)
	if status.Status == "RUNNING" {
		return nil
	}
	cause := fmt.Errorf("dashboard service failed after maintenance reopened: status=%s", status.Status)
	if !allowCompensation {
		return cause
	}
	rollbackErr := commandContext.applyRollbackWithState(
		binding.RuntimeRoot, binding.CodexRoot, binding.KnowledgeRoot,
		binding.Port, true, true,
	)
	if rollbackErr != nil {
		return errors.Join(cause, errors.New("dashboard compensation rollback failed to launch"), rollbackErr)
	}
	return errors.Join(cause, errors.New("dashboard compensation rollback was launched"))
}

func restartDashboardAfterActivationFailure(binary, knowledgeRoot string, port int, enabled bool) error {
	if !enabled {
		return nil
	}
	assets, err := resolveAssets("", "dashboard")
	if err != nil {
		return err
	}
	status := dashboard.StartService(binary, knowledgeRoot, assets, port)
	if status.Status == "FAILED" {
		return errors.New("dashboard restart failed after activation failure")
	}
	return nil
}

func restartCurrentDashboardAfterActivationFailure(knowledgeRoot string, port int, enabled bool) error {
	if !enabled {
		return nil
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	return restartDashboardAfterActivationFailure(binary, knowledgeRoot, port, true)
}

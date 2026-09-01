package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
	upgradepkg "github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

func (commandContext commandContext) upgrade(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "check", "apply", "status", "rollback", "schema-version", "activate", "rollback-activate")
	if err != nil {
		return err
	}
	if command == "schema-version" {
		if err := requireNoPositionals(arguments); err != nil {
			return err
		}
		_, err = fmt.Fprintln(commandContext.out, knowledge.LatestSchema)
		return err
	}
	if command == "activate" {
		return commandContext.activateUpgrade(arguments)
	}
	if command == "rollback-activate" {
		return commandContext.activateRollback(arguments)
	}
	flags := quietFlags("upgrade " + command)
	runtimeValue := flags.String("runtime-root", "", "runtime root")
	codexValue := flags.String("codex-root", "", "Codex home")
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	port := flags.Int("port", dashboard.DefaultPort, "dashboard loopback port")
	yes := flags.Bool("yes", false, "confirm upgrade mutation")
	dashboardService := flags.String("dashboard-service", "auto", "auto or off")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	runtimeRoot, codexRoot, knowledgeRoot, err := upgradeRoots(*runtimeValue, *codexValue, *knowledgeValue)
	if err != nil {
		return err
	}
	switch command {
	case "status":
		return commandContext.write(upgradepkg.Status(runtimeRoot, buildinfo.Version))
	case "check":
		result, _, err := upgradepkg.NewClient().Check(context.Background(), buildinfo.Version)
		if err != nil {
			return err
		}
		return commandContext.write(result)
	case "rollback":
		if !*yes {
			return errors.New("upgrade rollback requires --yes")
		}
		if *dashboardService != "auto" && *dashboardService != "off" {
			return errors.New("--dashboard-service must be auto or off")
		}
		return commandContext.applyRollback(runtimeRoot, codexRoot, knowledgeRoot, *port, *dashboardService)
	case "apply":
		if !*yes {
			return errors.New("upgrade apply requires --yes")
		}
		if *dashboardService != "auto" && *dashboardService != "off" {
			return errors.New("--dashboard-service must be auto or off")
		}
		return commandContext.applyUpgrade(runtimeRoot, codexRoot, knowledgeRoot, *port, *dashboardService)
	default:
		return errors.New("unsupported upgrade command")
	}
}

func (commandContext commandContext) applyUpgrade(runtimeRoot, codexRoot, knowledgeRoot string, port int, dashboardService string) error {
	client := upgradepkg.NewClient()
	check, release, err := client.Check(context.Background(), buildinfo.Version)
	if err != nil {
		return err
	}
	if !check.UpdateAvailable {
		return commandContext.write(map[string]any{
			"status": "UP_TO_DATE", "current_version": buildinfo.Version,
			"latest_version": check.LatestVersion,
		})
	}
	wasRunning := dashboard.Probe(port).Status == "RUNNING"
	plan, err := upgradepkg.Prepare(context.Background(), client, release, upgradepkg.PrepareOptions{
		RuntimeRoot: runtimeRoot, CodexRoot: codexRoot, KnowledgeRoot: knowledgeRoot,
		CurrentVersion: buildinfo.Version, Port: port,
		RestartDashboard: wasRunning && dashboardService == "auto",
	})
	if err != nil {
		return err
	}
	controller, err := beginUpgradeMaintenance(context.Background(), upgradepkg.ActivationBinding{
		OperationID: plan.ID, RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot,
		KnowledgeRoot: plan.KnowledgeRoot,
	}, "UPGRADE_V070_ACTIVATION")
	if err != nil {
		return errors.Join(err, upgradepkg.AbortPrepared(plan, "MAINTENANCE_ADMISSION_FAILED"))
	}
	if err := controller.beginMutation(false); err != nil {
		return controller.fail(errors.Join(err, upgradepkg.AbortPrepared(plan, "MAINTENANCE_MUTATION_FAILED")))
	}
	if wasRunning {
		status := dashboard.StopService(knowledgeRoot, port)
		if status.Status == "FAILED" {
			cause := errors.New("dashboard stop failed before upgrade activation")
			return controller.fail(errors.Join(cause, upgradepkg.AbortPrepared(plan, "DASHBOARD_STOP_FAILED")))
		}
	}
	preparedPlan := plan
	boundPlan, err := upgradepkg.BindActivationSnapshot(context.Background(), preparedPlan)
	if err != nil {
		restartErr := restartCurrentDashboardAfterActivationFailure(
			knowledgeRoot, port, wasRunning && dashboardService == "auto",
		)
		cause := errors.Join(err, upgradepkg.AbortPrepared(preparedPlan, "SNAPSHOT_BIND_FAILED"))
		return controller.failAfterRestore(cause, restartErr)
	}
	plan = boundPlan
	binding, err := planBinding(plan)
	if err != nil {
		restartErr := restartCurrentDashboardAfterActivationFailure(
			knowledgeRoot, port, wasRunning && dashboardService == "auto",
		)
		cause := errors.Join(err, upgradepkg.AbortPrepared(plan, "PLAN_BINDING_FAILED"))
		return controller.failAfterRestore(cause, restartErr)
	}
	launchOptions, clearCapability, err := controller.launchOptions(
		binding, maintenance.OutcomeSucceeded, maintenance.OutcomeRolledBack,
	)
	if err != nil {
		restartErr := restartCurrentDashboardAfterActivationFailure(
			knowledgeRoot, port, wasRunning && dashboardService == "auto",
		)
		cause := errors.Join(err, upgradepkg.AbortPrepared(plan, "CANARY_BINDING_FAILED"))
		return controller.failAfterRestore(cause, restartErr)
	}
	defer clearCapability()
	if err := upgradepkg.Launch(plan, os.Getpid(), launchOptions); err != nil {
		restartErr := restartCurrentDashboardAfterActivationFailure(
			knowledgeRoot, port, wasRunning && dashboardService == "auto",
		)
		return controller.failAfterRestore(err, restartErr)
	}
	return commandContext.write(map[string]any{
		"status": "ACTIVATION_PENDING", "current_version": buildinfo.Version,
		"latest_version": release.Version, "restart_required": wasRunning,
	})
}

func (commandContext commandContext) applyRollback(runtimeRoot, codexRoot, knowledgeRoot string, port int, dashboardService string) error {
	wasRunning := dashboard.Probe(port).Status == "RUNNING"
	return commandContext.applyRollbackWithState(
		runtimeRoot, codexRoot, knowledgeRoot, port,
		wasRunning, wasRunning && dashboardService == "auto",
	)
}

func (commandContext commandContext) applyRollbackWithState(
	runtimeRoot, codexRoot, knowledgeRoot string,
	port int,
	wasRunning, restartDashboard bool,
) error {
	active, binary, err := runtimeentry.ReadActive(runtimeRoot)
	if err != nil {
		return err
	}
	plan, err := upgradepkg.PrepareRollback(context.Background(), upgradepkg.RollbackOptions{
		RuntimeRoot: runtimeRoot, CodexRoot: codexRoot, KnowledgeRoot: knowledgeRoot,
		CurrentVersion: active.Version, CurrentBinary: binary, Port: port,
		RestartDashboard: restartDashboard,
	})
	if err != nil {
		return err
	}
	binding, err := rollbackBinding(plan)
	if err != nil {
		return errors.Join(err, upgradepkg.AbortPreparedRollback(plan, "PLAN_BINDING_FAILED"))
	}
	controller, err := beginUpgradeMaintenance(context.Background(), binding, "ROLLBACK_V070_ACTIVATION")
	if err != nil {
		return errors.Join(err, upgradepkg.AbortPreparedRollback(plan, "MAINTENANCE_ADMISSION_FAILED"))
	}
	if err := controller.beginMutation(true); err != nil {
		return controller.fail(errors.Join(err, upgradepkg.AbortPreparedRollback(plan, "MAINTENANCE_MUTATION_FAILED")))
	}
	if wasRunning {
		status := dashboard.StopService(knowledgeRoot, port)
		if status.Status == "FAILED" {
			cause := errors.New("dashboard stop failed before rollback activation")
			return controller.fail(errors.Join(cause, upgradepkg.AbortPreparedRollback(plan, "DASHBOARD_STOP_FAILED")))
		}
	}
	launchOptions, clearCapability, err := controller.launchOptions(
		binding, maintenance.OutcomeRolledBack, maintenance.OutcomeFailedSafe,
	)
	if err != nil {
		restartErr := restartDashboardAfterActivationFailure(
			binary, knowledgeRoot, port, restartDashboard,
		)
		cause := errors.Join(err, upgradepkg.AbortPreparedRollback(plan, "CANARY_BINDING_FAILED"))
		return controller.failAfterRestore(cause, restartErr)
	}
	defer clearCapability()
	if err := upgradepkg.LaunchRollback(plan, os.Getpid(), launchOptions); err != nil {
		restartErr := restartDashboardAfterActivationFailure(
			binary, knowledgeRoot, port, restartDashboard,
		)
		return controller.failAfterRestore(err, restartErr)
	}
	return commandContext.write(map[string]any{
		"status": "ROLLBACK_PENDING", "current_version": plan.CurrentVersion,
		"target_version": plan.TargetVersion, "restart_required": restartDashboard,
	})
}

func upgradeRoots(runtimeValue, codexValue, knowledgeValue string) (string, string, string, error) {
	runtimeRoot := runtimeValue
	var err error
	if runtimeRoot == "" {
		runtimeRoot, err = platform.RuntimeRoot()
	} else {
		runtimeRoot, err = filepath.Abs(runtimeRoot)
	}
	if err != nil {
		return "", "", "", err
	}
	codexRoot, err := platform.CodexRoot(codexValue)
	if err != nil {
		return "", "", "", err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(knowledgeValue)
	if err != nil {
		return "", "", "", err
	}
	return filepath.Clean(runtimeRoot), filepath.Clean(codexRoot), filepath.Clean(knowledgeRoot), nil
}

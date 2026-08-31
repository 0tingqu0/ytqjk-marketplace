package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
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
	if wasRunning {
		status := dashboard.StopService(knowledgeRoot, port)
		if status.Status == "FAILED" {
			return errors.New("dashboard stop failed before upgrade activation")
		}
	}
	if err := upgradepkg.Launch(plan, os.Getpid()); err != nil {
		if wasRunning && dashboardService == "auto" {
			binary, binaryErr := os.Executable()
			assets, assetsErr := resolveAssets("", "dashboard")
			if binaryErr == nil && assetsErr == nil {
				_ = dashboard.StartService(binary, knowledgeRoot, assets, port)
			}
		}
		return err
	}
	return commandContext.write(map[string]any{
		"status": "ACTIVATION_PENDING", "current_version": buildinfo.Version,
		"latest_version": release.Version, "restart_required": wasRunning,
	})
}

func (commandContext commandContext) activateUpgrade(arguments []string) error {
	flags := quietFlags("upgrade activate")
	planPath := flags.String("plan", "", "prepared upgrade plan")
	parentPID := flags.Int("parent-pid", 0, "parent process to wait for")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	if !filepath.IsAbs(*planPath) || *parentPID < 0 {
		return errors.New("upgrade activation arguments are invalid")
	}
	if err := upgradepkg.WaitForParent(*parentPID, 45*time.Second); err != nil {
		return err
	}
	result, err := upgradepkg.Activate(context.Background(), *planPath)
	if writeErr := commandContext.write(result); writeErr != nil {
		return writeErr
	}
	return err
}

func (commandContext commandContext) applyRollback(runtimeRoot, codexRoot, knowledgeRoot string, port int, dashboardService string) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	wasRunning := dashboard.Probe(port).Status == "RUNNING"
	plan, err := upgradepkg.PrepareRollback(context.Background(), upgradepkg.RollbackOptions{
		RuntimeRoot: runtimeRoot, CodexRoot: codexRoot, KnowledgeRoot: knowledgeRoot,
		CurrentVersion: buildinfo.Version, CurrentBinary: binary, Port: port,
		RestartDashboard: wasRunning && dashboardService == "auto",
	})
	if err != nil {
		return err
	}
	if wasRunning {
		status := dashboard.StopService(knowledgeRoot, port)
		if status.Status == "FAILED" {
			return errors.New("dashboard stop failed before rollback activation")
		}
	}
	if err := upgradepkg.LaunchRollback(plan, os.Getpid()); err != nil {
		if wasRunning && dashboardService == "auto" {
			assets, assetsErr := resolveAssets("", "dashboard")
			if assetsErr == nil {
				_ = dashboard.StartService(binary, knowledgeRoot, assets, port)
			}
		}
		return err
	}
	return commandContext.write(map[string]any{
		"status": "ROLLBACK_PENDING", "current_version": plan.CurrentVersion,
		"target_version": plan.TargetVersion, "restart_required": wasRunning,
	})
}

func (commandContext commandContext) activateRollback(arguments []string) error {
	flags := quietFlags("upgrade rollback-activate")
	planPath := flags.String("plan", "", "prepared rollback plan")
	parentPID := flags.Int("parent-pid", 0, "parent process to wait for")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	if !filepath.IsAbs(*planPath) || *parentPID < 0 {
		return errors.New("rollback activation arguments are invalid")
	}
	if err := upgradepkg.WaitForParent(*parentPID, 45*time.Second); err != nil {
		return err
	}
	result, err := upgradepkg.Rollback(context.Background(), *planPath)
	if writeErr := commandContext.write(result); writeErr != nil {
		return writeErr
	}
	return err
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

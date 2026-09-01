package cli

import (
	stdcontext "context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/importer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/install"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/runtimeentry"
)

func (context commandContext) install(arguments []string) int {
	flags := quietFlags("install")
	mode := flags.String("mode", "all", "all, codex-only, ide-only, or knowledge-only")
	sourceValue := flags.String("source-root", "", "distribution source root")
	targetValue := flags.String("target-root", "", "IDE skill root")
	projectValue := flags.String("project-root", "", "project to bootstrap")
	codexValue := flags.String("codex-root", "", "Codex home")
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	codexImport := flags.String("codex-import", "auto", "auto, off, or force")
	projectBootstrap := flags.String("project-bootstrap", "auto", "auto or off")
	dashboardService := flags.String("dashboard-service", "auto", "auto or off")
	apply := flags.Bool("apply", false, "apply changes")
	yes := flags.Bool("yes", false, "confirm mutation")
	uninstall := flags.Bool("uninstall", false, "uninstall managed components")
	jsonOutput := flags.Bool("json", false, "emit JSON receipt")
	healthOnly := flags.Bool("health", false, "show local dependency health")
	probeLocal := flags.Bool("probe-local", false, "probe local executables")
	vectorMode := flags.String("vector", "auto", "off, auto, or on")
	knowledgeBytes := flags.Int("knowledge-bytes", 0, "planning input")
	knowledgeChunks := flags.Int("knowledge-chunks", 0, "planning input")
	vectorFailed := flags.Bool("vector-failed", false, "record vector failure")
	version := flags.Bool("version", false, "show version")
	if err := flags.Parse(arguments); err != nil {
		writeFailure(context.out, err)
		return 2
	}
	if len(flags.Args()) > 0 {
		writeFailure(context.out, fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " ")))
		return 2
	}
	if *version {
		fmt.Fprintln(context.out, buildinfo.Version)
		return 0
	}
	if !oneOf(*codexImport, "auto", "off", "force") || !oneOf(*projectBootstrap, "auto", "off") || !oneOf(*dashboardService, "auto", "off") || !oneOf(*vectorMode, "auto", "off", "on") {
		writeFailure(context.out, errors.New("invalid installer option"))
		return 2
	}
	if *apply && (!*yes || *targetValue == "") {
		writeFailure(context.out, errors.New("--apply requires --yes and --target-root"))
		return 2
	}
	codexRoot, err := platform.CodexRoot(*codexValue)
	if err != nil {
		writeFailure(context.out, err)
		return 2
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		writeFailure(context.out, err)
		return 2
	}
	runtimeRoot, err := platform.RuntimeRoot()
	if err != nil {
		writeFailure(context.out, err)
		return 2
	}
	sourceRoot, err := platform.SourceRoot(*sourceValue)
	if err != nil && !*uninstall {
		writeFailure(context.out, err)
		return 2
	}
	if sourceRoot == "" {
		sourceRoot, _ = os.Getwd()
	}
	target := *targetValue
	planTarget := target
	if planTarget == "" {
		planTarget = "<target-root>"
	}
	normalizedMode, err := install.NormalizeUpdateMode(*mode, target, *codexImport, *projectBootstrap, *dashboardService)
	if err != nil {
		writeFailure(context.out, err)
		return 2
	}
	var plan install.Plan
	if *uninstall {
		plan, err = install.BuildUninstallPlan(normalizedMode, planTarget)
	} else {
		plan, err = install.BuildPlan(normalizedMode, sourceRoot, planTarget)
	}
	if err != nil {
		writeFailure(context.out, err)
		return 2
	}
	health := install.Health(*probeLocal || *healthOnly)
	if *healthOnly {
		_ = context.write(map[string]any{"ok": true, "version": buildinfo.Version, "health": health})
		return 0
	}
	operation := "install"
	if *uninstall {
		operation = "uninstall"
	}
	receipt := install.BaseReceipt(plan, target, *apply, health, install.VectorResult(*vectorMode, *knowledgeBytes, *knowledgeChunks, *vectorFailed), operation)
	receipt["codex_import"] = importer.Empty("NOT_RUN")
	receipt["project_bootstrap"] = rag.EmptyBootstrapReceipt("NOT_RUN")
	receipt["dashboard_service"] = dashboard.ServiceStatus{Status: "NOT_RUN", Port: dashboard.DefaultPort, Autostart: "NOT_CONFIGURED"}
	receipt["guidance"] = install.GuidanceResult{Status: "NOT_RUN"}
	receipt["runtime"] = install.RuntimeResult{Status: "NOT_RUN"}
	receipt["maintenance"] = map[string]any{"status": "NOT_RUN"}
	if !*apply {
		receipt["apply"] = map[string]any{"status": "PLANNED", "changed": false}
		receipt["runtime"] = install.RuntimeResult{Status: "PLANNED"}
		receipt["maintenance"] = map[string]any{"status": "PLANNED"}
		return context.emitInstall(receipt, *jsonOutput, 0)
	}
	binary, err := os.Executable()
	if err != nil {
		receipt["apply"] = map[string]any{"status": "FAILED", "error": safeError(err)}
		return context.emitInstall(receipt, *jsonOutput, 2)
	}
	if *uninstall {
		if err := install.ValidateRuntimeUninstall(runtimeRoot, binary); err != nil {
			receipt["runtime"] = install.RuntimeResult{Status: "FAILED", Rollback: "NOT_NEEDED"}
			receipt["apply"] = map[string]any{"status": "NOT_RUN", "error": safeError(err)}
			return context.emitInstall(receipt, *jsonOutput, 2)
		}
		receipt["dashboard_service"] = dashboard.Probe(dashboard.DefaultPort)
	}
	maintenanceController, err := beginInstallMaintenance(
		stdcontext.Background(), runtimeRoot, codexRoot, knowledgeRoot, target, operation,
	)
	if err != nil {
		receipt["maintenance"] = map[string]any{"status": "FAILED", "error": safeError(err)}
		receipt["apply"] = map[string]any{"status": "NOT_RUN"}
		return context.emitInstall(receipt, *jsonOutput, 2)
	}
	if err := maintenanceController.beginMutation(); err != nil {
		receipt["apply"] = map[string]any{"status": "NOT_RUN", "error": safeError(err)}
		return context.emitInstallMaintenance(
			receipt, *jsonOutput, 2, maintenanceController, maintenance.OutcomeAborted,
		)
	}
	if *uninstall {
		serviceStatus := dashboard.RemoveService(knowledgeRoot, dashboard.DefaultPort)
		receipt["dashboard_service"] = serviceStatus
		if serviceStatus.Status == "FAILED" {
			receipt["apply"] = map[string]any{"status": "NOT_RUN", "error": "dashboard removal failed"}
			return context.emitInstallMaintenance(
				receipt, *jsonOutput, 2, maintenanceController, maintenance.OutcomeFailedSafe,
			)
		}
	}
	runtimeBinary := binary
	runtimeResult := install.RuntimeResult{Status: "PRESERVED"}
	if !*uninstall {
		runtimeResult, err = install.InstallRuntime(runtimeRoot, binary, buildinfo.Version)
		receipt["runtime"] = runtimeResult
		if err != nil {
			receipt["apply"] = map[string]any{"status": "NOT_RUN", "error": safeError(err)}
			return context.emitInstallMaintenance(
				receipt, *jsonOutput, 2, maintenanceController, maintenance.OutcomeFailedSafe,
			)
		}
		runtimeBinary = runtimeentry.LauncherPath(runtimeRoot)
	} else {
		receipt["runtime"] = runtimeResult
	}
	options := install.ApplyOptions{
		Plan: plan, Target: target, SourceRoot: sourceRoot, CodexRoot: codexRoot, Binary: runtimeBinary,
	}
	var applyResult install.ApplyResult
	if *uninstall {
		applyResult, err = install.Uninstall(options)
	} else {
		applyResult, err = install.Apply(options)
	}
	if err != nil {
		failure := map[string]any{"status": "FAILED", "error": safeError(err)}
		var applyError *install.ApplyError
		if errors.As(err, &applyError) {
			failure["rollback"] = applyError.Rollback
			failure["failed_action"] = applyError.FailedAction
			failure["failed_compensations"] = applyError.FailedCompensations
		}
		if !*uninstall && runtimeResult.Changed {
			rollbackErr := install.RollbackFreshRuntime(runtimeRoot, runtimeResult)
			runtimeResult.Status = "ROLLED_BACK"
			runtimeResult.Rollback = "SUCCEEDED"
			if rollbackErr != nil {
				runtimeResult.Status = "FAILED"
				runtimeResult.Rollback = "FAILED"
				failure["runtime_rollback_error"] = safeError(rollbackErr)
			}
			receipt["runtime"] = runtimeResult
		}
		receipt["apply"] = failure
		return context.emitInstallMaintenance(
			receipt, *jsonOutput, 2, maintenanceController, maintenance.OutcomeFailedSafe,
		)
	}
	receipt["apply"] = applyResult
	guidanceAction := "install"
	if *uninstall {
		guidanceAction = "remove"
	}
	if normalizedMode == "all" || normalizedMode == "codex-only" || normalizedMode == "codex-stable-only" {
		receipt["guidance"] = install.ConfigureGuidance(codexRoot, knowledgeRoot, normalizedMode, guidanceAction, runtimeBinary)
	}
	if *uninstall {
		if guidance := receipt["guidance"].(install.GuidanceResult); guidance.Status == "FAILED" {
			return context.emitInstallMaintenance(
				receipt, *jsonOutput, 6, maintenanceController, maintenance.OutcomeFailedSafe,
			)
		}
		runtimeResult, runtimeErr := install.UninstallRuntime(runtimeRoot, binary)
		receipt["runtime"] = runtimeResult
		if runtimeErr != nil {
			receipt["runtime_error"] = safeError(runtimeErr)
			return context.emitInstallMaintenance(
				receipt, *jsonOutput, 2, maintenanceController, maintenance.OutcomeFailedSafe,
			)
		}
		return context.emitInstallMaintenance(
			receipt, *jsonOutput, 0, maintenanceController, maintenance.OutcomeSucceeded,
		)
	}
	exitCode := 0
	startDashboard := false
	if *codexImport != "off" {
		importReceipt, importErr := importer.Import(codexRoot, knowledgeRoot, *codexImport)
		receipt["codex_import"] = importReceipt
		if importErr != nil {
			receipt["codex_import_error"] = safeError(importErr)
			exitCode = 3
		}
	} else {
		receipt["codex_import"] = importer.Empty("SKIPPED_OFF")
	}
	if *projectBootstrap == "auto" {
		if *projectValue == "" {
			receipt["project_bootstrap"] = rag.EmptyBootstrapReceipt("SKIPPED_NO_PROJECT")
		} else if result, bootstrapErr := rag.Bootstrap(knowledgeRoot, *projectValue, *vectorMode); bootstrapErr != nil {
			receipt["project_bootstrap"] = rag.EmptyBootstrapReceipt("FAILED")
			receipt["project_bootstrap_error"] = safeError(bootstrapErr)
			if exitCode == 0 {
				exitCode = 4
			}
		} else {
			receipt["project_bootstrap"] = rag.BootstrapReceipt(result)
		}
	} else {
		receipt["project_bootstrap"] = rag.EmptyBootstrapReceipt("SKIPPED_OFF")
	}
	if *dashboardService == "auto" {
		assets := filepath.Join(sourceRoot, "plugins", "ytqjk-agentic-orchestrator", "skills", "ytqjk", "dashboard")
		status := dashboard.ConfigureService(runtimeBinary, knowledgeRoot, assets, dashboard.DefaultPort)
		receipt["dashboard_service"] = status
		if status.Status == "FAILED" && exitCode == 0 {
			exitCode = 5
		} else if status.Status == "CONFIGURED" {
			startDashboard = true
		}
	} else {
		receipt["dashboard_service"] = dashboard.ServiceStatus{Status: "SKIPPED_OFF", Port: dashboard.DefaultPort, Autostart: "NOT_CONFIGURED"}
	}
	if guidance := receipt["guidance"].(install.GuidanceResult); guidance.Status == "FAILED" && exitCode == 0 {
		exitCode = 6
	}
	maintenanceReceipt, maintenanceErr := maintenanceController.complete(maintenance.OutcomeSucceeded)
	if maintenanceErr != nil {
		receipt["maintenance"] = map[string]any{"status": "FAILED", "error": safeError(maintenanceErr)}
		return context.emitInstall(receipt, *jsonOutput, 2)
	}
	receipt["maintenance"] = map[string]any{"status": "SUCCEEDED", "receipt": maintenanceReceipt}
	if startDashboard {
		status := dashboard.StartConfiguredService(dashboard.DefaultPort)
		receipt["dashboard_service"] = status
		if status.Status == "FAILED" && exitCode == 0 {
			exitCode = 5
		}
	}
	return context.emitInstall(receipt, *jsonOutput, exitCode)
}

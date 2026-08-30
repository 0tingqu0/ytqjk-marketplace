package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/importer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/install"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
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
	if !*apply {
		receipt["apply"] = map[string]any{"status": "PLANNED", "changed": false}
		return context.emitInstall(receipt, *jsonOutput, 0)
	}
	binary, err := os.Executable()
	if err != nil {
		receipt["apply"] = map[string]any{"status": "FAILED", "error": safeError(err)}
		return context.emitInstall(receipt, *jsonOutput, 2)
	}
	options := install.ApplyOptions{Plan: plan, Target: target, SourceRoot: sourceRoot, CodexRoot: codexRoot, Binary: binary}
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
		receipt["apply"] = failure
		return context.emitInstall(receipt, *jsonOutput, 2)
	}
	receipt["apply"] = applyResult
	guidanceAction := "install"
	if *uninstall {
		guidanceAction = "remove"
	}
	if normalizedMode == "all" || normalizedMode == "codex-only" || normalizedMode == "codex-stable-only" {
		receipt["guidance"] = install.ConfigureGuidance(codexRoot, knowledgeRoot, normalizedMode, guidanceAction, binary)
	}
	if *uninstall {
		return context.emitInstall(receipt, *jsonOutput, 0)
	}
	exitCode := 0
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
		status := dashboard.StartService(binary, knowledgeRoot, assets, dashboard.DefaultPort)
		receipt["dashboard_service"] = status
		if status.Status == "FAILED" && exitCode == 0 {
			exitCode = 5
		}
	} else {
		receipt["dashboard_service"] = dashboard.ServiceStatus{Status: "SKIPPED_OFF", Port: dashboard.DefaultPort, Autostart: "NOT_CONFIGURED"}
	}
	if guidance := receipt["guidance"].(install.GuidanceResult); guidance.Status == "FAILED" && exitCode == 0 {
		exitCode = 6
	}
	return context.emitInstall(receipt, *jsonOutput, exitCode)
}

func (context commandContext) emitInstall(receipt map[string]any, asJSON bool, exitCode int) int {
	if asJSON {
		_ = context.write(receipt)
	} else {
		fmt.Fprintln(context.out, install.SummaryText(receipt))
	}
	return exitCode
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

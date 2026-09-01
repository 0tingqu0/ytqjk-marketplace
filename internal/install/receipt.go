package install

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"sort"
	"strings"
)

const (
	VectorLimitBytes  = 10 * 1024 * 1024
	VectorLimitChunks = 2000
)

func VectorResult(mode string, size, chunks int, failed bool) string {
	if failed {
		return "lexical-only"
	}
	if mode == "off" {
		return "off"
	}
	return "go-hybrid"
}

func Health(probe bool) map[string]string {
	checks := map[string]string{}
	for _, name := range []string{"go", "node", "npm", "codex"} {
		status := "UNKNOWN"
		if probe {
			if _, err := exec.LookPath(name); err == nil {
				status = "AVAILABLE"
			} else {
				status = "MISSING"
			}
		}
		checks[name] = status
	}
	for _, name := range []string{"core_task_api", "plugin_discovery", "skill_discovery", "knowledge_service", "loopback_workbench", "vector"} {
		checks[name] = "UNKNOWN"
	}
	checks["vector"] = "BUILTIN_GO"
	return checks
}

func BaseReceipt(plan Plan, target string, applied bool, health map[string]string, vector, operation string) map[string]any {
	actions := plan.Actions
	if actions == nil {
		actions = []Action{}
	}
	copies := make([]string, 0, len(plan.Copies))
	for _, item := range plan.Copies {
		copies = append(copies, item.Name)
	}
	removals := make([]string, 0, len(plan.Removals))
	for _, path := range plan.Removals {
		parts := strings.FieldsFunc(path, func(r rune) bool { return r == '/' || r == '\\' })
		if len(parts) > 0 {
			removals = append(removals, parts[len(parts)-1])
		}
	}
	configured := "NOT_CONFIGURED"
	if target != "" {
		configured = "CONFIGURED"
	}
	return map[string]any{
		"schema": "ytqjk-install-receipt/v1", "version": Version,
		"operation": operation, "mode": plan.Mode, "dry_run": !applied,
		"target_root": configured, "actions": actions, "copies": copies,
		"removals": removals, "grill_me_present": HasGrill(target), "health": health,
		"vector": vector, "platform": runtime.GOOS,
		"sqlite_note": "SQLite caches are not shared across Windows, Linux, WSL.",
	}
}

func JSONText(value any) string {
	data, _ := json.Marshal(value)
	return string(data)
}

func SummaryText(value map[string]any) string {
	version := text(value["version"], "unknown")
	operation := text(value["operation"], "install")
	dryRun, _ := value["dry_run"].(bool)
	applyStatus := nestedStatus(value["apply"])
	statuses := []string{
		nestedStatus(value["project_bootstrap"]), nestedStatus(value["codex_import"]),
		nestedStatus(value["guidance"]), nestedStatus(value["dashboard_service"]),
		nestedStatus(value["runtime"]), nestedStatus(value["maintenance"]),
	}
	expected := "APPLIED"
	if operation == "uninstall" {
		expected = "UNINSTALLED"
	}
	failed := !dryRun && applyStatus != expected
	for _, status := range statuses {
		if status == "FAILED" {
			failed = true
		}
	}
	if !dryRun && statuses[5] != "SUCCEEDED" {
		failed = true
	}
	if !dryRun && operation == "install" && statuses[4] != "ACTIVE" {
		failed = true
	}
	if !dryRun && operation == "uninstall" && statuses[4] != "REMOVED" && statuses[4] != "ABSENT" {
		failed = true
	}
	warned := statuses[1] == "SUCCEEDED_WITH_WARNINGS"
	headline := "安装完成"
	if dryRun {
		headline = "安装预览"
		if operation == "uninstall" {
			headline = "卸载预览"
		}
	} else if failed {
		headline = "安装未完全成功"
		if operation == "uninstall" {
			headline = "卸载未完全成功"
		}
	} else if warned {
		headline = "安装完成（有警告）"
	} else if operation == "uninstall" {
		headline = "卸载完成"
	}
	lines := []string{fmt.Sprintf("YTQJK v%s：%s", version, headline)}
	if dryRun {
		count := 0
		if actions, ok := value["actions"].([]Action); ok {
			count = len(actions)
		}
		lines = append(lines, fmt.Sprintf("- 计划操作：%d 项（尚未写入）", count))
		return strings.Join(lines, "\n")
	}
	if applyStatus == "APPLIED" {
		lines = append(lines, "- 插件与技能：已安装或更新")
	} else if applyStatus == "UNINSTALLED" {
		lines = append(lines, "- 插件与技能：已卸载")
	} else {
		lines = append(lines, "- 插件与技能：失败")
	}
	appendStatusLine(&lines, value["project_bootstrap"], map[string]string{
		"SUCCEEDED": "- 知识库：就绪", "FAILED": "- 知识库：初始化失败", "NOT_CONFIGURED": "- 知识库：未配置项目索引",
	})
	appendStatusLine(&lines, value["codex_import"], map[string]string{
		"SUCCEEDED": "- 资料导入：成功", "SUCCEEDED_WITH_WARNINGS": "- 资料导入：成功（有警告）", "FAILED": "- 资料导入：失败",
	})
	appendStatusLine(&lines, value["guidance"], map[string]string{
		"INSTALLED": "- 新会话接入：已配置", "REMOVED": "- 新会话接入：已移除", "FAILED": "- 新会话接入：配置失败",
	})
	appendStatusLine(&lines, value["dashboard_service"], map[string]string{
		"RUNNING": "- 后台网页：运行中（http://127.0.0.1:8765）", "STOPPED": "- 后台网页：已停止", "FAILED": "- 后台网页：启动失败",
	})
	appendStatusLine(&lines, value["runtime"], map[string]string{
		"ACTIVE": "- Go runtime：已激活", "REMOVED": "- Go runtime：已卸载",
		"ABSENT": "- Go runtime：无需卸载", "FAILED": "- Go runtime：失败",
	})
	appendStatusLine(&lines, value["maintenance"], map[string]string{
		"SUCCEEDED": "- 维护窗口：已安全关闭", "FAILED": "- 维护窗口：状态未知或失败",
	})
	if operation == "uninstall" {
		lines = append(lines, "- 知识库数据：保留")
	}
	if failed || warned {
		lines = append(lines, "详情：重新运行安装命令并追加 --json")
	}
	return strings.Join(lines, "\n")
}

func sortedKeys(value map[string]string) []string {
	keys := make([]string, 0, len(value))
	for key := range value {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func text(value any, fallback string) string {
	if result, ok := value.(string); ok && result != "" {
		return result
	}
	return fallback
}

func nestedStatus(value any) string {
	if item, ok := value.(map[string]any); ok {
		return text(item["status"], "UNKNOWN")
	}
	data, err := json.Marshal(value)
	if err == nil {
		var item map[string]any
		if json.Unmarshal(data, &item) == nil {
			return text(item["status"], "UNKNOWN")
		}
	}
	return "UNKNOWN"
}

func appendStatusLine(lines *[]string, value any, choices map[string]string) {
	if line := choices[nestedStatus(value)]; line != "" {
		*lines = append(*lines, line)
	}
}

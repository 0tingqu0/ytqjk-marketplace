package install

import (
	"strings"
	"testing"
)

func TestSummaryTextUsesCanonicalInstallReceiptFields(t *testing.T) {
	receipt := map[string]any{
		"version": Version, "operation": "install", "dry_run": false,
		"apply":             map[string]any{"status": "APPLIED"},
		"project_bootstrap": map[string]any{"status": "SUCCEEDED"},
		"codex_import":      map[string]any{"status": "SUCCEEDED"},
		"guidance":          map[string]any{"status": "INSTALLED"},
		"dashboard_service": map[string]any{"status": "RUNNING"},
		"runtime":           map[string]any{"status": "ACTIVE"},
		"maintenance":       map[string]any{"status": "SUCCEEDED"},
	}
	text := SummaryText(receipt)
	for _, expected := range []string{
		"安装完成", "知识库：就绪", "资料导入：成功", "新会话接入：已配置",
		"Go runtime：已激活", "维护窗口：已安全关闭",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("summary %q does not contain %q", text, expected)
		}
	}
}

func TestSummaryTextFailsClosedOnRuntimeOrMaintenanceFailure(t *testing.T) {
	for _, field := range []string{"runtime", "maintenance"} {
		t.Run(field, func(t *testing.T) {
			receipt := map[string]any{
				"version": Version, "operation": "install", "dry_run": false,
				"apply":       map[string]any{"status": "APPLIED"},
				"runtime":     map[string]any{"status": "ACTIVE"},
				"maintenance": map[string]any{"status": "SUCCEEDED"},
			}
			receipt[field] = map[string]any{"status": "FAILED"}
			if text := SummaryText(receipt); !strings.Contains(text, "安装未完全成功") {
				t.Fatalf("summary=%q", text)
			}
		})
	}
}

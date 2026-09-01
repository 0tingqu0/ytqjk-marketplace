package upgrade

import (
	"context"
	"testing"
)

func TestConfigureDashboardRequiresOneSuccessfulHook(t *testing.T) {
	configuration := DashboardActivation{
		RuntimeRoot: "runtime", CodexRoot: "codex", KnowledgeRoot: "knowledge",
		Version: "0.7.0", Port: 8765,
	}
	if err := configureDashboard(context.Background(), nil, configuration); errorCodeOf(err) != "UPGRADE_DASHBOARD_CONFIGURATION_FAILED" {
		t.Fatalf("missing hook error=%v", err)
	}
	called := false
	err := configureDashboard(context.Background(), []ActivationHooks{{
		ConfigureDashboard: func(_ context.Context, actual DashboardActivation) error {
			called = true
			if actual != configuration {
				t.Fatalf("configuration=%#v", actual)
			}
			return nil
		},
	}}, configuration)
	if err != nil || !called {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

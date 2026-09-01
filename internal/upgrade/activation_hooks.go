package upgrade

import (
	"context"
	"errors"
)

// DashboardActivation describes the service registration that must follow the
// currently active runtime generation. Configuration is allowed while writes
// are fenced; starting the service is deliberately left to the caller after
// the maintenance canary durably reopens admission.
type DashboardActivation struct {
	RuntimeRoot   string
	CodexRoot     string
	KnowledgeRoot string
	Version       string
	Port          int
}

type ActivationHooks struct {
	ConfigureDashboard func(context.Context, DashboardActivation) error
}

func configureDashboard(
	ctx context.Context,
	hooks []ActivationHooks,
	configuration DashboardActivation,
) error {
	if len(hooks) != 1 || hooks[0].ConfigureDashboard == nil {
		return failure("UPGRADE_DASHBOARD_CONFIGURATION_FAILED", errors.New("dashboard configuration hook is unavailable"))
	}
	if err := hooks[0].ConfigureDashboard(ctx, configuration); err != nil {
		return failure("UPGRADE_DASHBOARD_CONFIGURATION_FAILED", err)
	}
	return nil
}

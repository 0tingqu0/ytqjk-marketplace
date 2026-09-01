package upgrade

import (
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func CanRollback(state State, currentVersion string) bool {
	return (state.Status == "ACTIVE" || state.Status == "FAILED") &&
		state.CurrentVersion == currentVersion && state.PreviousVersion != "" &&
		state.PreviousVersion != state.CurrentVersion && hexDigestPattern.MatchString(state.SnapshotID) &&
		hexDigestPattern.MatchString(state.SnapshotManifestSHA256)
}

func rollbackPlanPath(plan RollbackPlan) string {
	return filepath.Join(plan.StageRoot, "rollback-plan.json")
}

func validateRollbackPlan(plan RollbackPlan, path string) error {
	if plan.Schema != rollbackPlanSchema || !hexDigestPattern.MatchString(plan.ID) ||
		!hexDigestPattern.MatchString(plan.TargetSnapshotID) ||
		!hexDigestPattern.MatchString(plan.TargetSnapshotManifestSHA256) ||
		!hexDigestPattern.MatchString(plan.TargetBinarySHA256) ||
		!hexDigestPattern.MatchString(plan.BinarySHA256) || plan.Port < 1 || plan.Port > 65535 {
		return failure("UPGRADE_PLAN_INVALID", nil)
	}
	if _, err := parseVersion(plan.CurrentVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := parseVersion(plan.TargetVersion); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	for _, root := range []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot, plan.StageRoot} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return failure("UPGRADE_PLAN_INVALID", nil)
		}
	}
	if _, err := restorePlanRoots(Plan{
		RuntimeRoot: plan.RuntimeRoot, CodexRoot: plan.CodexRoot, KnowledgeRoot: plan.KnowledgeRoot,
	}); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	upgradeRoot := filepath.Join(plan.RuntimeRoot, "upgrade")
	if _, err := safeio.Contained(upgradeRoot, plan.StageRoot); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	if _, err := safeio.Contained(plan.StageRoot, plan.BinaryPath); err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	expected, err := safeio.Contained(plan.StageRoot, path)
	if err != nil {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	absolute, err := filepath.Abs(path)
	if err != nil || expected != absolute || filepath.Clean(path) != absolute {
		return failure("UPGRADE_PLAN_INVALID", err)
	}
	return nil
}

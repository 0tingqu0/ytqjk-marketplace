package upgrade

import (
	"crypto/subtle"
	"os"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func rollbackPlanDigest(plan RollbackPlan, path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	var stored RollbackPlan
	if err := decodeStrictJSON(data, &stored); err != nil {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	if err := validateRollbackPlan(stored, path); err != nil || stored != plan {
		return "", failure("UPGRADE_PLAN_INVALID", err)
	}
	binaryHash, err := safeio.FileSHA256(plan.BinaryPath)
	if err != nil || subtle.ConstantTimeCompare([]byte(binaryHash), []byte(plan.BinarySHA256)) != 1 {
		return "", failure("ROLLBACK_HELPER_INVALID", err)
	}
	return safeio.SHA256(data), nil
}

func readAuthenticatedRollbackPlan(path, expectedSHA256 string) (RollbackPlan, error) {
	if !hexDigestPattern.MatchString(expectedSHA256) {
		return RollbackPlan{}, failure("UPGRADE_PLAN_INVALID", nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return RollbackPlan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	actualSHA256 := safeio.SHA256(data)
	if subtle.ConstantTimeCompare([]byte(actualSHA256), []byte(expectedSHA256)) != 1 {
		return RollbackPlan{}, failure("UPGRADE_PLAN_INVALID", nil)
	}
	var plan RollbackPlan
	if err := decodeStrictJSON(data, &plan); err != nil {
		return RollbackPlan{}, failure("UPGRADE_PLAN_INVALID", err)
	}
	return plan, validateRollbackPlan(plan, path)
}

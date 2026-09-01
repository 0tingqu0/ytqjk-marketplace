package cli

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	upgradepkg "github.com/0tingqu0/ytqjk-marketplace/internal/upgrade"
)

const (
	maintenanceControlRootEnv = "YTQJK_MAINTENANCE_CONTROL_ROOT"
	maintenanceOperationIDEnv = "YTQJK_MAINTENANCE_OPERATION_ID"
	maintenanceGenerationEnv  = "YTQJK_MAINTENANCE_GENERATION"
	maintenanceCapabilityEnv  = "YTQJK_MAINTENANCE_CAPABILITY"
)

type upgradeMaintenanceController struct {
	lease       *maintenance.Lease
	controlRoot string
	mutated     bool
	transferred bool
}

type activationCanaryClaim struct {
	lease *maintenance.CanaryLease
	ctx   context.Context
	scope maintenance.Scope
}

func beginUpgradeMaintenance(ctx context.Context, binding upgradepkg.ActivationBinding, purpose string) (*upgradeMaintenanceController, error) {
	controlRoot, err := maintenanceControlRoot()
	if err != nil {
		return nil, err
	}
	if err := maintenance.BootstrapControlRoot(ctx, controlRoot); err != nil {
		return nil, err
	}
	lease, err := maintenance.BeginExclusive(ctx, activationMaintenanceScope(controlRoot, binding), maintenance.ExclusiveOptions{
		OperationID: binding.OperationID, Purpose: purpose,
		Duration: maintenance.MaxExclusiveDuration, DrainTimeout: maintenance.MaxDrainTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &upgradeMaintenanceController{lease: lease, controlRoot: controlRoot}, nil
}

func (controller *upgradeMaintenanceController) beginMutation(restoring bool) error {
	if controller == nil || controller.lease == nil {
		return errors.New("upgrade maintenance controller is unavailable")
	}
	if err := controller.lease.BeginMutation(); err != nil {
		return err
	}
	controller.mutated = true
	if restoring {
		return controller.lease.BeginRestoring()
	}
	return nil
}

func (controller *upgradeMaintenanceController) fail(cause error) error {
	if controller == nil || controller.lease == nil || controller.transferred {
		return cause
	}
	outcome := maintenance.OutcomeAborted
	if controller.mutated {
		outcome = maintenance.OutcomeFailedSafe
	}
	_, completeErr := controller.lease.Complete(outcome)
	return errors.Join(cause, completeErr)
}

func (controller *upgradeMaintenanceController) failAfterRestore(cause, restoreErr error) error {
	if restoreErr == nil {
		return controller.fail(cause)
	}
	if controller == nil || controller.lease == nil || controller.transferred {
		return errors.Join(cause, restoreErr)
	}
	markErr := controller.lease.MarkRecoveryRequired(
		maintenance.CodeRecoveryRequired,
		"dashboard restoration failed after activation preparation",
	)
	return errors.Join(cause, restoreErr, markErr)
}

func (controller *upgradeMaintenanceController) launchOptions(
	binding upgradepkg.ActivationBinding,
	expected maintenance.Outcome,
	fallback maintenance.Outcome,
) (upgradepkg.LaunchOptions, func(), error) {
	if controller == nil || controller.lease == nil || !controller.mutated ||
		controller.lease.OperationID() != binding.OperationID {
		return upgradepkg.LaunchOptions{}, nil, errors.New("upgrade maintenance handoff is not ready")
	}
	capability := make([]byte, 32)
	if _, err := rand.Read(capability); err != nil {
		return upgradepkg.LaunchOptions{}, nil, fmt.Errorf("generate maintenance capability: %w", err)
	}
	encoded := base64.RawStdEncoding.EncodeToString(capability)
	options := upgradepkg.LaunchOptions{
		Environment: []string{
			maintenanceControlRootEnv + "=" + controller.controlRoot,
			maintenanceOperationIDEnv + "=" + binding.OperationID,
			maintenanceGenerationEnv + "=" + strconv.FormatUint(controller.lease.Generation(), 10),
			maintenanceCapabilityEnv + "=" + encoded,
		},
		Transfer: func(childPID int) error {
			committed, err := controller.lease.BeginReopeningResult(childPID, maintenance.CanaryOptions{
				Capability: capability, PlanSHA256: binding.PlanSHA256,
				SnapshotManifestSHA256: binding.SnapshotManifestSHA256,
				TargetBinarySHA256:     binding.TargetBinarySHA256, TargetVersion: binding.TargetVersion,
				Port: binding.Port, Attempt: 1, ExpectedOutcome: expected, FallbackOutcome: fallback,
				Deadline: controller.lease.ExpiresAt().Add(-maintenance.RecoveryReserve),
			})
			if committed {
				controller.transferred = true
				return nil
			}
			return err
		},
	}
	clear := func() {
		for index := range capability {
			capability[index] = 0
		}
		for index := range options.Environment {
			options.Environment[index] = ""
		}
		options.Environment = nil
	}
	return options, clear, nil
}

func activationMaintenanceScope(controlRoot string, binding upgradepkg.ActivationBinding) maintenance.Scope {
	return maintenance.Scope{
		ControlRoot: controlRoot, RuntimeRoot: binding.RuntimeRoot, CodexRoot: binding.CodexRoot,
		ProspectiveRoots: []string{binding.KnowledgeRoot},
	}
}

func claimActivationCanary(ctx context.Context, binding upgradepkg.ActivationBinding) (*activationCanaryClaim, error) {
	controlRoot, operationID, generation, capability, err := readMaintenanceHandoff()
	if err != nil {
		return nil, err
	}
	defer clearBytes(capability)
	if err := clearMaintenanceHandoffEnvironment(); err != nil {
		return nil, err
	}
	if operationID != binding.OperationID {
		return nil, errors.New("maintenance operation does not match activation plan")
	}
	scope := activationMaintenanceScope(controlRoot, binding)
	lease, err := maintenance.ClaimCanary(ctx, scope, operationID, generation, capability)
	if err != nil {
		return nil, err
	}
	canaryContext, err := maintenance.WithCanary(ctx, lease)
	if err != nil {
		return nil, err
	}
	return &activationCanaryClaim{lease: lease, ctx: canaryContext, scope: scope}, nil
}

func clearMaintenanceHandoffEnvironment() error {
	var result error
	for _, key := range []string{
		maintenanceControlRootEnv,
		maintenanceOperationIDEnv,
		maintenanceGenerationEnv,
		maintenanceCapabilityEnv,
	} {
		if err := os.Unsetenv(key); err != nil {
			result = errors.Join(result, fmt.Errorf("clear %s: %w", key, err))
		}
	}
	return result
}

func readMaintenanceHandoff() (string, string, uint64, []byte, error) {
	controlRoot, controlOK := os.LookupEnv(maintenanceControlRootEnv)
	operationID, operationOK := os.LookupEnv(maintenanceOperationIDEnv)
	generationValue, generationOK := os.LookupEnv(maintenanceGenerationEnv)
	capabilityValue, capabilityOK := os.LookupEnv(maintenanceCapabilityEnv)
	absoluteControl, err := filepath.Abs(controlRoot)
	if !controlOK || !operationOK || !generationOK || !capabilityOK || err != nil ||
		absoluteControl != filepath.Clean(controlRoot) {
		return "", "", 0, nil, errors.New("maintenance handoff environment is invalid")
	}
	generation, err := strconv.ParseUint(generationValue, 10, 64)
	if err != nil || generation == 0 {
		return "", "", 0, nil, errors.New("maintenance generation is invalid")
	}
	capability, err := base64.RawStdEncoding.Strict().DecodeString(capabilityValue)
	if err != nil || len(capability) != 32 {
		clearBytes(capability)
		return "", "", 0, nil, errors.New("maintenance capability is invalid")
	}
	return absoluteControl, operationID, generation, capability, nil
}

func completeActivationCanary(
	claim *activationCanaryClaim,
	binding upgradepkg.ActivationBinding,
	result upgradepkg.ActivateResult,
	outcome maintenance.Outcome,
	expected maintenance.Outcome,
	fallback maintenance.Outcome,
) error {
	if claim == nil || claim.lease == nil || claim.ctx == nil {
		return errors.New("maintenance canary claim is unavailable")
	}
	fence, err := maintenance.CanaryFenceFromContext(claim.ctx, claim.scope)
	if err != nil {
		return err
	}
	if fence.OperationID != binding.OperationID || fence.PlanSHA256 != binding.PlanSHA256 ||
		fence.SnapshotManifestSHA256 != binding.SnapshotManifestSHA256 ||
		fence.TargetBinarySHA256 != binding.TargetBinarySHA256 || fence.TargetVersion != binding.TargetVersion ||
		fence.Port != binding.Port || fence.ExpectedOutcome != expected || fence.FallbackOutcome != fallback {
		return errors.New("maintenance canary binding does not match activation plan")
	}
	state, finalStateSHA256, err := upgradepkg.ReadActivationStateEvidence(binding.RuntimeRoot, binding.OperationID)
	if err != nil {
		return err
	}
	if state.Status != result.Status || state.CurrentVersion != result.CurrentVersion ||
		state.PreviousVersion != result.PreviousVersion || state.SnapshotID != result.SnapshotID ||
		state.SnapshotManifestSHA256 != result.SnapshotManifestSHA256 {
		return errors.New("activation result does not match durable state")
	}
	readyPayload, err := json.Marshal(struct {
		Schema           string                       `json:"schema"`
		Binding          upgradepkg.ActivationBinding `json:"binding"`
		Result           upgradepkg.ActivateResult    `json:"result"`
		State            upgradepkg.State             `json:"state"`
		Outcome          maintenance.Outcome          `json:"outcome"`
		FinalStateSHA256 string                       `json:"final_state_sha256"`
	}{"ytqjk-activation-ready/v1", binding, result, state, outcome, finalStateSHA256})
	if err != nil {
		return err
	}
	if err := claim.lease.MarkReady(safeio.SHA256(readyPayload)); err != nil {
		return err
	}
	_, err = claim.lease.Complete(outcome, finalStateSHA256)
	return err
}

func clearBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func activationResultOutcome(result upgradepkg.ActivateResult, rollbackCommand bool) (maintenance.Outcome, bool) {
	if rollbackCommand && result.Status == "ACTIVE" && result.Rollback == "SUCCEEDED" {
		return maintenance.OutcomeRolledBack, true
	}
	if !rollbackCommand && result.Status == "ACTIVE" && result.Rollback == "" {
		return maintenance.OutcomeSucceeded, true
	}
	if !rollbackCommand && result.Status == "ROLLED_BACK" && result.Rollback == "SUCCEEDED" {
		return maintenance.OutcomeRolledBack, true
	}
	return "", false
}

func planBinding(plan upgradepkg.Plan) (upgradepkg.ActivationBinding, error) {
	path := filepath.Join(plan.StageRoot, "plan.json")
	digest, err := safeio.FileSHA256(path)
	if err != nil {
		return upgradepkg.ActivationBinding{}, err
	}
	return upgradepkg.ReadActivationBinding(path, digest)
}

func rollbackBinding(plan upgradepkg.RollbackPlan) (upgradepkg.ActivationBinding, error) {
	path := filepath.Join(plan.StageRoot, "rollback-plan.json")
	digest, err := safeio.FileSHA256(path)
	if err != nil {
		return upgradepkg.ActivationBinding{}, err
	}
	return upgradepkg.ReadRollbackBinding(path, digest)
}

package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
)

type installMaintenanceController struct {
	lease *maintenance.Lease
}

func beginInstallMaintenance(
	ctx context.Context,
	runtimeRoot, codexRoot, knowledgeRoot, target, operation string,
) (*installMaintenanceController, error) {
	controlRoot, err := maintenanceControlRoot()
	if err != nil {
		return nil, err
	}
	if err := maintenance.BootstrapControlRoot(ctx, controlRoot); err != nil {
		return nil, err
	}
	operationID, err := randomOperationID()
	if err != nil {
		return nil, err
	}
	lease, err := maintenance.BeginExclusive(ctx, maintenance.Scope{
		ControlRoot: controlRoot,
		ProspectiveRoots: []string{
			runtimeRoot, codexRoot, knowledgeRoot, target,
		},
	}, maintenance.ExclusiveOptions{
		OperationID:  operationID,
		Purpose:      strings.ToUpper(operation),
		Duration:     maintenance.MaxExclusiveDuration,
		DrainTimeout: maintenance.MaxDrainTimeout,
	})
	if err != nil {
		return nil, err
	}
	return &installMaintenanceController{lease: lease}, nil
}

func (controller *installMaintenanceController) beginMutation() error {
	if controller == nil || controller.lease == nil {
		return errors.New("install maintenance controller is unavailable")
	}
	return controller.lease.BeginMutation()
}

func (controller *installMaintenanceController) complete(outcome maintenance.Outcome) (maintenance.Receipt, error) {
	if controller == nil || controller.lease == nil {
		return maintenance.Receipt{}, errors.New("install maintenance controller is unavailable")
	}
	return controller.lease.Complete(outcome)
}

func randomOperationID() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func (context commandContext) emitInstallMaintenance(
	receipt map[string]any,
	asJSON bool,
	exitCode int,
	controller *installMaintenanceController,
	outcome maintenance.Outcome,
) int {
	maintenanceReceipt, err := controller.complete(outcome)
	if err != nil {
		receipt["maintenance"] = map[string]any{"status": "FAILED", "error": safeError(err)}
		return context.emitInstall(receipt, asJSON, 2)
	}
	receipt["maintenance"] = map[string]any{"status": "SUCCEEDED", "receipt": maintenanceReceipt}
	return context.emitInstall(receipt, asJSON, exitCode)
}

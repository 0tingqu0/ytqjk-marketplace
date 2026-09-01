package upgrade

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	operationLockSchema = "ytqjk-upgrade-operation-lock/v1"
	operationLease      = 2 * time.Minute
	operationGuardWait  = 5 * time.Second

	phasePreparing         = "PREPARING"
	phasePrepared          = "PREPARED"
	phaseActivationPending = "ACTIVATION_PENDING"
	phaseActivating        = "ACTIVATING"
	phaseRollbackPreparing = "ROLLBACK_PREPARING"
	phaseRollbackPrepared  = "ROLLBACK_PREPARED"
	phaseRollbackPending   = "ROLLBACK_PENDING"
	phaseRollingBack       = "ROLLING_BACK"
	phaseReleased          = "RELEASED"
)

type operationRecord struct {
	Schema         string    `json:"schema"`
	OperationID    string    `json:"operation_id"`
	OwnerPID       int       `json:"owner_pid"`
	OwnerIdentity  string    `json:"owner_identity"`
	Phase          string    `json:"phase"`
	Active         bool      `json:"active"`
	AcquiredAt     time.Time `json:"acquired_at"`
	RenewedAt      time.Time `json:"renewed_at"`
	LeaseExpiresAt time.Time `json:"lease_expires_at"`
	FinishedAt     time.Time `json:"finished_at,omitempty"`
}

var writeOperationJSON = safeio.WriteJSON

func acquireOperation(runtimeRoot, operationID, phase string) error {
	if !hexDigestPattern.MatchString(operationID) || !preMutationPhase(phase) {
		return failure("UPGRADE_OPERATION_LOCK_INVALID", nil)
	}
	return withOperationGuard(runtimeRoot, func() error {
		current, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		if exists {
			if err := permitStaleReplacement(current, time.Now().UTC()); err != nil {
				return err
			}
		} else if err := allowMissingOperationRecord(runtimeRoot); err != nil {
			return err
		}
		owner, err := currentOperationOwner()
		if err != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", err)
		}
		now := time.Now().UTC()
		next := operationRecord{
			Schema: operationLockSchema, OperationID: operationID,
			OwnerPID: owner.PID, OwnerIdentity: owner.Identity, Phase: phase,
			Active: true, AcquiredAt: now, RenewedAt: now, LeaseExpiresAt: now.Add(operationLease),
		}
		return replaceOperationRecord(runtimeRoot, recordPointer(current, exists), next)
	})
}

func transitionOperation(runtimeRoot, operationID, fromPhase, toPhase string) error {
	if !knownOperationPhase(fromPhase) || !knownOperationPhase(toPhase) {
		return failure("UPGRADE_OPERATION_LOCK_INVALID", nil)
	}
	return withOperationGuard(runtimeRoot, func() error {
		record, err := ownedOperation(runtimeRoot, operationID, fromPhase)
		if err != nil {
			return err
		}
		previous := record
		record.Phase = toPhase
		renewOperationRecord(&record)
		return replaceOperationRecord(runtimeRoot, &previous, record)
	})
}

func claimOperation(runtimeRoot, operationID, phase string) error {
	return withOperationGuard(runtimeRoot, func() error {
		record, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		owner, ownerErr := currentOperationOwner()
		if ownerErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
		}
		if !exists || !record.Active || record.OperationID != operationID || record.Phase != phase || !recordOwnedBy(record, owner) {
			return failure("UPGRADE_RECOVERY_REQUIRED", nil)
		}
		previous := record
		renewOperationRecord(&record)
		return replaceOperationRecord(runtimeRoot, &previous, record)
	})
}

func transferOperation(runtimeRoot, operationID, phase string, expectedOwnerPID, childPID int) error {
	return withOperationGuard(runtimeRoot, func() error {
		record, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		owner, ownerErr := currentOperationOwner()
		if ownerErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
		}
		if !exists || !record.Active || record.OperationID != operationID || record.Phase != phase ||
			expectedOwnerPID != owner.PID || !recordOwnedBy(record, owner) {
			return failure("UPGRADE_RECOVERY_REQUIRED", nil)
		}
		alive, err := operationProcessAlive(childPID)
		if err != nil {
			return failure("UPGRADE_OPERATION_TRANSFER_FAILED", err)
		}
		if !alive {
			return failure("UPGRADE_OPERATION_TRANSFER_FAILED", os.ErrProcessDone)
		}
		childIdentity, err := operationProcessIdentity(childPID)
		if err != nil {
			return failure("UPGRADE_OPERATION_TRANSFER_FAILED", err)
		}
		previous := record
		record.OwnerPID = childPID
		record.OwnerIdentity = childIdentity
		renewOperationRecord(&record)
		return replaceOperationRecord(runtimeRoot, &previous, record)
	})
}

func restoreOperationOwner(runtimeRoot, operationID, pendingPhase, preparedPhase string) error {
	return withOperationGuard(runtimeRoot, func() error {
		record, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		owner, ownerErr := currentOperationOwner()
		if ownerErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
		}
		if !exists || !record.Active || record.OperationID != operationID || record.Phase != pendingPhase ||
			!recordOwnedBy(record, owner) {
			return failure("UPGRADE_RECOVERY_REQUIRED", nil)
		}
		previous := record
		record.Phase = preparedPhase
		renewOperationRecord(&record)
		return replaceOperationRecord(runtimeRoot, &previous, record)
	})
}

func releaseTerminalOperation(runtimeRoot, operationID string, operationErr error) error {
	if errorContainsCode(operationErr, "UPGRADE_STATE_DURABILITY_UNKNOWN") ||
		errorContainsCode(operationErr, "UPGRADE_OPERATION_DURABILITY_UNKNOWN") {
		return nil
	}
	return withOperationGuard(runtimeRoot, func() error {
		record, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		owner, ownerErr := currentOperationOwner()
		if ownerErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
		}
		if !exists || !record.Active || record.OperationID != operationID || !recordOwnedBy(record, owner) {
			return failure("UPGRADE_RECOVERY_REQUIRED", nil)
		}
		state, err := readOperationState(runtimeRoot)
		if err != nil || state.OperationID != operationID || !safeTerminalStatus(state.Status) {
			return failure("UPGRADE_RECOVERY_REQUIRED", err)
		}
		previous := record
		return replaceOperationRecord(runtimeRoot, &previous, releasedOperationRecord(record))
	})
}

func abandonOperation(runtimeRoot, operationID, phase string) error {
	return withOperationGuard(runtimeRoot, func() error {
		record, err := ownedOperation(runtimeRoot, operationID, phase)
		if err != nil {
			return err
		}
		previous := record
		return replaceOperationRecord(runtimeRoot, &previous, releasedOperationRecord(record))
	})
}

func permitStaleReplacement(record operationRecord, now time.Time) error {
	if !record.Active && record.Phase == phaseReleased {
		return nil
	}
	alive, err := recordedOwnerAlive(record)
	if err != nil {
		return failure("UPGRADE_OPERATION_LOCK_FAILED", err)
	}
	if alive || !now.After(record.LeaseExpiresAt) {
		return failure("UPGRADE_OPERATION_IN_PROGRESS", nil)
	}
	if preMutationPhase(record.Phase) {
		return nil
	}
	return failure("UPGRADE_RECOVERY_REQUIRED", nil)
}

func ownedOperation(runtimeRoot, operationID, phase string) (operationRecord, error) {
	record, exists, err := readOperationRecord(runtimeRoot)
	if err != nil {
		return operationRecord{}, err
	}
	owner, ownerErr := currentOperationOwner()
	if ownerErr != nil {
		return operationRecord{}, failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
	}
	if !exists || !record.Active || record.OperationID != operationID || !recordOwnedBy(record, owner) || record.Phase != phase {
		return operationRecord{}, failure("UPGRADE_RECOVERY_REQUIRED", nil)
	}
	return record, nil
}

func allowMissingOperationRecord(runtimeRoot string) error {
	state, err := readOperationState(runtimeRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if safeTerminalStatus(state.Status) || (state.Status == "IDLE" && state.OperationID == "") {
		return nil
	}
	return failure("UPGRADE_RECOVERY_REQUIRED", nil)
}

func currentOperationOwner() (operationOwner, error) {
	identity, err := operationProcessIdentity(os.Getpid())
	return operationOwner{PID: os.Getpid(), Identity: identity}, err
}

func recordedOwnerAlive(record operationRecord) (bool, error) {
	alive, err := operationProcessAlive(record.OwnerPID)
	if err != nil || !alive {
		return alive, err
	}
	identity, err := operationProcessIdentity(record.OwnerPID)
	if err != nil {
		return true, err
	}
	return identity == record.OwnerIdentity, nil
}

type operationOwner struct {
	PID      int
	Identity string
}

func recordOwnedBy(record operationRecord, owner operationOwner) bool {
	return record.OwnerPID == owner.PID && record.OwnerIdentity == owner.Identity
}

func withOperationGuard(runtimeRoot string, action func() error) error {
	root := filepath.Join(runtimeRoot, "upgrade")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return failure("UPGRADE_OPERATION_LOCK_FAILED", err)
	}
	unlock, err := lockOperationGuard(filepath.Join(root, "operation.guard"))
	if err != nil {
		return failure("UPGRADE_OPERATION_LOCK_FAILED", err)
	}
	actionErr := action()
	if unlockErr := unlock(); unlockErr != nil {
		return errors.Join(actionErr, failure("UPGRADE_OPERATION_LOCK_FAILED", unlockErr))
	}
	return actionErr
}

func readOperationRecord(runtimeRoot string) (operationRecord, bool, error) {
	if _, err := os.Stat(operationUncertainPath(runtimeRoot)); err == nil {
		return operationRecord{}, false, failure("UPGRADE_RECOVERY_REQUIRED", nil)
	} else if !errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, failure("UPGRADE_OPERATION_LOCK_FAILED", err)
	}
	var record operationRecord
	err := safeio.ReadJSON(operationRecordPath(runtimeRoot), &record)
	if errors.Is(err, os.ErrNotExist) {
		return operationRecord{}, false, nil
	}
	if err != nil || !validOperationRecord(record) {
		return operationRecord{}, false, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return record, true, nil
}

func replaceOperationRecord(runtimeRoot string, previous *operationRecord, record operationRecord) error {
	if !validOperationRecord(record) {
		return failure("UPGRADE_OPERATION_LOCK_INVALID", nil)
	}
	if err := writeOperationJSON(operationRecordPath(runtimeRoot), record); err != nil {
		if safeio.WasCommitted(err) {
			if previous != nil {
				if restoreErr := writeOperationJSON(operationRecordPath(runtimeRoot), *previous); restoreErr != nil {
					markOperationUncertain(runtimeRoot)
				}
			}
			return failure("UPGRADE_OPERATION_DURABILITY_UNKNOWN", err)
		}
		return failure("UPGRADE_OPERATION_LOCK_FAILED", err)
	}
	return nil
}

func readOperationState(runtimeRoot string) (State, error) {
	var state State
	if err := safeio.ReadJSON(statePath(runtimeRoot), &state); err != nil {
		return State{}, fmt.Errorf("upgrade state is unavailable: %w", err)
	}
	if state.Schema != stateSchema {
		return State{}, errors.New("upgrade state schema is invalid")
	}
	return state, nil
}

func renewOperationRecord(record *operationRecord) {
	record.RenewedAt = time.Now().UTC()
	record.LeaseExpiresAt = record.RenewedAt.Add(operationLease)
}

func releasedOperationRecord(record operationRecord) operationRecord {
	now := time.Now().UTC()
	record.Active = false
	record.Phase = phaseReleased
	record.RenewedAt = now
	record.LeaseExpiresAt = now
	record.FinishedAt = now
	return record
}

func validOperationRecord(record operationRecord) bool {
	return record.Schema == operationLockSchema && hexDigestPattern.MatchString(record.OperationID) &&
		record.OwnerPID > 0 && validOwnerIdentity(record.OwnerIdentity) && knownOperationPhase(record.Phase) && !record.AcquiredAt.IsZero() &&
		!record.RenewedAt.Before(record.AcquiredAt) && !record.LeaseExpiresAt.Before(record.RenewedAt) &&
		((record.Active && record.Phase != phaseReleased && record.FinishedAt.IsZero()) ||
			(!record.Active && record.Phase == phaseReleased && !record.FinishedAt.IsZero()))
}

func validOwnerIdentity(identity string) bool {
	return identity != "" && len(identity) <= 256 && identity == strings.TrimSpace(identity) &&
		!strings.ContainsAny(identity, "\r\n")
}

func knownOperationPhase(phase string) bool {
	return preMutationPhase(phase) || phase == phaseActivating || phase == phaseRollingBack || phase == phaseReleased
}

func preMutationPhase(phase string) bool {
	switch phase {
	case phasePreparing, phasePrepared, phaseActivationPending,
		phaseRollbackPreparing, phaseRollbackPrepared, phaseRollbackPending:
		return true
	default:
		return false
	}
}

func safeTerminalStatus(status string) bool {
	return status == "ACTIVE" || status == "ROLLED_BACK" || status == "FAILED"
}

func operationRecordPath(runtimeRoot string) string {
	return filepath.Join(runtimeRoot, "upgrade", "operation.json")
}

func operationUncertainPath(runtimeRoot string) string {
	return filepath.Join(runtimeRoot, "upgrade", "operation.uncertain")
}

func markOperationUncertain(runtimeRoot string) {
	_ = os.WriteFile(operationUncertainPath(runtimeRoot), []byte("manual recovery required\n"), 0o600)
}

func recordPointer(record operationRecord, exists bool) *operationRecord {
	if !exists {
		return nil
	}
	return &record
}

func errorContainsCode(err error, code string) bool {
	if err == nil {
		return false
	}
	if value, ok := err.(*Error); ok && value.Code == code {
		return true
	}
	if many, ok := err.(interface{ Unwrap() []error }); ok {
		for _, nested := range many.Unwrap() {
			if errorContainsCode(nested, code) {
				return true
			}
		}
		return false
	}
	return errorContainsCode(errors.Unwrap(err), code)
}

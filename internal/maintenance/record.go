package maintenance

import (
	"errors"
	"os"
	"reflect"
	"sort"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

var writeRecordJSON = writeBoundJSON

func loadOrInitialize(control controlPlane) (Record, error) {
	record, exists, err := readRecord(control)
	if err != nil {
		return Record{}, err
	}
	if !exists {
		return Record{}, fail(CodeStateCorrupt, errors.New("maintenance record is missing"))
	}
	return record, nil
}

func readRecord(control controlPlane) (Record, bool, error) {
	var record Record
	data, err := readBoundRegularFile(control, recordFileName)
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err == nil {
		err = decodeStrictJSON(data, &record)
	}
	if err == nil && validRecord(record) {
		err = validateRecordCanaryBinding(control, record)
	}
	if err != nil || !validRecord(record) {
		return Record{}, false, fail(CodeStateCorrupt, errors.Join(errors.New("maintenance record is invalid"), err))
	}
	return record, true, nil
}

func writeInitialRecord(control controlPlane, record Record) error {
	if !validRecord(record) {
		return fail(CodeStateCorrupt, errors.New("initial maintenance record is invalid"))
	}
	exists, err := boundEntryExists(control, recordFileName)
	if err != nil {
		return fail(CodeStateCorrupt, err)
	}
	if exists {
		return fail(CodeStateCorrupt, errors.New("maintenance record appeared during initialization"))
	}
	if err := validateRecordCanaryBinding(control, record); err != nil {
		return err
	}
	return writeRecordFile(control, record)
}

func writeTransition(control controlPlane, previous, next Record) error {
	if !validRecord(previous) || !validRecord(next) || next.Revision != previous.Revision+1 ||
		next.UpdatedAt.Before(previous.UpdatedAt) {
		return fail(CodeStateCorrupt, errors.New("maintenance transition is invalid"))
	}
	if err := errors.Join(
		validateRecordCanaryBinding(control, previous),
		validateRecordCanaryBinding(control, next),
	); err != nil {
		return err
	}
	return writeRecordFile(control, next)
}

func persistTransition(control controlPlane, previous, next Record) (bool, error) {
	err := writeTransition(control, previous, next)
	if err == nil {
		return true, nil
	}
	if !IsCode(err, CodeDurabilityUnknown) {
		return false, err
	}
	observed, exists, readErr := readRecord(control)
	if readErr == nil && exists && reflect.DeepEqual(observed, next) {
		return true, nil
	}
	return false, errors.Join(
		err,
		fail(CodeRecoveryRequired, errors.New("maintenance transition result could not be confirmed")),
		readErr,
	)
}

func writeRecordFile(control controlPlane, record Record) error {
	err := writeRecordJSON(control, record)
	if err == nil {
		return nil
	}
	if safeio.WasCommitted(err) {
		return fail(CodeDurabilityUnknown, err)
	}
	return fail(CodeLockFailed, err)
}

func validRecord(record Record) bool {
	if record.Schema != recordSchema || record.Revision == 0 || record.UpdatedAt.IsZero() {
		return false
	}
	switch record.State {
	case StateOpen:
		return record.Intent == nil && validOptionalReceipt(record.Receipt, record.Generation)
	case StateDraining:
		return record.Receipt == nil && validIntent(record.Intent) && record.Generation == record.Intent.BaseGeneration &&
			record.Intent.MutationStarted == nil && !record.Intent.TransferPending && record.Intent.Canary == nil
	case StateMaintenance:
		return record.Receipt == nil && validIntent(record.Intent) && record.Generation == record.Intent.TargetGeneration &&
			record.Intent.Canary == nil && (record.Intent.MutationStarted == nil || !record.Intent.TransferPending)
	case StateReopening:
		return record.Receipt == nil && validIntent(record.Intent) && record.Generation == record.Intent.TargetGeneration &&
			record.Intent.MutationStarted != nil && validCanary(record.Intent.Canary, record.Intent) &&
			record.Intent.Recovery == nil
	case StateRestoring:
		return record.Receipt == nil && validIntent(record.Intent) && record.Generation == record.Intent.TargetGeneration &&
			record.Intent.MutationStarted != nil && !record.Intent.TransferPending && record.Intent.Canary == nil
	case StateRecoveryRequired:
		return record.Receipt == nil && validIntent(record.Intent) && record.Generation == record.Intent.TargetGeneration &&
			!record.Intent.TransferPending && validRecovery(record.Intent.Recovery, record.Intent) &&
			(record.Intent.Canary == nil || validCanary(record.Intent.Canary, record.Intent))
	default:
		return false
	}
}

func validIntent(intent *Intent) bool {
	if intent == nil || !validOperationID(intent.OperationID) || !validPurpose(intent.Purpose) ||
		!validOwner(intent.Owner) || intent.TargetGeneration != intent.BaseGeneration+1 ||
		intent.StartedAt.IsZero() || intent.UpdatedAt.Before(intent.StartedAt) ||
		!intent.ExpiresAt.After(intent.StartedAt) || intent.DrainDeadline.Before(intent.StartedAt) ||
		intent.DrainDeadline.After(intent.ExpiresAt) || !validResources(intent.Resources) {
		return false
	}
	if intent.MutationStarted != nil &&
		(intent.MutationStarted.Before(intent.StartedAt) || !intent.MutationStarted.Before(intent.ExpiresAt)) {
		return false
	}
	return intent.Recovery == nil || validRecovery(intent.Recovery, intent)
}

func validRecovery(recovery *Recovery, intent *Intent) bool {
	return recovery != nil && validRecoveryCode(recovery.Code) && validRecoveryCause(recovery.Cause) &&
		!recovery.MarkedAt.IsZero() && !recovery.MarkedAt.Before(intent.StartedAt) &&
		!recovery.MarkedAt.After(intent.UpdatedAt)
}

func validResources(resources []string) bool {
	if len(resources) == 0 || !sort.StringsAreSorted(resources) {
		return false
	}
	for index, value := range resources {
		if value == "" || (index > 0 && resources[index-1] == value) {
			return false
		}
	}
	return true
}

func validOptionalReceipt(receipt *Receipt, generation uint64) bool {
	if receipt == nil {
		return true
	}
	if !validOperationID(receipt.OperationID) || receipt.Generation != generation || !validOutcome(receipt.Outcome) ||
		!validResources(receipt.Resources) || receipt.FinishedAt.IsZero() {
		return false
	}
	return receipt.Canary == nil ||
		validCanaryReceipt(receipt.Canary, receipt.Outcome, receipt.FinishedAt, receipt.Generation)
}

func requireOwned(record Record, control controlPlane, operationID string, generation uint64, owner Owner) error {
	if record.Intent == nil || record.Intent.OperationID != operationID ||
		record.Intent.TargetGeneration != generation || !ownerEqual(record.Intent.Owner, owner) ||
		!sameStrings(record.Intent.Resources, control.resources) {
		return fail(CodeRecoveryRequired, errors.New("maintenance operation ownership does not match"))
	}
	return nil
}

func requireRecoverable(
	record Record,
	control controlPlane,
	operationID string,
	generation uint64,
	now time.Time,
) (bool, error) {
	if record.State == StateOpen || record.Intent == nil || record.Intent.OperationID != operationID ||
		record.Intent.TargetGeneration != generation || !sameStrings(record.Intent.Resources, control.resources) {
		return false, fail(CodeRecoveryRequired, errors.New("maintenance recovery identity does not match"))
	}
	alive, err := ownerAlive(record.Intent.Owner)
	if err != nil {
		return false, fail(CodeRecoveryRequired, err)
	}
	if alive {
		return false, fail(CodeActive, errors.New("maintenance owner is still alive"))
	}
	expired := !now.Before(record.Intent.ExpiresAt)
	safeExpiredPreMutationAbort := record.Intent.MutationStarted == nil
	return expired && !safeExpiredPreMutationAbort, nil
}

func activeStateError(record Record, now time.Time) error {
	if record.State == StateOpen {
		return nil
	}
	if record.State == StateRestoring || record.State == StateRecoveryRequired || record.Intent == nil {
		return fail(CodeRecoveryRequired, nil)
	}
	alive, err := ownerAlive(record.Intent.Owner)
	if err != nil {
		return fail(CodeRecoveryRequired, err)
	}
	if alive || now.Before(record.Intent.ExpiresAt) {
		return fail(CodeActive, nil)
	}
	return fail(CodeRecoveryRequired, nil)
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func cloneStrings(values []string) []string {
	return append([]string(nil), values...)
}

func cloneRecord(record Record) Record {
	result := record
	if record.Intent != nil {
		intent := *record.Intent
		intent.Resources = cloneStrings(record.Intent.Resources)
		if record.Intent.MutationStarted != nil {
			started := *record.Intent.MutationStarted
			intent.MutationStarted = &started
		}
		if record.Intent.Recovery != nil {
			recovery := *record.Intent.Recovery
			intent.Recovery = &recovery
		}
		if record.Intent.Canary != nil {
			canary := *record.Intent.Canary
			if record.Intent.Canary.ReadyAt != nil {
				readyAt := *record.Intent.Canary.ReadyAt
				canary.ReadyAt = &readyAt
			}
			intent.Canary = &canary
		}
		result.Intent = &intent
	}
	if record.Receipt != nil {
		receipt := *record.Receipt
		receipt.Resources = cloneStrings(record.Receipt.Resources)
		if record.Receipt.Canary != nil {
			canary := *record.Receipt.Canary
			receipt.Canary = &canary
		}
		result.Receipt = &receipt
	}
	return result
}

func cloneReceipt(receipt Receipt) Receipt {
	receipt.Resources = cloneStrings(receipt.Resources)
	if receipt.Canary != nil {
		canary := *receipt.Canary
		receipt.Canary = &canary
	}
	return receipt
}

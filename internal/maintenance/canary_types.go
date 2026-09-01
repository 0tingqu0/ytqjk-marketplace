package maintenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	canaryBaseBindingSchema    = "ytqjk-maintenance-canary-base-binding/v1"
	canaryReadyBindingSchema   = "ytqjk-maintenance-canary-ready-binding/v1"
	canaryActiveBindingSchema  = "ytqjk-maintenance-canary-active-binding/v1"
	canaryReceiptBindingSchema = "ytqjk-maintenance-canary-receipt-binding/v1"
)

// CanaryOptions binds one target process to one authenticated reopening
// attempt. Capability is transported out of band and is never persisted.
type CanaryOptions struct {
	Capability             []byte `json:"-"`
	PlanSHA256             string
	SnapshotManifestSHA256 string
	TargetBinarySHA256     string
	TargetVersion          string
	Port                   int
	Attempt                uint64
	ExpectedOutcome        Outcome
	FallbackOutcome        Outcome
	Deadline               time.Time
}

// Canary is durable REOPENING metadata. CapabilitySHA256 is deliberately the
// only persisted representation of the out-of-band capability.
type Canary struct {
	Owner                  Owner      `json:"owner"`
	CapabilitySHA256       string     `json:"capability_sha256"`
	BaseBindingSHA256      string     `json:"base_binding_sha256"`
	ReadyBindingSHA256     string     `json:"ready_binding_sha256,omitempty"`
	ActiveBindingSHA256    string     `json:"active_binding_sha256"`
	PlanSHA256             string     `json:"plan_sha256"`
	SnapshotManifestSHA256 string     `json:"snapshot_manifest_sha256"`
	TargetBinarySHA256     string     `json:"target_binary_sha256"`
	TargetVersion          string     `json:"target_version"`
	Port                   int        `json:"port"`
	Attempt                uint64     `json:"attempt"`
	ExpectedOutcome        Outcome    `json:"expected_outcome"`
	FallbackOutcome        Outcome    `json:"fallback_outcome,omitempty"`
	Deadline               time.Time  `json:"deadline"`
	ReadyReceiptSHA256     string     `json:"ready_receipt_sha256,omitempty"`
	ReadyAt                *time.Time `json:"ready_at,omitempty"`
}

// CanaryReceipt preserves the authenticated reopening evidence after Intent
// is removed by the durable transition to OPEN.
type CanaryReceipt struct {
	Owner                  Owner     `json:"owner"`
	Purpose                string    `json:"purpose"`
	BaseGeneration         uint64    `json:"base_generation"`
	StartedAt              time.Time `json:"started_at"`
	ExpiresAt              time.Time `json:"expires_at"`
	DrainDeadline          time.Time `json:"drain_deadline"`
	MutationStarted        time.Time `json:"mutation_started_at"`
	CapabilitySHA256       string    `json:"capability_sha256"`
	BaseBindingSHA256      string    `json:"base_binding_sha256"`
	ReadyBindingSHA256     string    `json:"ready_binding_sha256"`
	ActiveBindingSHA256    string    `json:"active_binding_sha256"`
	ReceiptBindingSHA256   string    `json:"receipt_binding_sha256"`
	PlanSHA256             string    `json:"plan_sha256"`
	SnapshotManifestSHA256 string    `json:"snapshot_manifest_sha256"`
	TargetBinarySHA256     string    `json:"target_binary_sha256"`
	TargetVersion          string    `json:"target_version"`
	Port                   int       `json:"port"`
	Attempt                uint64    `json:"attempt"`
	ExpectedOutcome        Outcome   `json:"expected_outcome"`
	FallbackOutcome        Outcome   `json:"fallback_outcome,omitempty"`
	Deadline               time.Time `json:"deadline"`
	ReadyReceiptSHA256     string    `json:"ready_receipt_sha256"`
	ReadyAt                time.Time `json:"ready_at"`
	FinalStateSHA256       string    `json:"final_state_sha256"`
}

// CanaryFence is safe to expose to the target health path. It contains no
// capability bytes or capability digest.
type CanaryFence struct {
	OperationID            string
	Generation             uint64
	Revision               uint64
	Resources              []string
	BaseBindingSHA256      string
	ReadyBindingSHA256     string
	ActiveBindingSHA256    string
	PlanSHA256             string
	SnapshotManifestSHA256 string
	TargetBinarySHA256     string
	TargetVersion          string
	Port                   int
	Owner                  Owner
	Attempt                uint64
	ExpectedOutcome        Outcome
	FallbackOutcome        Outcome
	Deadline               time.Time
}

type canaryBaseBindingPayload struct {
	Schema                   string    `json:"schema"`
	OperationID              string    `json:"operation_id"`
	Purpose                  string    `json:"purpose"`
	Resources                []string  `json:"resources"`
	Owner                    Owner     `json:"owner"`
	BaseGeneration           uint64    `json:"base_generation"`
	TargetGeneration         uint64    `json:"target_generation"`
	StartedAt                time.Time `json:"started_at"`
	ExpiresAt                time.Time `json:"expires_at"`
	DrainDeadline            time.Time `json:"drain_deadline"`
	MutationStarted          time.Time `json:"mutation_started_at"`
	ControlDirectoryIdentity string    `json:"control_directory_identity"`
	CapabilitySHA256         string    `json:"capability_sha256"`
	PlanSHA256               string    `json:"plan_sha256"`
	SnapshotManifestSHA256   string    `json:"snapshot_manifest_sha256"`
	TargetBinarySHA256       string    `json:"target_binary_sha256"`
	TargetVersion            string    `json:"target_version"`
	Port                     int       `json:"port"`
	Attempt                  uint64    `json:"attempt"`
	ExpectedOutcome          Outcome   `json:"expected_outcome"`
	FallbackOutcome          Outcome   `json:"fallback_outcome,omitempty"`
	Deadline                 time.Time `json:"deadline"`
}

type canaryActiveBindingPayload struct {
	Schema             string    `json:"schema"`
	State              State     `json:"state"`
	Generation         uint64    `json:"generation"`
	Revision           uint64    `json:"revision"`
	RecordUpdatedAt    time.Time `json:"record_updated_at"`
	IntentUpdatedAt    time.Time `json:"intent_updated_at"`
	TransferPending    bool      `json:"transfer_pending"`
	BaseBindingSHA256  string    `json:"base_binding_sha256"`
	ReadyBindingSHA256 string    `json:"ready_binding_sha256,omitempty"`
}

type canaryReadyBindingPayload struct {
	Schema             string    `json:"schema"`
	BaseBindingSHA256  string    `json:"base_binding_sha256"`
	ReadyReceiptSHA256 string    `json:"ready_receipt_sha256"`
	ReadyAt            time.Time `json:"ready_at"`
}

type canaryReceiptBindingPayload struct {
	Schema                   string    `json:"schema"`
	State                    State     `json:"state"`
	RecordGeneration         uint64    `json:"record_generation"`
	RecordRevision           uint64    `json:"record_revision"`
	RecordUpdatedAt          time.Time `json:"record_updated_at"`
	BaseBindingSHA256        string    `json:"base_binding_sha256"`
	ReadyBindingSHA256       string    `json:"ready_binding_sha256"`
	ActiveBindingSHA256      string    `json:"active_binding_sha256"`
	OperationID              string    `json:"operation_id"`
	Generation               uint64    `json:"generation"`
	Resources                []string  `json:"resources"`
	ControlDirectoryIdentity string    `json:"control_directory_identity"`
	CapabilitySHA256         string    `json:"capability_sha256"`
	PlanSHA256               string    `json:"plan_sha256"`
	SnapshotManifestSHA256   string    `json:"snapshot_manifest_sha256"`
	TargetBinarySHA256       string    `json:"target_binary_sha256"`
	TargetVersion            string    `json:"target_version"`
	Port                     int       `json:"port"`
	Owner                    Owner     `json:"owner"`
	Attempt                  uint64    `json:"attempt"`
	ExpectedOutcome          Outcome   `json:"expected_outcome"`
	FallbackOutcome          Outcome   `json:"fallback_outcome,omitempty"`
	Deadline                 time.Time `json:"deadline"`
	ReadyReceiptSHA256       string    `json:"ready_receipt_sha256"`
	ReadyAt                  time.Time `json:"ready_at"`
	FinalStateSHA256         string    `json:"final_state_sha256"`
	Outcome                  Outcome   `json:"outcome"`
	FinishedAt               time.Time `json:"finished_at"`
}

func validateCanaryOptions(options CanaryOptions, intent *Intent, now time.Time) error {
	deadline := options.Deadline.UTC()
	if intent == nil || intent.MutationStarted == nil || len(options.Capability) < 32 || len(options.Capability) > 256 ||
		!validSHA256(options.PlanSHA256) || !validSHA256(options.SnapshotManifestSHA256) ||
		!validSHA256(options.TargetBinarySHA256) || !validTargetVersion(options.TargetVersion) ||
		options.Port < 1 || options.Port > 65535 || options.Attempt == 0 ||
		!validCanaryOutcome(options.ExpectedOutcome) ||
		!validCanaryFallback(options.ExpectedOutcome, options.FallbackOutcome) ||
		deadline.IsZero() || !now.Before(deadline) ||
		deadline.After(intent.ExpiresAt.Add(-RecoveryReserve)) || deadline.Before(*intent.MutationStarted) {
		return fail(CodeInvalid, errors.New("canary options are invalid"))
	}
	return nil
}

func validCanary(value *Canary, intent *Intent) bool {
	if value == nil || intent == nil || intent.MutationStarted == nil || !validOwner(value.Owner) ||
		!ownerEqual(value.Owner, intent.Owner) || !validSHA256(value.CapabilitySHA256) ||
		!validSHA256(value.BaseBindingSHA256) || !validSHA256(value.ActiveBindingSHA256) ||
		!validSHA256(value.PlanSHA256) ||
		!validSHA256(value.SnapshotManifestSHA256) || !validSHA256(value.TargetBinarySHA256) ||
		!validTargetVersion(value.TargetVersion) || value.Port < 1 || value.Port > 65535 ||
		value.Attempt == 0 || !validCanaryOutcome(value.ExpectedOutcome) ||
		!validCanaryFallback(value.ExpectedOutcome, value.FallbackOutcome) || value.Deadline.IsZero() ||
		value.Deadline.After(intent.ExpiresAt.Add(-RecoveryReserve)) ||
		value.Deadline.Before(*intent.MutationStarted) {
		return false
	}
	if value.ReadyAt == nil {
		return value.ReadyReceiptSHA256 == "" && value.ReadyBindingSHA256 == ""
	}
	return validSHA256(value.ReadyReceiptSHA256) && validSHA256(value.ReadyBindingSHA256) &&
		!value.ReadyAt.Before(*intent.MutationStarted) &&
		!value.ReadyAt.After(intent.UpdatedAt) && value.ReadyAt.Before(value.Deadline)
}

func validCanaryReceipt(value *CanaryReceipt, outcome Outcome, finishedAt time.Time, generation uint64) bool {
	return value != nil && validOwner(value.Owner) && validPurpose(value.Purpose) &&
		value.BaseGeneration+1 == generation &&
		!value.StartedAt.IsZero() && value.ExpiresAt.After(value.StartedAt) &&
		!value.DrainDeadline.Before(value.StartedAt) && !value.DrainDeadline.After(value.ExpiresAt) &&
		!value.MutationStarted.Before(value.StartedAt) && value.MutationStarted.Before(value.ExpiresAt) &&
		validSHA256(value.CapabilitySHA256) &&
		validSHA256(value.BaseBindingSHA256) && validSHA256(value.ReadyBindingSHA256) &&
		validSHA256(value.ActiveBindingSHA256) &&
		validSHA256(value.ReceiptBindingSHA256) &&
		validSHA256(value.PlanSHA256) && validSHA256(value.SnapshotManifestSHA256) &&
		validSHA256(value.TargetBinarySHA256) && validTargetVersion(value.TargetVersion) &&
		value.Port >= 1 && value.Port <= 65535 && value.Attempt > 0 &&
		canaryOutcomeAllowed(value.ExpectedOutcome, value.FallbackOutcome, outcome) &&
		!value.Deadline.IsZero() && value.ReadyAt.Before(value.Deadline) &&
		finishedAt.Before(value.Deadline) && !value.ReadyAt.After(finishedAt) &&
		validSHA256(value.ReadyReceiptSHA256) && !value.ReadyAt.IsZero() &&
		validSHA256(value.FinalStateSHA256)
}

func validSHA256(value string) bool { return validOperationID(value) }

func validCanaryOutcome(value Outcome) bool {
	switch value {
	case OutcomeSucceeded, OutcomeRolledBack, OutcomeFailedSafe:
		return true
	default:
		return false
	}
}

func validCanaryFallback(primary, fallback Outcome) bool {
	if fallback == "" {
		return true
	}
	if !validCanaryOutcome(fallback) || fallback == primary {
		return false
	}
	switch primary {
	case OutcomeSucceeded:
		return fallback == OutcomeRolledBack || fallback == OutcomeFailedSafe
	case OutcomeRolledBack:
		return fallback == OutcomeFailedSafe
	default:
		return false
	}
}

func canaryOutcomeAllowed(primary, fallback, actual Outcome) bool {
	return validCanaryOutcome(actual) && (actual == primary || fallback != "" && actual == fallback)
}

func validTargetVersion(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\r\n\x00")
}

func capabilitySHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func canaryBaseBindingSHA256(control controlPlane, intent *Intent, canary *Canary) (string, error) {
	if intent == nil || canary == nil {
		return "", fail(CodeInvalid, errors.New("canary binding metadata is missing"))
	}
	payload := canaryBaseBindingPayload{
		Schema: canaryBaseBindingSchema, OperationID: intent.OperationID,
		Purpose: intent.Purpose, Resources: cloneStrings(intent.Resources), Owner: intent.Owner,
		BaseGeneration: intent.BaseGeneration, TargetGeneration: intent.TargetGeneration,
		StartedAt: intent.StartedAt.UTC(), ExpiresAt: intent.ExpiresAt.UTC(),
		DrainDeadline: intent.DrainDeadline.UTC(), MutationStarted: intent.MutationStarted.UTC(),
		ControlDirectoryIdentity: control.directoryID, CapabilitySHA256: canary.CapabilitySHA256,
		PlanSHA256: canary.PlanSHA256, SnapshotManifestSHA256: canary.SnapshotManifestSHA256,
		TargetBinarySHA256: canary.TargetBinarySHA256, TargetVersion: canary.TargetVersion,
		Port: canary.Port, Attempt: canary.Attempt,
		ExpectedOutcome: canary.ExpectedOutcome, FallbackOutcome: canary.FallbackOutcome,
		Deadline: canary.Deadline.UTC(),
	}
	return bindingPayloadSHA256(payload)
}

func canaryActiveBindingSHA256(record *Record) (string, error) {
	if record == nil || record.State != StateReopening || record.Intent == nil || record.Intent.Canary == nil {
		return "", fail(CodeInvalid, errors.New("active canary metadata is missing"))
	}
	return bindingPayloadSHA256(canaryActiveBindingPayload{
		Schema: canaryActiveBindingSchema, State: record.State, Generation: record.Generation,
		Revision: record.Revision, RecordUpdatedAt: record.UpdatedAt.UTC(),
		IntentUpdatedAt: record.Intent.UpdatedAt.UTC(), TransferPending: record.Intent.TransferPending,
		BaseBindingSHA256:  record.Intent.Canary.BaseBindingSHA256,
		ReadyBindingSHA256: record.Intent.Canary.ReadyBindingSHA256,
	})
}

func refreshCanaryActiveBinding(record *Record) error {
	if record == nil || record.State != StateReopening || record.Intent == nil || record.Intent.Canary == nil {
		return nil
	}
	binding, err := canaryActiveBindingSHA256(record)
	if err != nil {
		return err
	}
	record.Intent.Canary.ActiveBindingSHA256 = binding
	return nil
}

func canaryReadyBindingSHA256(canary *Canary) (string, error) {
	if canary == nil || canary.ReadyAt == nil {
		return "", fail(CodeInvalid, errors.New("canary readiness metadata is missing"))
	}
	return bindingPayloadSHA256(canaryReadyBindingPayload{
		Schema: canaryReadyBindingSchema, BaseBindingSHA256: canary.BaseBindingSHA256,
		ReadyReceiptSHA256: canary.ReadyReceiptSHA256, ReadyAt: canary.ReadyAt.UTC(),
	})
}

func canaryReceiptBindingSHA256(control controlPlane, record *Record) (string, error) {
	if record == nil || record.State != StateOpen || record.Receipt == nil || record.Receipt.Canary == nil {
		return "", fail(CodeInvalid, errors.New("canary receipt metadata is missing"))
	}
	receipt := record.Receipt
	canary := receipt.Canary
	return bindingPayloadSHA256(canaryReceiptBindingPayload{
		Schema: canaryReceiptBindingSchema, State: record.State,
		RecordGeneration: record.Generation, RecordRevision: record.Revision,
		RecordUpdatedAt:   record.UpdatedAt.UTC(),
		BaseBindingSHA256: canary.BaseBindingSHA256, ReadyBindingSHA256: canary.ReadyBindingSHA256,
		ActiveBindingSHA256: canary.ActiveBindingSHA256,
		OperationID:         receipt.OperationID, Generation: receipt.Generation,
		Resources: cloneStrings(receipt.Resources), ControlDirectoryIdentity: control.directoryID,
		CapabilitySHA256: canary.CapabilitySHA256, PlanSHA256: canary.PlanSHA256,
		SnapshotManifestSHA256: canary.SnapshotManifestSHA256,
		TargetBinarySHA256:     canary.TargetBinarySHA256, TargetVersion: canary.TargetVersion,
		Port: canary.Port, Owner: canary.Owner, Attempt: canary.Attempt,
		ExpectedOutcome: canary.ExpectedOutcome, FallbackOutcome: canary.FallbackOutcome,
		Deadline:           canary.Deadline.UTC(),
		ReadyReceiptSHA256: canary.ReadyReceiptSHA256, ReadyAt: canary.ReadyAt.UTC(),
		FinalStateSHA256: canary.FinalStateSHA256, Outcome: receipt.Outcome,
		FinishedAt: receipt.FinishedAt.UTC(),
	})
}

func bindingPayloadSHA256(payload any) (string, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", fail(CodeInvalid, err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validateRecordCanaryBinding(control controlPlane, record Record) error {
	if record.Intent != nil && record.Intent.Canary != nil {
		canary := record.Intent.Canary
		baseBinding, err := canaryBaseBindingSHA256(control, record.Intent, canary)
		if err != nil || baseBinding != canary.BaseBindingSHA256 {
			return fail(CodeStateCorrupt, errors.Join(errors.New("active canary binding is invalid"), err))
		}
		if canary.ReadyAt != nil {
			readyBinding, readyErr := canaryReadyBindingSHA256(canary)
			if readyErr != nil || readyBinding != canary.ReadyBindingSHA256 {
				return fail(CodeStateCorrupt, errors.Join(errors.New("active canary ready binding is invalid"), readyErr))
			}
		}
		if record.State == StateReopening {
			activeBinding, activeErr := canaryActiveBindingSHA256(&record)
			if activeErr != nil || activeBinding != canary.ActiveBindingSHA256 {
				return fail(CodeStateCorrupt, errors.Join(errors.New("active canary phase binding is invalid"), activeErr))
			}
		}
	}
	if record.Receipt == nil || record.Receipt.Canary == nil {
		return nil
	}
	receipt := record.Receipt.Canary
	intent := &Intent{
		OperationID: record.Receipt.OperationID, Resources: cloneStrings(record.Receipt.Resources),
		Purpose: receipt.Purpose, Owner: receipt.Owner,
		BaseGeneration: receipt.BaseGeneration, TargetGeneration: record.Receipt.Generation,
		StartedAt: receipt.StartedAt, ExpiresAt: receipt.ExpiresAt,
		DrainDeadline: receipt.DrainDeadline, MutationStarted: &receipt.MutationStarted,
	}
	canary := &Canary{
		Owner: receipt.Owner, CapabilitySHA256: receipt.CapabilitySHA256,
		PlanSHA256: receipt.PlanSHA256, SnapshotManifestSHA256: receipt.SnapshotManifestSHA256,
		TargetBinarySHA256: receipt.TargetBinarySHA256, TargetVersion: receipt.TargetVersion,
		Port: receipt.Port, Attempt: receipt.Attempt, ExpectedOutcome: receipt.ExpectedOutcome,
		FallbackOutcome: receipt.FallbackOutcome,
		Deadline:        receipt.Deadline,
	}
	baseBinding, err := canaryBaseBindingSHA256(control, intent, canary)
	if err != nil || baseBinding != receipt.BaseBindingSHA256 {
		return fail(CodeStateCorrupt, errors.Join(errors.New("canary receipt base binding is invalid"), err))
	}
	readyCanary := &Canary{
		BaseBindingSHA256:  receipt.BaseBindingSHA256,
		ReadyReceiptSHA256: receipt.ReadyReceiptSHA256, ReadyAt: &receipt.ReadyAt,
	}
	readyBinding, readyErr := canaryReadyBindingSHA256(readyCanary)
	if readyErr != nil || readyBinding != receipt.ReadyBindingSHA256 {
		return fail(CodeStateCorrupt, errors.Join(errors.New("canary receipt ready binding is invalid"), readyErr))
	}
	receiptBinding, receiptErr := canaryReceiptBindingSHA256(control, &record)
	if receiptErr != nil || receiptBinding != receipt.ReceiptBindingSHA256 {
		return fail(CodeStateCorrupt, errors.Join(errors.New("canary receipt binding is invalid"), receiptErr))
	}
	return nil
}

func cloneCanaryFence(fence CanaryFence) CanaryFence {
	fence.Resources = cloneStrings(fence.Resources)
	return fence
}

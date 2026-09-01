package maintenance

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	recordSchema = "ytqjk-maintenance-record/v2"

	CodeActive              = "MIGRATION_MAINTENANCE_ACTIVE"
	CodeGenerationConflict  = "MIGRATION_ADMISSION_GENERATION_CONFLICT"
	CodeStateCorrupt        = "MIGRATION_ADMISSION_STATE_CORRUPT"
	CodeWriterDrainTimeout  = "MIGRATION_WRITER_DRAIN_TIMEOUT"
	CodeRecoveryRequired    = "MIGRATION_RECOVERY_REQUIRED"
	CodeInvalid             = "MIGRATION_MAINTENANCE_INVALID"
	CodeLockFailed          = "MIGRATION_MAINTENANCE_LOCK_FAILED"
	CodeDurabilityUnknown   = "MIGRATION_MAINTENANCE_DURABILITY_UNKNOWN"
	CodeCommitResultUnknown = "MIGRATION_COMMIT_RESULT_UNKNOWN"

	StateOpen             State = "OPEN"
	StateDraining         State = "DRAINING"
	StateMaintenance      State = "MAINTENANCE"
	StateReopening        State = "REOPENING"
	StateRestoring        State = "RESTORING"
	StateRecoveryRequired State = "RECOVERY_REQUIRED"

	OutcomeAborted    Outcome = "ABORTED"
	OutcomeSucceeded  Outcome = "SUCCEEDED"
	OutcomeRolledBack Outcome = "ROLLED_BACK"
	OutcomeFailedSafe Outcome = "FAILED_SAFE"

	MaxExclusiveDuration = 15 * time.Minute
	MaxDrainTimeout      = 3 * time.Minute
	RecoveryReserve      = 3 * time.Minute
	defaultLockWait      = 5 * time.Second
)

var (
	errLockContended = errors.New("maintenance lock is contended")
	clockNow         = func() time.Time { return time.Now().UTC() }
)

type Error struct {
	Code string
	Err  error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *Error) Unwrap() error { return e.Err }

func IsCode(err error, code string) bool {
	var value *Error
	return errors.As(err, &value) && value.Code == code
}

type Scope struct {
	ControlRoot      string
	RuntimeRoot      string
	CodexRoot        string
	KnowledgeRoot    string
	ExtraRoots       []string
	ProspectiveRoots []string
	FilePaths        []string
}

type ExclusiveOptions struct {
	OperationID  string
	Purpose      string
	Duration     time.Duration
	DrainTimeout time.Duration
}

type Fence struct {
	Generation  uint64
	Revision    uint64
	OperationID string
	Resources   []string
	AcquiredAt  time.Time
}

type State string
type Outcome string

type Owner struct {
	PID      int    `json:"pid"`
	Identity string `json:"identity"`
}

type Intent struct {
	OperationID      string     `json:"operation_id"`
	Purpose          string     `json:"purpose"`
	Resources        []string   `json:"resources"`
	Owner            Owner      `json:"owner"`
	BaseGeneration   uint64     `json:"base_generation"`
	TargetGeneration uint64     `json:"target_generation"`
	StartedAt        time.Time  `json:"started_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	DrainDeadline    time.Time  `json:"drain_deadline"`
	MutationStarted  *time.Time `json:"mutation_started_at,omitempty"`
	TransferPending  bool       `json:"transfer_pending"`
	Canary           *Canary    `json:"canary,omitempty"`
	Recovery         *Recovery  `json:"recovery,omitempty"`
}

type Recovery struct {
	Code     string    `json:"code"`
	Cause    string    `json:"cause"`
	MarkedAt time.Time `json:"marked_at"`
}

type Receipt struct {
	OperationID string         `json:"operation_id"`
	Generation  uint64         `json:"generation"`
	Outcome     Outcome        `json:"outcome"`
	Resources   []string       `json:"resources"`
	FinishedAt  time.Time      `json:"finished_at"`
	Canary      *CanaryReceipt `json:"canary,omitempty"`
}

type Record struct {
	Schema     string    `json:"schema"`
	State      State     `json:"state"`
	Generation uint64    `json:"generation"`
	Revision   uint64    `json:"revision"`
	UpdatedAt  time.Time `json:"updated_at"`
	Intent     *Intent   `json:"intent,omitempty"`
	Receipt    *Receipt  `json:"receipt,omitempty"`
}

type controlPlane struct {
	root        string
	directory   string
	directoryID string
	guardPath   string
	guardID     string
	writersPath string
	writersID   string
	recordPath  string
	resources   []string
	bindings    []*resourceBinding
}

type heldLock struct {
	path   string
	unlock func() error
}

func fail(code string, err error) error { return &Error{Code: code, Err: err} }

func validOperationID(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validPurpose(value string) bool {
	if value == "" || len(value) > 64 || value != strings.ToUpper(value) {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func validOutcome(value Outcome) bool {
	switch value {
	case OutcomeAborted, OutcomeSucceeded, OutcomeRolledBack, OutcomeFailedSafe:
		return true
	default:
		return false
	}
}

func validRecoveryCode(value string) bool {
	return strings.HasPrefix(value, "MIGRATION_") && validPurpose(value)
}

func validRecoveryCause(value string) bool {
	return value != "" && len(value) <= 256 && value == strings.TrimSpace(value) &&
		!strings.ContainsAny(value, "\r\n")
}

func currentOwner() (Owner, error) {
	identity, err := processIdentity(os.Getpid())
	if err != nil {
		return Owner{}, err
	}
	return Owner{PID: os.Getpid(), Identity: identity}, nil
}

func ownerAlive(owner Owner) (bool, error) {
	alive, err := processAlive(owner.PID)
	if err != nil || !alive {
		return alive, err
	}
	identity, err := processIdentity(owner.PID)
	if err != nil {
		return true, err
	}
	return identity == owner.Identity, nil
}

func validOwner(owner Owner) bool {
	return owner.PID > 0 && owner.Identity != "" && len(owner.Identity) <= 256 &&
		owner.Identity == strings.TrimSpace(owner.Identity) && !strings.ContainsAny(owner.Identity, "\r\n")
}

func ownerEqual(left, right Owner) bool {
	return left.PID == right.PID && left.Identity == right.Identity
}

func joinUnlock(current error, locks ...heldLock) error {
	for index := len(locks) - 1; index >= 0; index-- {
		if locks[index].unlock == nil {
			continue
		}
		if err := locks[index].unlock(); err != nil {
			current = errors.Join(current, fail(CodeLockFailed, fmt.Errorf("unlock %s: %w", locks[index].path, err)))
		}
	}
	return current
}

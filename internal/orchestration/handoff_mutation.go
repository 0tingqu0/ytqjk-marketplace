package orchestration

import (
	"errors"
	"slices"
)

const (
	HandoffApplyCapability = "git:handoff-apply"
	HandoffApplyOperation  = "handoff:apply"
)

// ExecuteHandoffApply consumes a handoff-bound lease exactly once. The caller
// supplies the already-snapshotted concrete handler; no command or shell text
// is accepted by this API.
func (l *Ledger) ExecuteHandoffApply(
	token Attestation,
	currentSessionKey string,
	projectID string,
	bundleSHA256 string,
	paths []string,
	operation func() error,
) error {
	if err := ValidateHandoffApplyBinding(token, projectID, bundleSHA256, paths); err != nil {
		return err
	}
	return l.executeMutation(token, currentSessionKey, operation)
}

func ValidateHandoffApplyBinding(token Attestation, projectID, bundleSHA256 string, paths []string) error {
	canonical, err := canonicalScope(paths)
	if err != nil || len(canonical) != len(paths) ||
		token.Role != "git" || token.Capability != HandoffApplyCapability ||
		token.Operation != HandoffApplyOperation || token.ProjectID != projectID ||
		token.StagedHash != bundleSHA256 || !slices.Equal(token.ReadScope, canonical) ||
		!slices.Equal(token.WriteScope, canonical) {
		return errors.New("handoff mutation binding mismatch")
	}
	return nil
}

package upgrade

import (
	"errors"
)

type restoreRecoveryOutcome uint8

const (
	restoreRecoveryUnknown restoreRecoveryOutcome = iota
	restoreRecoveryCommitted
	restoreRecoveryRolledBack
)

func resolveCommittedRestoreError(
	plan Plan, identifier, manifestSHA256 string, cause error, guard *restoreGuard,
) error {
	outcome, err := recoverBoundRestoreJournalGuarded(plan, identifier, manifestSHA256, guard)
	if err != nil {
		return errors.Join(cause, err)
	}
	if outcome != restoreRecoveryCommitted {
		return failure("UPGRADE_SNAPSHOT_RESTORE_ROLLED_BACK", cause)
	}
	return nil
}

func recoverBoundRestoreJournal(
	plan Plan,
	identifier string,
	manifestSHA256 string,
) (outcome restoreRecoveryOutcome, returned error) {
	guard, err := acquireRestoreGuard(plan)
	if err != nil {
		return restoreRecoveryUnknown, err
	}
	defer func() { returned = errors.Join(returned, guard.release()) }()
	return recoverBoundRestoreJournalGuarded(plan, identifier, manifestSHA256, guard)
}

func recoverBoundRestoreJournalGuarded(
	plan Plan,
	identifier string,
	manifestSHA256 string,
	guard *restoreGuard,
) (restoreRecoveryOutcome, error) {
	if err := guard.require(plan); err != nil {
		return restoreRecoveryUnknown, err
	}
	if !hexDigestPattern.MatchString(identifier) || !hexDigestPattern.MatchString(manifestSHA256) {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore recovery identity is invalid"))
	}
	path := restoreJournalPath(plan.RuntimeRoot, identifier)
	journal, err := readRestoreJournalBound(guard, path)
	if err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if journal.SnapshotID != identifier || journal.ManifestSHA256 != manifestSHA256 {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", errors.New("restore recovery identity changed"))
	}
	return recoverValidatedRestoreJournal(plan, path, &journal, guard)
}

func recoverRestoreJournalFile(
	plan Plan, path string, guard *restoreGuard,
) (restoreRecoveryOutcome, error) {
	journal, err := readRestoreJournalBound(guard, path)
	if err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return recoverValidatedRestoreJournal(plan, path, &journal, guard)
}

func recoverValidatedRestoreJournal(
	plan Plan,
	path string,
	journal *restoreJournal,
	guard *restoreGuard,
) (restoreRecoveryOutcome, error) {
	if err := guard.require(plan); err != nil {
		return restoreRecoveryUnknown, err
	}
	if err := validateRestoreJournal(plan, path, *journal); err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if err := verifyRestoreJournalBinding(path, journal); err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	outcome := restoreRecoveryUnknown
	var err error
	switch {
	case journal.Decision == restoreDecisionCommit &&
		(journal.Phase == "COMMITTED" || journal.Phase == "COMMITTED_CLEANING"):
		outcome = restoreRecoveryCommitted
		err = recoverCommittedJournal(path, journal)
	case journal.Decision == restoreDecisionRollback && rollbackRestorePhase(journal.Phase):
		outcome = restoreRecoveryRolledBack
		err = recoverRollbackJournal(path, journal)
	default:
		err = markRestoreRecoveryRequired(path, journal, errors.New("unknown restore journal decision"))
	}
	if err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if err := verifyJournalTargets(journal.Items, outcome == restoreRecoveryRolledBack); err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	if err := verifyRestoreJournalRemoved(guard, path); err != nil {
		return restoreRecoveryUnknown, failure("UPGRADE_RECOVERY_REQUIRED", err)
	}
	return outcome, nil
}

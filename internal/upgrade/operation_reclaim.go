package upgrade

func reclaimOperationOwnerFromStoppedChild(runtimeRoot, operationID, phase string, childPID int) error {
	return withOperationGuard(runtimeRoot, func() error {
		record, exists, err := readOperationRecord(runtimeRoot)
		if err != nil {
			return err
		}
		owner, ownerErr := currentOperationOwner()
		if ownerErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", ownerErr)
		}
		alive, aliveErr := operationProcessAlive(childPID)
		if aliveErr != nil {
			return failure("UPGRADE_OPERATION_LOCK_FAILED", aliveErr)
		}
		if !exists || !record.Active || record.OperationID != operationID ||
			record.Phase != phase || record.OwnerPID != childPID || alive {
			return failure("UPGRADE_RECOVERY_REQUIRED", nil)
		}
		previous := record
		record.OwnerPID = owner.PID
		record.OwnerIdentity = owner.Identity
		renewOperationRecord(&record)
		return replaceOperationRecord(runtimeRoot, &previous, record)
	})
}

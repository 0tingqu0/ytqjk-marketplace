package cli

import (
	stdcontext "context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/orchestration"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
)

func (context commandContext) orchestration(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "start-run", "show-run", "transition", "grant", "attest", "verify")
	if err != nil {
		return err
	}
	flags := quietFlags("orchestration " + command)
	knowledgeValue := flags.String("knowledge-root", "", "knowledge root")
	databaseValue := flags.String("database", "", "identity database")
	keyValue := flags.String("key-file", "", "identity key")
	projectID := flags.String("project-id", "", "project identity")
	objectiveHash := flags.String("objective-hash", "", "SHA-256 objective hash")
	sessionKey := flags.String("session-key", "", "session key")
	runID := flags.String("run-id", "", "run identifier")
	role := flags.String("role", "", "director, controller, worker, reviewer, or git")
	state := flags.String("state", "", "target lifecycle state")
	expectedVersion := flags.Int("expected-version", -1, "expected run version")
	mutation := flags.Bool("mutation", false, "grant or attest mutation authority")
	operation := flags.String("operation", "", "integrated mutation operation")
	stagedHash := flags.String("staged-hash", "", "reviewed staged snapshot SHA-256")
	leaseSeconds := flags.Int("lease-seconds", orchestration.DefaultLeaseSeconds, "lease duration")
	tokenFile := flags.String("token-file", "", "attestation JSON file")
	var reads, writes, capabilities stringsFlag
	flags.Var(&reads, "read", "read scope (repeatable)")
	flags.Var(&writes, "write", "write scope (repeatable)")
	flags.Var(&capabilities, "capability", "capability (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		return err
	}
	if *databaseValue == "" {
		*databaseValue = filepath.Join(knowledgeRoot, "service", "orchestration.sqlite3")
	}
	if *keyValue == "" {
		*keyValue = filepath.Join(knowledgeRoot, "service", "orchestration.key")
	}
	fileScope, err := knowledgeFileScope(knowledgeRoot, *databaseValue, *keyValue)
	if err != nil {
		return err
	}
	return context.withSharedScopeOutput(stdcontext.Background(), fileScope, func(_ stdcontext.Context, admittedCommand commandContext) (result error) {
		ledger, databaseID, err := orchestration.Open(*databaseValue, *keyValue)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, ledger.Close()) }()
		switch command {
		case "start-run":
			if *projectID == "" || *objectiveHash == "" || *sessionKey == "" {
				return errors.New("--project-id, --objective-hash, and --session-key are required")
			}
			run, err := ledger.StartRun(*projectID, strings.ToLower(*objectiveHash), *sessionKey, *sessionKey)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "run started", "database_id": databaseID, "run": run})
		case "show-run":
			if *runID == "" {
				return errors.New("--run-id is required")
			}
			run, err := ledger.Run(*runID)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "run": run})
		case "transition":
			if *runID == "" || *sessionKey == "" || *state == "" || *expectedVersion < 0 {
				return errors.New("--run-id, --session-key, --state, and --expected-version are required")
			}
			run, err := ledger.Transition(*runID, *sessionKey, strings.ToUpper(*state), *expectedVersion)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "run": run})
		case "grant":
			if *runID == "" || *sessionKey == "" || *role == "" {
				return errors.New("--run-id, --session-key, and --role are required")
			}
			grant := orchestration.Grant{RunID: *runID, SessionKey: *sessionKey, Role: *role, ReadScope: reads, WriteScope: writes, Mutation: *mutation, Capabilities: capabilities}
			if err := ledger.Grant(grant, *sessionKey); err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "GRANTED", "grant": grant})
		case "attest":
			if *runID == "" || *sessionKey == "" || *role == "" {
				return errors.New("--run-id, --session-key, and --role are required")
			}
			var token orchestration.Attestation
			if *mutation {
				if *operation != orchestration.HandoffApplyOperation {
					return errors.New("mutation attestations require --operation handoff:apply")
				}
				token, err = ledger.AttestHandoffApply(*runID, *sessionKey, *role, reads, writes, strings.ToLower(*stagedHash), *leaseSeconds)
			} else {
				if *operation != "" {
					return errors.New("read-only attestations cannot bind an operation")
				}
				token, err = ledger.Attest(*runID, *sessionKey, *role, reads, writes, false, strings.ToLower(*stagedHash), *leaseSeconds)
			}
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "ATTESTED", "attestation": token})
		case "verify":
			if *tokenFile == "" || *sessionKey == "" {
				return errors.New("--token-file and --session-key are required")
			}
			token, err := readAttestation(*tokenFile)
			if err != nil {
				return err
			}
			if err := ledger.Verify(token, *sessionKey); err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "VERIFIED", "lease_id": token.LeaseID})
		}
		return errors.New("unreachable orchestration command")
	})
}

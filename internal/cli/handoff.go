package cli

import (
	stdcontext "context"
	"errors"
	"path/filepath"

	"github.com/0tingqu0/ytqjk-marketplace/internal/handoff"
	"github.com/0tingqu0/ytqjk-marketplace/internal/orchestration"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func (context commandContext) handoff(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "export", "apply")
	if err != nil {
		return err
	}
	flags := quietFlags("handoff " + command)
	repo := flags.String("repo", "", "Git worktree")
	bundle := flags.String("bundle", "", "bundle path outside worktree")
	knowledgeValue := flags.String("knowledge-root", "", "knowledge root")
	databaseValue := flags.String("orchestration-database", "", "identity database")
	keyValue := flags.String("orchestration-key-file", "", "identity key")
	sessionKey := flags.String("session-key", "", "session key")
	tokenFile := flags.String("token-file", "", "mutation attestation JSON file")
	var paths stringsFlag
	flags.Var(&paths, "path", "allowlisted repository path (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if err := requireNoPositionals(flags.Args()); err != nil {
		return err
	}
	if *repo == "" || *bundle == "" {
		return errors.New("--repo and --bundle are required")
	}
	if command == "export" {
		result, err := handoff.Export(*repo, *bundle, paths)
		if err != nil {
			return err
		}
		return context.write(struct {
			OK bool `json:"ok"`
			handoff.ExportResult
		}{OK: true, ExportResult: result})
	}
	if len(paths) != 0 {
		return errors.New("--path is only valid for export")
	}
	if *sessionKey == "" || *tokenFile == "" {
		return errors.New("handoff apply requires --session-key and --token-file")
	}
	token, err := readAttestation(*tokenFile)
	if err != nil {
		return err
	}
	identity, err := rag.IdentifyProject(*repo)
	if err != nil {
		return err
	}
	preflight, err := readHandoffMutationBinding(*bundle)
	if err != nil {
		return err
	}
	if err := orchestration.ValidateHandoffApplyBinding(token, identity.ID, preflight.bundleSHA256, preflight.paths); err != nil {
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
	fileScope, err := handoffApplyScope(
		knowledgeRoot, identity.Root, *bundle, *databaseValue, *keyValue,
	)
	if err != nil {
		return err
	}
	return context.withSharedScopeOutput(stdcontext.Background(), fileScope, func(_ stdcontext.Context, admittedCommand commandContext) (actionErr error) {
		ledger, _, err := orchestration.Open(*databaseValue, *keyValue)
		if err != nil {
			return err
		}
		defer func() { actionErr = errors.Join(actionErr, ledger.Close()) }()
		var result handoff.ApplyResult
		actionErr = ledger.ExecuteHandoffApply(token, *sessionKey, identity.ID, preflight.bundleSHA256, preflight.paths, func() error {
			snapshot, cleanup, err := snapshotHandoffBundle(*bundle)
			if err != nil {
				return err
			}
			defer cleanup()
			binding, err := readHandoffMutationBinding(snapshot)
			if err != nil {
				return err
			}
			if err := orchestration.ValidateHandoffApplyBinding(token, identity.ID, binding.bundleSHA256, binding.paths); err != nil {
				return err
			}
			result, err = applyHandoff(*repo, snapshot)
			return err
		})
		if actionErr != nil {
			return actionErr
		}
		return admittedCommand.write(struct {
			OK bool `json:"ok"`
			handoff.ApplyResult
		}{OK: true, ApplyResult: result})
	})
}

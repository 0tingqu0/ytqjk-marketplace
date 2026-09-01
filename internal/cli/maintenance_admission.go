package cli

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
)

var maintenanceControlRoot = platform.MaintenanceControlRoot

func knowledgeFileScope(knowledgeRoot string, paths ...string) (maintenance.Scope, error) {
	return maintenance.Scope{
		ProspectiveRoots: []string{knowledgeRoot},
		FilePaths:        append([]string(nil), paths...),
	}, nil
}

func withSharedKnowledge(
	ctx context.Context,
	knowledgeRoot string,
	action func(context.Context) (any, error),
) (any, error) {
	return withSharedScope(ctx, maintenance.Scope{ProspectiveRoots: []string{knowledgeRoot}}, action)
}

func withSharedScope(
	ctx context.Context,
	scope maintenance.Scope,
	action func(context.Context) (any, error),
) (any, error) {
	if ctx == nil || action == nil {
		return nil, &maintenance.Error{Code: maintenance.CodeInvalid}
	}
	controlRoot, err := maintenanceControlRoot()
	if err != nil {
		return nil, err
	}
	if err := maintenance.BootstrapControlRoot(ctx, controlRoot); err != nil {
		return nil, err
	}
	scope.ControlRoot = controlRoot
	permit, err := maintenance.AcquireShared(ctx, scope)
	if err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = permit.Release()
		}
	}()
	admittedContext, err := maintenance.WithShared(ctx, permit)
	if err != nil {
		return nil, err
	}
	var result any
	invoked := false
	err = permit.Commit(func(maintenance.Fence) error {
		invoked = true
		var actionErr error
		result, actionErr = action(admittedContext)
		return actionErr
	})
	committed = true
	if err != nil {
		return nil, err
	}
	if !invoked {
		return nil, errors.New("maintenance admission action was not invoked")
	}
	return result, nil
}

func (command commandContext) withSharedKnowledgeOutput(
	ctx context.Context,
	knowledgeRoot string,
	action func(context.Context, commandContext) error,
) error {
	return command.withSharedScopeOutput(
		ctx, maintenance.Scope{ProspectiveRoots: []string{knowledgeRoot}}, action,
	)
}

func (command commandContext) withSharedScopeOutput(
	ctx context.Context,
	scope maintenance.Scope,
	action func(context.Context, commandContext) error,
) error {
	if action == nil {
		return &maintenance.Error{Code: maintenance.CodeInvalid}
	}
	var staged bytes.Buffer
	admittedCommand := command
	admittedCommand.out = &staged
	_, err := withSharedScope(ctx, scope, func(admittedContext context.Context) (any, error) {
		return nil, action(admittedContext, admittedCommand)
	})
	if err != nil {
		return err
	}
	_, err = io.Copy(command.out, &staged)
	return err
}

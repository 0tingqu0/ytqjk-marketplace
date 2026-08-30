package cli

import (
	"errors"

	"github.com/0tingqu0/ytqjk-marketplace/internal/handoff"
)

func (context commandContext) handoff(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "export", "apply")
	if err != nil {
		return err
	}
	flags := quietFlags("handoff " + command)
	repo := flags.String("repo", "", "Git worktree")
	bundle := flags.String("bundle", "", "bundle path outside worktree")
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
	result, err := handoff.Apply(*repo, *bundle)
	if err != nil {
		return err
	}
	return context.write(struct {
		OK bool `json:"ok"`
		handoff.ApplyResult
	}{OK: true, ApplyResult: result})
}

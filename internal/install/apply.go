package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type ApplyOptions struct {
	Plan       Plan
	Target     string
	SourceRoot string
	CodexRoot  string
	Binary     string
}

type ApplyResult struct {
	Status           string       `json:"status"`
	Changed          bool         `json:"changed"`
	ExternalCommands [][]string   `json:"external_commands"`
	Snapshot         *string      `json:"snapshot"`
	Cleanup          string       `json:"cleanup"`
	StagingResidue   bool         `json:"staging_residue"`
	CleanupAction    *string      `json:"cleanup_action"`
	CodexPlugins     PluginResult `json:"codex_plugins"`
	RemovedPaths     []string     `json:"removed_paths,omitempty"`
	Rollback         string       `json:"rollback,omitempty"`
}

type ApplyError struct {
	Message             string
	Rollback            string
	FailedAction        string
	FailedCompensations []string
	Cleanup             string
	StagingResidue      bool
	CleanupAction       string
}

func (e *ApplyError) Error() string { return e.Message }

type replacement struct {
	Destination string
	Backup      string
}

func Apply(options ApplyOptions) (ApplyResult, error) {
	target, err := filepath.Abs(options.Target)
	if err != nil {
		return ApplyResult{}, err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return ApplyResult{}, err
	}
	state, err := safeio.Contained(target, filepath.Join(target, ".ytqjk-install"))
	if err != nil {
		return ApplyResult{}, err
	}
	snapshot, err := safeio.Contained(target, filepath.Join(state, "snapshots", fmt.Sprintf("%d", time.Now().UnixNano())))
	if err != nil {
		return ApplyResult{}, err
	}
	plan, err := BuildPlan(options.Plan.Mode, options.SourceRoot, target)
	if err != nil {
		return ApplyResult{}, err
	}
	var commands [][]string
	var compensated []Action
	failedAction := "target-root-files"
	for _, action := range plan.Actions {
		if action.Kind != "codex" {
			continue
		}
		failedAction = action.Name
		present, checkErr := externalState(action, target)
		if checkErr != nil {
			compensateActions(compensated, target)
			return ApplyResult{}, applyFailure(checkErr, "NOT_NEEDED", failedAction, nil)
		}
		if present {
			continue
		}
		if _, runErr := runExternal(action.Command, target, 10*time.Minute); runErr != nil {
			failures := compensateActions(compensated, target)
			return ApplyResult{}, applyFailure(runErr, rollbackStatus(failures), failedAction, failures)
		}
		commands = append(commands, action.Command)
		compensated = append(compensated, action)
	}

	copies := append([]CopySpec{}, plan.Copies...)
	stageRoot := ""
	for _, action := range plan.Actions {
		if action.Kind != "third-party-stage" {
			continue
		}
		failedAction = action.Name
		staged, root, stageErr := stageExternalSkill(target, action.Command, "grill-me")
		stageRoot = root
		if stageErr != nil {
			failures := compensateActions(compensated, target)
			return ApplyResult{}, applyFailure(stageErr, rollbackStatus(failures), failedAction, failures)
		}
		copies = append(copies, CopySpec{Source: staged, Destination: filepath.Join(target, "skills", "grill-me"), Name: "grill-me"})
	}
	if stageRoot != "" {
		defer os.RemoveAll(stageRoot)
	}

	var replacements []replacement
	changed := false
	rollbackFiles := func() []string {
		var failures []string
		for index := len(replacements) - 1; index >= 0; index-- {
			item := replacements[index]
			if err := os.RemoveAll(item.Destination); err != nil {
				failures = append(failures, fmt.Sprintf("target-root-item:%d", index+1))
				continue
			}
			if item.Backup != "" {
				if err := os.MkdirAll(filepath.Dir(item.Destination), 0o755); err != nil {
					failures = append(failures, fmt.Sprintf("target-root-item:%d", index+1))
					continue
				}
				if err := os.Rename(item.Backup, item.Destination); err != nil {
					failures = append(failures, fmt.Sprintf("target-root-item:%d", index+1))
				}
			}
		}
		_ = os.RemoveAll(snapshot)
		return failures
	}
	for _, item := range copies {
		destination, containedErr := safeio.Contained(target, item.Destination)
		if containedErr != nil {
			failures := append(rollbackFiles(), compensateActions(compensated, target)...)
			return ApplyResult{}, applyFailure(containedErr, rollbackStatus(failures), failedAction, failures)
		}
		sourceHash, hashErr := safeio.TreeHash(item.Source)
		if hashErr != nil {
			failures := append(rollbackFiles(), compensateActions(compensated, target)...)
			return ApplyResult{}, applyFailure(hashErr, rollbackStatus(failures), failedAction, failures)
		}
		if destinationHash, destinationErr := safeio.TreeHash(destination); destinationErr == nil && destinationHash == sourceHash {
			continue
		}
		backup := ""
		if _, statErr := os.Lstat(destination); statErr == nil {
			relative, _ := filepath.Rel(target, destination)
			backup = filepath.Join(snapshot, relative)
			if err := os.MkdirAll(filepath.Dir(backup), 0o755); err != nil {
				failures := append(rollbackFiles(), compensateActions(compensated, target)...)
				return ApplyResult{}, applyFailure(err, rollbackStatus(failures), failedAction, failures)
			}
			if err := os.Rename(destination, backup); err != nil {
				failures := append(rollbackFiles(), compensateActions(compensated, target)...)
				return ApplyResult{}, applyFailure(err, rollbackStatus(failures), failedAction, failures)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			failures := append(rollbackFiles(), compensateActions(compensated, target)...)
			return ApplyResult{}, applyFailure(statErr, rollbackStatus(failures), failedAction, failures)
		}
		replacements = append(replacements, replacement{Destination: destination, Backup: backup})
		if err := safeio.CopyTree(item.Source, destination); err != nil {
			failures := append(rollbackFiles(), compensateActions(compensated, target)...)
			return ApplyResult{}, applyFailure(err, rollbackStatus(failures), failedAction, failures)
		}
		changed = true
	}

	plugins := PluginResult{StablePaths: []string{}}
	if plan.Mode == "all" || plan.Mode == "codex-only" || plan.Mode == "codex-stable-only" {
		failedAction = "codex-stable-paths"
		plugins, err = MaterializePlugins(options.CodexRoot, options.SourceRoot, options.Binary)
		if err != nil {
			failures := append(rollbackFiles(), compensateActions(compensated, target)...)
			return ApplyResult{}, applyFailure(err, rollbackStatus(failures), failedAction, failures)
		}
	}
	var snapshotValue *string
	if _, err := os.Stat(snapshot); err == nil {
		relative, _ := filepath.Rel(target, snapshot)
		text := filepath.ToSlash(relative)
		snapshotValue = &text
	}
	return ApplyResult{
		Status: "APPLIED", Changed: changed || plugins.Changed || len(commands) > 0,
		ExternalCommands: commands, Snapshot: snapshotValue, Cleanup: "SUCCEEDED",
		CodexPlugins: plugins,
	}, nil
}

func Uninstall(options ApplyOptions) (ApplyResult, error) {
	target, err := filepath.Abs(options.Target)
	if err != nil {
		return ApplyResult{}, err
	}
	plan, err := BuildUninstallPlan(options.Plan.Mode, target)
	if err != nil {
		return ApplyResult{}, err
	}
	var commands [][]string
	for _, action := range plan.Actions {
		present, checkErr := externalState(action, target)
		if checkErr != nil {
			return ApplyResult{}, applyFailure(checkErr, "NOT_APPLICABLE", action.Name, nil)
		}
		if !present {
			continue
		}
		if _, runErr := runExternal(action.Command, target, 10*time.Minute); runErr != nil {
			return ApplyResult{}, applyFailure(runErr, "NOT_APPLICABLE", action.Name, nil)
		}
		commands = append(commands, action.Command)
	}
	var removed []string
	for _, path := range plan.Removals {
		contained, containErr := safeio.Contained(target, path)
		if containErr != nil {
			return ApplyResult{}, containErr
		}
		if _, statErr := os.Lstat(contained); statErr == nil {
			if err := os.RemoveAll(contained); err != nil {
				return ApplyResult{}, err
			}
			relative, _ := filepath.Rel(target, contained)
			removed = append(removed, filepath.ToSlash(relative))
		}
	}
	if plan.Mode == "all" || plan.Mode == "codex-only" {
		stable, removeErr := RemoveManagedPlugins(options.CodexRoot)
		if removeErr != nil {
			return ApplyResult{}, removeErr
		}
		removed = append(removed, stable...)
	}
	return ApplyResult{
		Status: "UNINSTALLED", Changed: len(commands) > 0 || len(removed) > 0,
		ExternalCommands: commands, Cleanup: "SUCCEEDED", RemovedPaths: removed,
		Rollback: "NOT_APPLICABLE", CodexPlugins: PluginResult{StablePaths: []string{}},
	}, nil
}

func stageExternalSkill(target string, command []string, expected string) (string, string, error) {
	root := filepath.Join(target, ".ytqjk-install", "staging", fmt.Sprintf("%d", time.Now().UnixNano()))
	work := filepath.Join(root, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return "", root, err
	}
	if _, err := runExternal(command, work, 10*time.Minute); err != nil {
		_ = os.RemoveAll(root)
		return "", root, err
	}
	var matches []string
	err := filepath.WalkDir(work, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() == "SKILL.md" && filepath.Base(filepath.Dir(path)) == expected {
			matches = append(matches, filepath.Dir(path))
		}
		return nil
	})
	if err != nil || len(matches) != 1 {
		_ = os.RemoveAll(root)
		return "", root, errors.New("staged skill output is invalid")
	}
	return matches[0], root, nil
}

func externalState(action Action, directory string) (bool, error) {
	output, err := runExternal(action.Check, directory, 2*time.Minute)
	if err != nil {
		return false, err
	}
	var value any
	if err := json.Unmarshal([]byte(output), &value); err != nil {
		return false, errors.New("state check returned invalid JSON")
	}
	return containsIdentity(value, action.Identity), nil
}

func containsIdentity(value any, identity string) bool {
	switch item := value.(type) {
	case string:
		return item == identity
	case []any:
		for _, child := range item {
			if containsIdentity(child, identity) {
				return true
			}
		}
	case map[string]any:
		if item["name"] == identity || item["id"] == identity {
			return true
		}
		for _, child := range item {
			if containsIdentity(child, identity) {
				return true
			}
		}
	}
	return false
}

func runExternal(arguments []string, directory string, timeout time.Duration) (string, error) {
	if len(arguments) == 0 {
		return "", errors.New("external command is empty")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, arguments[0], arguments[1:]...)
	command.Dir = directory
	command.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", errors.New("external command timed out")
		}
		message := strings.TrimSpace(stderr.String())
		if len(message) > 512 {
			message = message[:512]
		}
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("external command failed: %s", message)
	}
	return stdout.String(), nil
}

func compensateActions(actions []Action, target string) []string {
	var failures []string
	for index := len(actions) - 1; index >= 0; index-- {
		action := actions[index]
		if _, err := runExternal(action.Compensate, target, 5*time.Minute); err != nil {
			failures = append(failures, action.Name)
		}
	}
	return failures
}

func rollbackStatus(failures []string) string {
	if len(failures) > 0 {
		return "FAILED"
	}
	return "SUCCEEDED"
}

func applyFailure(cause error, rollback, action string, failures []string) *ApplyError {
	return &ApplyError{
		Message: fmt.Sprintf("installation failed [%T]", cause), Rollback: rollback,
		FailedAction: action, FailedCompensations: failures, Cleanup: "SUCCEEDED",
	}
}

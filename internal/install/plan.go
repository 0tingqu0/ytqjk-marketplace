package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
)

const Version = buildinfo.Version

var PublicModes = []string{"all", "codex-only", "ide-only", "knowledge-only"}

type Action struct {
	Kind                 string   `json:"kind,omitempty"`
	Name                 string   `json:"name"`
	Check                []string `json:"check,omitempty"`
	Identity             string   `json:"identity,omitempty"`
	Command              []string `json:"command"`
	Compensate           []string `json:"compensate,omitempty"`
	Verification         string   `json:"verification,omitempty"`
	ConfirmationRequired bool     `json:"confirmation_required,omitempty"`
	Scope                string   `json:"scope,omitempty"`
}

type CopySpec struct {
	Source      string
	Destination string
	Name        string
}

type Plan struct {
	Mode     string
	Actions  []Action
	Copies   []CopySpec
	Removals []string
}

func NormalizeMode(mode string) (string, error) {
	for _, candidate := range append(append([]string{}, PublicModes...), "codex-stable-only") {
		if mode == candidate {
			return mode, nil
		}
	}
	return "", errors.New("unsupported mode")
}

func NormalizeUpdateMode(mode, target, codexImport, projectBootstrap, dashboardService string) (string, error) {
	normalized, err := NormalizeMode(mode)
	if err != nil {
		return "", err
	}
	if normalized == "codex-only" && target != "" && codexImport == "off" &&
		projectBootstrap == "off" && dashboardService == "off" {
		absolute, absErr := filepath.Abs(target)
		if absErr == nil {
			for current := absolute; ; current = filepath.Dir(current) {
				if strings.HasPrefix(filepath.Base(current), "ytqjk-update-") {
					return "codex-stable-only", nil
				}
				parent := filepath.Dir(current)
				if parent == current {
					break
				}
			}
		}
	}
	return normalized, nil
}

func BuildPlan(mode, sourceRoot, target string) (Plan, error) {
	mode, err := NormalizeMode(mode)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Mode: mode}
	if mode == "all" || mode == "ide-only" {
		for _, name := range []string{"ytqjk", "caveman"} {
			plan.Copies = append(plan.Copies, CopySpec{
				Source:      filepath.Join(sourceRoot, "plugins", "ytqjk-agentic-orchestrator", "skills", name),
				Destination: filepath.Join(target, "skills", name),
				Name:        name,
			})
		}
	}
	if mode == "all" || mode == "knowledge-only" {
		plan.Copies = append(plan.Copies, CopySpec{
			Source:      filepath.Join(sourceRoot, "plugins", "ytqjk-knowledge", "skills", "ytqjk-knowledge"),
			Destination: filepath.Join(target, "skills", "ytqjk-knowledge"),
			Name:        "ytqjk-knowledge",
		})
	}
	if mode == "all" || mode == "codex-only" || mode == "codex-stable-only" {
		plan.Actions = append(plan.Actions, CodexActions()...)
	}
	if (mode == "all" || mode == "ide-only") && !HasGrill(target) {
		plan.Actions = append(plan.Actions, Action{
			Kind: "third-party-stage", Name: "skill:grill-me",
			Command:      []string{"npx", "--yes", "skills@latest", "add", "mattpocock/skills", "--agent", "codex", "--skill", "grill-me", "--yes", "--copy"},
			Verification: "unverified", ConfirmationRequired: true, Scope: "target-root staging",
		})
	}
	return plan, nil
}

func BuildUninstallPlan(mode, target string) (Plan, error) {
	mode, err := NormalizeMode(mode)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{Mode: mode}
	if mode == "all" || mode == "codex-only" {
		for _, action := range CodexActions() {
			action.Command, action.Compensate = action.Compensate, action.Command
			plan.Actions = append(plan.Actions, action)
		}
	}
	if mode == "all" || mode == "ide-only" {
		plan.Removals = append(plan.Removals, filepath.Join(target, "skills", "ytqjk"), filepath.Join(target, "skills", "caveman"))
	}
	if mode == "all" || mode == "knowledge-only" {
		plan.Removals = append(plan.Removals, filepath.Join(target, "skills", "ytqjk-knowledge"))
	}
	return plan, nil
}

func CodexActions() []Action {
	return []Action{
		{
			Kind: "codex", Name: "marketplace:ytqjk",
			Check: []string{"codex", "plugin", "marketplace", "list", "--json"}, Identity: "ytqjk",
			Command:    []string{"codex", "plugin", "marketplace", "add", "0tingqu0/ytqjk-marketplace"},
			Compensate: []string{"codex", "plugin", "marketplace", "remove", "ytqjk"},
		},
		{
			Kind: "codex", Name: "plugin:orchestrator",
			Check: []string{"codex", "plugin", "list", "--json"}, Identity: "ytqjk-agentic-orchestrator",
			Command:    []string{"codex", "plugin", "add", "ytqjk-agentic-orchestrator@ytqjk"},
			Compensate: []string{"codex", "plugin", "remove", "ytqjk-agentic-orchestrator@ytqjk"},
		},
		{
			Kind: "codex", Name: "plugin:knowledge",
			Check: []string{"codex", "plugin", "list", "--json"}, Identity: "ytqjk-knowledge",
			Command:    []string{"codex", "plugin", "add", "ytqjk-knowledge@ytqjk"},
			Compensate: []string{"codex", "plugin", "remove", "ytqjk-knowledge@ytqjk"},
		},
	}
}

func HasGrill(target string) bool {
	if strings.TrimSpace(target) == "" || strings.Contains(target, "<target-root>") {
		return false
	}
	found := false
	_ = filepath.WalkDir(target, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.Name() == "SKILL.md" && filepath.Base(filepath.Dir(path)) == "grill-me" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

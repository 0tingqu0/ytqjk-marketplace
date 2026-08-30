package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	guidanceStart = "<!-- ytqjk-knowledge managed:start -->"
	guidanceEnd   = "<!-- ytqjk-knowledge managed:end -->"
)

type GuidanceResult struct {
	Status  string `json:"status"`
	Changed bool   `json:"changed"`
	Target  string `json:"target,omitempty"`
}

func ConfigureGuidance(codexRoot, knowledgeRoot, mode, action, binary string) GuidanceResult {
	if mode != "all" && mode != "codex-only" {
		return GuidanceResult{Status: "SKIPPED_MODE"}
	}
	if action == "uninstall" {
		changed, err := removeGuidance(codexRoot)
		if err != nil {
			return GuidanceResult{Status: "FAILED"}
		}
		return GuidanceResult{Status: "REMOVED", Changed: changed}
	}
	changed, target, err := installGuidance(codexRoot, knowledgeRoot, binary)
	if err != nil {
		return GuidanceResult{Status: "FAILED"}
	}
	return GuidanceResult{Status: "INSTALLED", Changed: changed, Target: target}
}

func installGuidance(codexRoot, knowledgeRoot, binary string) (bool, string, error) {
	if binary == "" {
		return false, "", errors.New("YTQJK binary path is empty")
	}
	paths := []string{filepath.Join(codexRoot, "AGENTS.md"), filepath.Join(codexRoot, "AGENTS.override.md")}
	contents := map[string]string{}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, "", err
		}
		cleaned, cleanErr := stripGuidance(string(data))
		if cleanErr != nil {
			return false, "", cleanErr
		}
		contents[path] = cleaned
	}
	target := paths[0]
	if strings.TrimSpace(contents[paths[1]]) != "" {
		target = paths[1]
	}
	command := commandText(binary, knowledgeRoot)
	block := guidanceStart + "\n## YTQJK project knowledge\n\n" +
		"- Before reading or changing project files, resolve the project root with `git rev-parse --show-toplevel`; use the current directory outside Git.\n" +
		"- When `CODEX_THREAD_ID` is available, query the local knowledge cache with a task-specific query. Never invent a session ID.\n\n" +
		"  `" + command + "`\n\n" +
		"- Report the complete `KNOWLEDGE_RECEIPT`; a miss is valid and is not a knowledge hit.\n" + guidanceEnd + "\n"
	updated := strings.TrimSpace(contents[target])
	if updated != "" {
		updated += "\n\n"
	}
	updated += block
	contents[target] = updated
	changed := false
	for _, path := range paths {
		before, _ := os.ReadFile(path)
		if string(before) == contents[path] {
			continue
		}
		if err := safeio.AtomicWrite(path, []byte(contents[path]), 0o600); err != nil {
			return false, "", err
		}
		changed = true
	}
	return changed, filepath.Base(target), nil
}

func removeGuidance(codexRoot string) (bool, error) {
	changed := false
	for _, name := range []string{"AGENTS.md", "AGENTS.override.md"} {
		path := filepath.Join(codexRoot, name)
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		cleaned, err := stripGuidance(string(data))
		if err != nil {
			return false, err
		}
		if cleaned != string(data) {
			if err := safeio.AtomicWrite(path, []byte(cleaned), 0o600); err != nil {
				return false, err
			}
			changed = true
		}
	}
	return changed, nil
}

func stripGuidance(text string) (string, error) {
	starts, ends := strings.Count(text, guidanceStart), strings.Count(text, guidanceEnd)
	if starts != ends || starts > 1 {
		return "", errors.New("managed guidance markers are invalid")
	}
	if starts == 0 {
		return text, nil
	}
	left, remainder, _ := strings.Cut(text, guidanceStart)
	_, right, _ := strings.Cut(remainder, guidanceEnd)
	cleaned := strings.TrimSpace(strings.TrimRight(left, " \t\r\n") + "\n\n" + strings.TrimLeft(right, " \t\r\n"))
	if cleaned != "" {
		cleaned += "\n"
	}
	return cleaned, nil
}

func commandText(binary, knowledgeRoot string) string {
	if runtime.GOOS == "windows" {
		quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" }
		return fmt.Sprintf("& %s session query '<task-related-query>' --knowledge-root %s --project-root '<project-root>' --session-id $env:CODEX_THREAD_ID --limit 5", quote(binary), quote(knowledgeRoot))
	}
	quote := func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }
	return fmt.Sprintf("%s session query '<task-related-query>' --knowledge-root %s --project-root '<project-root>' --session-id \"$CODEX_THREAD_ID\" --limit 5", quote(binary), quote(knowledgeRoot))
}

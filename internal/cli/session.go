package cli

import (
	stdcontext "context"
	"errors"
	"os"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func (context commandContext) session(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "query", "anchor", "checkpoint", "inspect", "prepare-archive", "finalize-archive")
	if err != nil {
		return err
	}
	flags := quietFlags("session " + command)
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	projectRoot := flags.String("project-root", "", "project worktree")
	projectID := flags.String("project-id", "", "project identifier")
	expectedProject := flags.String("expected-project-id", "", "expected project identifier")
	sessionID := flags.String("session-id", envOr("CODEX_THREAD_ID", ""), "stable session identifier")
	memoryFile := flags.String("memory-file", "", "UTF-8 memory file")
	limit := flags.Int("limit", 8, "query result count")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		return err
	}
	if *sessionID == "" {
		return errors.New("--session-id is required when CODEX_THREAD_ID is unavailable")
	}
	if command == "query" && *projectRoot == "" {
		return errors.New("--project-root is required")
	}
	query := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if command == "query" && query == "" {
		return errors.New("query text is required after flags")
	}
	var memory []byte
	if command == "checkpoint" {
		if *memoryFile == "" {
			return errors.New("--memory-file is required")
		}
		memory, err = os.ReadFile(*memoryFile)
		if err != nil {
			return err
		}
	}
	response, err := withSharedKnowledge(stdcontext.Background(), knowledgeRoot, func(stdcontext.Context) (any, error) {
		if command == "query" {
			receipt, err := rag.Query(knowledgeRoot, *projectRoot, query, *sessionID, *expectedProject, *limit)
			if err != nil {
				return nil, err
			}
			return receipt, nil
		}
		if command == "prepare-archive" {
			anchor, err := rag.PrepareArchive(knowledgeRoot, *sessionID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "ARCHIVE_PREPARED", "anchor": anchor}, nil
		}
		if command == "finalize-archive" {
			anchor, err := rag.FinalizeArchive(knowledgeRoot, *sessionID)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "ARCHIVED", "anchor": anchor}, nil
		}
		resolvedProject, err := resolveProject(knowledgeRoot, *projectRoot, *projectID)
		if err != nil {
			return nil, err
		}
		switch command {
		case "anchor":
			anchor, created, err := rag.EnsureAnchor(knowledgeRoot, *sessionID, resolvedProject)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "ACTIVE", "created": created, "anchor": anchor}, nil
		case "inspect":
			state, err := rag.InspectAnchor(knowledgeRoot, *sessionID, resolvedProject)
			if err != nil {
				return nil, err
			}
			state["ok"] = true
			return state, nil
		case "checkpoint":
			anchor, err := rag.Checkpoint(knowledgeRoot, *sessionID, resolvedProject, string(memory))
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "CHECKPOINTED", "anchor": anchor}, nil
		}
		return nil, errors.New("unreachable session command")
	})
	if err != nil {
		return err
	}
	return context.write(response)
}

func resolveProject(knowledgeRoot, projectRoot, projectID string) (string, error) {
	if projectID != "" {
		return projectID, nil
	}
	if projectRoot == "" {
		return "", errors.New("--project-id or --project-root is required")
	}
	identity, err := rag.TrackProject(knowledgeRoot, projectRoot)
	if err != nil {
		return "", err
	}
	return identity.ID, nil
}

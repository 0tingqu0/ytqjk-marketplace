package cli

import (
	stdcontext "context"
	"errors"
	"flag"
	"io"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func (context commandContext) rag(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "init", "index", "index-global", "bootstrap", "query")
	if err != nil {
		return err
	}
	flags := quietFlags("rag " + command)
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	projectRoot := flags.String("project-root", "", "project worktree")
	vectorMode := flags.String("vector-mode", "auto", "off, auto, or on")
	sessionID := flags.String("session-id", envOr("CODEX_THREAD_ID", ""), "stable session identifier")
	expectedProject := flags.String("expected-project-id", "", "expected project identifier")
	limit := flags.Int("limit", 8, "result count")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		return err
	}
	if command != "index-global" && *projectRoot == "" {
		return errors.New("--project-root is required")
	}
	query := strings.TrimSpace(strings.Join(flags.Args(), " "))
	if command == "query" && query == "" {
		return errors.New("query text is required after flags")
	}
	response, err := withSharedKnowledge(stdcontext.Background(), knowledgeRoot, func(stdcontext.Context) (any, error) {
		switch command {
		case "init":
			manifest, directory, err := rag.Init(knowledgeRoot, *projectRoot)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "INITIALIZED", "project_dir": directory, "manifest": manifest}, nil
		case "index":
			result, err := rag.Build(knowledgeRoot, *projectRoot, *vectorMode)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "INDEXED", "result": result}, nil
		case "index-global":
			result, err := rag.BuildGlobal(knowledgeRoot, *vectorMode)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "INDEXED", "result": result}, nil
		case "bootstrap":
			result, err := rag.Bootstrap(knowledgeRoot, *projectRoot, *vectorMode)
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": true, "status": "SUCCEEDED", "result": result, "receipt": rag.BootstrapReceipt(result)}, nil
		case "query":
			receipt, err := rag.Query(knowledgeRoot, *projectRoot, query, *sessionID, *expectedProject, *limit)
			if err != nil {
				return nil, err
			}
			return receipt, nil
		}
		return nil, errors.New("unreachable RAG command")
	})
	if err != nil {
		return err
	}
	return context.write(response)
}

func quietFlags(name string) *flag.FlagSet {
	result := flag.NewFlagSet(name, flag.ContinueOnError)
	result.SetOutput(io.Discard)
	return result
}

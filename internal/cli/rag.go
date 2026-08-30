package cli

import (
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
	switch command {
	case "init":
		if *projectRoot == "" {
			return errors.New("--project-root is required")
		}
		manifest, directory, err := rag.Init(knowledgeRoot, *projectRoot)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "INITIALIZED", "project_dir": directory, "manifest": manifest})
	case "index":
		if *projectRoot == "" {
			return errors.New("--project-root is required")
		}
		result, err := rag.Build(knowledgeRoot, *projectRoot, *vectorMode)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "INDEXED", "result": result})
	case "index-global":
		result, err := rag.BuildGlobal(knowledgeRoot, *vectorMode)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "INDEXED", "result": result})
	case "bootstrap":
		if *projectRoot == "" {
			return errors.New("--project-root is required")
		}
		result, err := rag.Bootstrap(knowledgeRoot, *projectRoot, *vectorMode)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "SUCCEEDED", "result": result, "receipt": rag.BootstrapReceipt(result)})
	case "query":
		if *projectRoot == "" {
			return errors.New("--project-root is required")
		}
		query := strings.TrimSpace(strings.Join(flags.Args(), " "))
		if query == "" {
			return errors.New("query text is required after flags")
		}
		receipt, err := rag.Query(knowledgeRoot, *projectRoot, query, *sessionID, *expectedProject, *limit)
		if err != nil {
			return err
		}
		return context.write(receipt)
	}
	return errors.New("unreachable RAG command")
}

func quietFlags(name string) *flag.FlagSet {
	result := flag.NewFlagSet(name, flag.ContinueOnError)
	result.SetOutput(io.Discard)
	return result
}

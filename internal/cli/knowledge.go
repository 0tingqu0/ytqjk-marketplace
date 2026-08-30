package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
)

func (context commandContext) knowledge(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "create-project", "create-candidate", "edit", "delete", "state", "snapshot", "feedback", "search", "intake", "workbench")
	if err != nil {
		return err
	}
	flags := quietFlags("knowledge " + command)
	knowledgeValue := flags.String("knowledge-root", "", "knowledge store root")
	databaseValue := flags.String("database", "", "knowledge SQLite database")
	projectID := flags.String("project-id", "", "project UUID")
	documentID := flags.String("document-id", "", "document UUID")
	scope := flags.String("scope", "project", "project scope")
	alias := flags.String("alias", "", "project alias")
	title := flags.String("title", "", "candidate title")
	contentFile := flags.String("content-file", "", "UTF-8 content file")
	source := flags.String("source", "local-cli", "source reference")
	state := flags.String("state", "", "candidate lifecycle state")
	invocationID := flags.String("invocation-id", "", "feedback invocation ID")
	correct := flags.Bool("correct", false, "record a correct retrieval")
	limit := flags.Int("limit", 20, "search result count")
	assets := flags.String("assets", "", "workbench asset directory")
	port := flags.Int("port", 0, "loopback port, zero selects an available port")
	var sources stringsFlag
	flags.Var(&sources, "source-ref", "intake source (repeatable)")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	knowledgeRoot, err := platform.KnowledgeRoot(*knowledgeValue)
	if err != nil {
		return err
	}
	if *databaseValue == "" {
		*databaseValue = filepath.Join(knowledgeRoot, "service", "knowledge.sqlite3")
	}
	if command == "workbench" {
		if *projectID == "" {
			return errors.New("--project-id is required")
		}
		resolvedAssets, err := resolveAssets(*assets, "workbench")
		if err != nil {
			return err
		}
		return dashboard.RunWorkbench(*databaseValue, *projectID, resolvedAssets, *port, context.out)
	}
	service, err := knowledge.Open(*databaseValue)
	if err != nil {
		return err
	}
	defer service.Close()
	switch command {
	case "create-project":
		if strings.TrimSpace(*alias) == "" {
			return errors.New("--alias is required")
		}
		identifier, err := service.CreateProject(*scope, *alias)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "READY", "project_id": identifier})
	case "create-candidate", "intake":
		if *contentFile == "" {
			return errors.New("--content-file is required")
		}
		content, err := os.ReadFile(*contentFile)
		if err != nil {
			return err
		}
		identifier := *projectID
		candidateTitle := *title
		candidateSource := *source
		if command == "intake" {
			identifier, err = service.CreateProject("global", "global-candidates")
			if err != nil {
				return err
			}
			if candidateTitle == "" {
				candidateTitle = strings.TrimSpace(strings.Join(flags.Args(), " "))
			}
			if candidateTitle == "" {
				candidateTitle = filepath.Base(*contentFile)
			}
			if len(sources) > 0 {
				candidateSource = strings.Join(sources, ", ")
			}
		} else if err := requireNoPositionals(flags.Args()); err != nil {
			return err
		}
		if identifier == "" || candidateTitle == "" {
			return errors.New("--project-id and --title are required")
		}
		document, err := service.CreateCandidate(identifier, candidateTitle, string(content), candidateSource)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "CANDIDATE", "project_id": identifier, "document_id": document})
	case "edit":
		if *documentID == "" || *contentFile == "" {
			return errors.New("--document-id and --content-file are required")
		}
		content, err := os.ReadFile(*contentFile)
		if err != nil {
			return err
		}
		if err := service.EditCandidate(*documentID, string(content), *source); err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "UPDATED", "document_id": *documentID})
	case "delete":
		if *documentID == "" {
			return errors.New("--document-id is required")
		}
		if err := service.SoftDeleteCandidate(*documentID); err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "DELETED", "document_id": *documentID})
	case "state":
		if *documentID == "" || *state == "" {
			return errors.New("--document-id and --state are required")
		}
		normalizedState := strings.ToLower(*state)
		if err := service.AppendState(*documentID, normalizedState, nil); err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": strings.ToUpper(normalizedState), "document_id": *documentID})
	case "snapshot":
		if *projectID == "" {
			return errors.New("--project-id is required")
		}
		identifier, err := service.CreateSnapshot(*projectID)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "ACTIVE", "snapshot_id": identifier})
	case "feedback":
		if *documentID == "" || *invocationID == "" {
			return errors.New("--document-id and --invocation-id are required")
		}
		if err := service.RecordFeedback(*documentID, *invocationID, *correct); err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "RECORDED", "correct": *correct})
	case "search":
		if *projectID == "" {
			return errors.New("--project-id is required")
		}
		query := strings.TrimSpace(strings.Join(flags.Args(), " "))
		if query == "" {
			return errors.New("query text is required after flags")
		}
		results, err := service.Search(*projectID, query, *limit)
		if err != nil {
			return err
		}
		return context.write(map[string]any{"ok": true, "status": "READY", "result_count": len(results), "results": results})
	}
	return fmt.Errorf("unreachable knowledge command: %s", command)
}

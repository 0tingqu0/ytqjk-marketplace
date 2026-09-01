package cli

import (
	stdcontext "context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/dashboard"
	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
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
	fileScope, err := knowledgeFileScope(knowledgeRoot, *databaseValue)
	if err != nil {
		return err
	}
	if command == "workbench" {
		if *projectID == "" {
			return errors.New("--project-id is required")
		}
		resolvedAssets, err := resolveAssets(*assets, "workbench")
		if err != nil {
			return err
		}
		admit := func(
			requestContext stdcontext.Context,
			action func(*knowledge.Service) error,
		) error {
			_, admissionErr := withSharedScope(requestContext, fileScope, func(stdcontext.Context) (any, error) {
				service, openErr := knowledge.Open(*databaseValue)
				if openErr != nil {
					return nil, openErr
				}
				actionErr := action(service)
				return nil, errors.Join(actionErr, service.Close())
			})
			return admissionErr
		}
		return dashboard.RunWorkbench(*projectID, resolvedAssets, *port, context.out, admit)
	}
	return context.withSharedScopeOutput(stdcontext.Background(), fileScope, func(admittedContext stdcontext.Context, admittedCommand commandContext) (result error) {
		service, err := knowledge.Open(*databaseValue)
		if err != nil {
			return err
		}
		defer func() { result = errors.Join(result, service.Close()) }()
		switch command {
		case "create-project":
			if strings.TrimSpace(*alias) == "" {
				return errors.New("--alias is required")
			}
			identifier, err := service.CreateProject(*scope, *alias)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "READY", "project_id": identifier})
		case "create-candidate":
			if *contentFile == "" {
				return errors.New("--content-file is required")
			}
			content, err := os.ReadFile(*contentFile)
			if err != nil {
				return err
			}
			if err := requireNoPositionals(flags.Args()); err != nil {
				return err
			}
			if *projectID == "" || *title == "" {
				return errors.New("--project-id and --title are required")
			}
			documentID, err := service.CreateCandidate(*projectID, *title, string(content), *source)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "CANDIDATE", "project_id": *projectID, "document_id": documentID})
		case "intake":
			if *contentFile == "" {
				return errors.New("--content-file is required")
			}
			identifier, err := service.CreateProject("global", "global-candidates")
			if err != nil {
				return err
			}
			candidateTitle := *title
			if candidateTitle == "" {
				candidateTitle = strings.TrimSpace(strings.Join(flags.Args(), " "))
			}
			if candidateTitle == "" {
				candidateTitle = filepath.Base(*contentFile)
			}
			candidateSource := *source
			if len(sources) > 0 {
				candidateSource = strings.Join(sources, ", ")
			}
			documentID, extraction, job, err := runDocumentIntake(admittedContext, *databaseValue, service, identifier, *contentFile, candidateTitle, candidateSource)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{
				"ok": true, "status": "CANDIDATE", "project_id": identifier, "document_id": documentID,
				"job": job, "extraction": map[string]any{
					"engine": extraction.Engine, "format": extraction.Format, "media_kind": extraction.MediaKind,
					"ocr_state": extraction.OCRState, "chunk_count": len(extraction.Chunks),
					"warnings": extraction.Warnings, "review_reasons": extraction.ReviewReasons,
				},
			})
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
			return admittedCommand.write(map[string]any{"ok": true, "status": "UPDATED", "document_id": *documentID})
		case "delete":
			if *documentID == "" {
				return errors.New("--document-id is required")
			}
			if err := service.SoftDeleteCandidate(*documentID); err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "DELETED", "document_id": *documentID})
		case "state":
			if *documentID == "" || *state == "" {
				return errors.New("--document-id and --state are required")
			}
			normalizedState := strings.ToLower(*state)
			if err := service.AppendState(*documentID, normalizedState, nil); err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": strings.ToUpper(normalizedState), "document_id": *documentID})
		case "snapshot":
			if *projectID == "" {
				return errors.New("--project-id is required")
			}
			identifier, err := service.CreateSnapshot(*projectID)
			if err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "ACTIVE", "snapshot_id": identifier})
		case "feedback":
			if *documentID == "" || *invocationID == "" {
				return errors.New("--document-id and --invocation-id are required")
			}
			if err := service.RecordFeedback(*documentID, *invocationID, *correct); err != nil {
				return err
			}
			return admittedCommand.write(map[string]any{"ok": true, "status": "RECORDED", "correct": *correct})
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
			return admittedCommand.write(map[string]any{"ok": true, "status": "READY", "result_count": len(results), "results": results})
		}
		return fmt.Errorf("unreachable knowledge command: %s", command)
	})
}

func runDocumentIntake(ctx stdcontext.Context, databasePath string, service *knowledge.Service, projectID, sourcePath, title, source string) (string, document.Result, document.Job, error) {
	content, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", document.Result{}, document.Job{}, err
	}
	digest := sha256.Sum256(content)
	store, err := document.OpenJobStore(databasePath, 30*time.Second, 3)
	if err != nil {
		return "", document.Result{}, document.Job{}, err
	}
	defer store.Close()
	payload := map[string]any{
		"source_name": filepath.Base(sourcePath), "source_sha256": hex.EncodeToString(digest[:]),
		"project_id": projectID, "title": title, "source": source,
	}
	job, err := store.Enqueue(ctx, payload, map[string]any{"extractor": "go-native-v1", "max_bytes": document.MaxSourceBytes})
	if err != nil {
		return "", document.Result{}, document.Job{}, err
	}
	if job.State == "SUCCEEDED" {
		identifier, _ := job.Result["document_id"].(string)
		if identifier == "" {
			return "", document.Result{}, job, errors.New("completed intake job has no document id")
		}
		return identifier, document.Result{
			Engine: valueString(job.Result["engine"]), Format: valueString(job.Result["format"]),
			MediaKind: valueString(job.Result["media_kind"]), OCRState: valueString(job.Result["ocr_state"]),
		}, job, nil
	}
	if job.State == "FAILED" {
		job, err = store.Retry(ctx, job.ID)
		if err != nil {
			return "", document.Result{}, job, err
		}
	}
	running, found, err := store.ClaimID(ctx, job.ID)
	if err != nil || !found {
		if err == nil {
			err = errors.New("document intake job is already running")
		}
		return "", document.Result{}, job, err
	}
	advance := func(stage string) error {
		var advanceErr error
		running, advanceErr = store.Advance(ctx, running.ID, running.Attempt, stage, 0)
		return advanceErr
	}
	if err := advance("parse"); err != nil {
		return "", document.Result{}, running, err
	}
	extraction, err := document.ExtractBytes(filepath.Base(sourcePath), content)
	if err != nil {
		failed, _ := store.Fail(ctx, running.ID, running.Attempt, "EXTRACTION_FAILED", err)
		return "", document.Result{}, failed, err
	}
	for _, stage := range []string{"extract", "ocr", "chunk", "assess"} {
		if err := advance(stage); err != nil {
			return "", extraction, running, err
		}
	}
	identifier, err := service.CreateCandidate(projectID, title, extraction.Text, source)
	if err != nil {
		failed, _ := store.Fail(ctx, running.ID, running.Attempt, "PERSIST_FAILED", err)
		return "", extraction, failed, err
	}
	if err := advance("persist"); err != nil {
		return "", extraction, running, err
	}
	finished, err := store.Succeed(ctx, running.ID, running.Attempt, map[string]any{
		"document_id": identifier, "engine": extraction.Engine, "format": extraction.Format,
		"media_kind": extraction.MediaKind, "ocr_state": extraction.OCRState, "chunk_count": len(extraction.Chunks),
	})
	if err != nil {
		return "", extraction, running, err
	}
	return identifier, extraction, finished, nil
}

func valueString(value any) string {
	result, _ := value.(string)
	return result
}

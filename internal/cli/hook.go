package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/platform"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

func (context commandContext) hook(arguments []string) error {
	command, arguments, err := requireCommand(arguments, "session-start")
	if err != nil {
		return err
	}
	if command != "session-start" || len(arguments) != 0 {
		return errors.New("hook session-start accepts no arguments")
	}
	var event struct {
		SessionID string `json:"session_id"`
		CWD       string `json:"cwd"`
	}
	decoder := json.NewDecoder(io.LimitReader(context.in, 64*1024))
	if err := decoder.Decode(&event); err != nil {
		return context.write(map[string]any{"systemMessage": "YTQJK session anchoring failed: invalid hook input"})
	}
	event.SessionID = strings.TrimSpace(event.SessionID)
	if event.SessionID == "" || event.CWD == "" {
		return nil
	}
	if info, err := os.Stat(event.CWD); err != nil || !info.IsDir() {
		return nil
	}
	knowledgeRoot, err := platform.KnowledgeRoot("")
	if err != nil {
		return context.write(map[string]any{"systemMessage": "YTQJK session anchoring failed: knowledge root unavailable"})
	}
	identity, err := rag.TrackProject(knowledgeRoot, event.CWD)
	if err != nil {
		return context.write(map[string]any{"systemMessage": "YTQJK session anchoring failed: project tracking unavailable"})
	}
	anchor, created, err := rag.EnsureAnchor(knowledgeRoot, event.SessionID, identity.ID)
	if err != nil {
		return context.write(map[string]any{"systemMessage": "YTQJK session anchoring failed: session anchor unavailable"})
	}
	additional := "KNOWLEDGE_RECEIPT status=SESSION_ANCHORED project_id=" + identity.ID +
		" project_tracking=REGISTERED scope=session-anchor anchor_key=" + anchor.SessionKey +
		" anchor_created=" + map[bool]string{true: "true", false: "false"}[created] +
		". Before answering project questions, run `ytqjk session query --project-root <cwd> --session-id <session-id> --expected-project-id " +
		identity.ID + " <question>` and report the complete receipt."
	return context.write(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "SessionStart", "additionalContext": additional}})
}

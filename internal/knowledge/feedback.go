package knowledge

import (
	"crypto/sha1"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

const (
	globalAlias     = "global-knowledge"
	globalScope     = "global"
	globalNamespace = "afca7743-836a-4fb3-a7aa-31f010f58eb0"
)

var errLegacyFeedbackDowngrade = errors.New(
	"legacy schema blocks feedback downgrade; destructive trigger migration is not approved",
)

type feedbackDocument struct {
	ID             string
	Title          string
	Scope          string
	VersionID      int64
	Ordinal        int
	State          string
	Content        string
	OriginalSHA256 string
}

func recordFeedback(tx *sql.Tx, jobID int64, payload map[string]any, now string) error {
	documentID := payload["document_id"].(string)
	invocationID := payload["invocation_id"].(string)
	correct := payload["correct"].(bool)
	var existing int
	if err := tx.QueryRow("SELECT COUNT(*) FROM feedback_events WHERE document_id=? AND invocation_id=?", documentID, invocationID).Scan(&existing); err != nil {
		return err
	}
	if existing != 0 {
		return errors.New("invocation feedback conflicts with existing event")
	}
	source, err := currentFeedbackDocument(tx, documentID)
	if err != nil {
		return err
	}
	if source.Scope == globalScope {
		return errors.New("feedback must target a project knowledge document")
	}
	if source.State == "tombstone" {
		return errors.New("recycled knowledge cannot receive feedback")
	}
	score, err := feedbackScore(tx, source.ID, source.State)
	if err != nil {
		return err
	}
	nextScore, nextState := feedbackOutcome(score, source.State, correct)
	if err := validateFeedbackTransition(tx, source.State, nextState); err != nil {
		return err
	}
	globalID, err := linkedGlobalDocument(tx, source.ID)
	if err != nil {
		return err
	}
	if globalID == "" && correct && nextScore >= 2 {
		globalID, err = createGlobalCopy(tx, source, now)
		if err != nil {
			return err
		}
	}
	resultVersionID, err := transitionFeedback(tx, source, nextState, "feedback:"+source.ID, "feedback", now)
	if err != nil {
		return err
	}
	var globalResult any
	if globalID != "" {
		globalVersion, err := syncGlobalDocument(tx, globalID, source, nextState, now)
		if err != nil {
			return err
		}
		globalResult = globalVersion
	}
	correctValue := 0
	if correct {
		correctValue = 1
	}
	if _, err := tx.Exec(`INSERT INTO feedback_events(job_id,document_id,invocation_id,correct,score,state,
input_version_id,result_version_id,global_result_version_id,created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		jobID, source.ID, invocationID, correctValue, nextScore, nextState, source.VersionID, resultVersionID, globalResult, now); err != nil {
		return err
	}
	event := "feedback_incorrect"
	if correct {
		event = "feedback_correct"
	}
	return audit(tx, event, source.ID, now)
}

func validateFeedbackTransition(tx *sql.Tx, prior, next string) error {
	if !((prior == "approved" && next == "candidate") || (prior == "verified" && next == "approved")) {
		return nil
	}
	var definition string
	err := tx.QueryRow(
		"SELECT sql FROM sqlite_master WHERE type='trigger' AND name='versions_state_machine'",
	).Scan(&definition)
	if err != nil {
		return fmt.Errorf("inspect versions state trigger: %w", err)
	}
	normalized := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(strings.ToLower(definition))
	approved := "prior.state='approved'andnew.statein('candidate','approved','verified','tombstone')"
	verified := "prior.state='verified'andnew.statein('approved','verified','tombstone')"
	if strings.Contains(normalized, approved) && strings.Contains(normalized, verified) {
		return nil
	}
	return errLegacyFeedbackDowngrade
}

func feedbackOutcome(score int, state string, correct bool) (int, string) {
	if correct {
		next := score + 1
		if next > 3 {
			next = 3
		}
		states := map[int]string{0: "candidate", 1: "candidate", 2: "approved", 3: "verified"}
		return next, states[next]
	}
	switch state {
	case "candidate":
		return 0, "tombstone"
	case "approved":
		return 1, "candidate"
	default:
		return 2, "approved"
	}
}

func feedbackScore(tx *sql.Tx, documentID, state string) (int, error) {
	var eventScore int
	err := tx.QueryRow("SELECT score FROM feedback_events WHERE document_id=? ORDER BY id DESC LIMIT 1", documentID).Scan(&eventScore)
	if errors.Is(err, sql.ErrNoRows) {
		eventScore = 0
	} else if err != nil {
		return 0, err
	}
	stateScore := map[string]int{"candidate": 0, "approved": 2, "verified": 3}[state]
	if stateScore > eventScore {
		return stateScore, nil
	}
	return eventScore, nil
}

func currentFeedbackDocument(tx *sql.Tx, documentID string) (feedbackDocument, error) {
	var value feedbackDocument
	var deleted sql.NullString
	err := tx.QueryRow(`SELECT d.id,d.title,p.scope,v.id,v.ordinal,v.state,CAST(o.content AS TEXT),v.original_sha256,d.deleted_at
FROM documents d JOIN projects p ON p.id=d.project_id JOIN versions v ON v.document_id=d.id
JOIN originals o ON o.sha256=v.original_sha256 WHERE d.id=? ORDER BY v.ordinal DESC LIMIT 1`, documentID).
		Scan(&value.ID, &value.Title, &value.Scope, &value.VersionID, &value.Ordinal, &value.State, &value.Content, &value.OriginalSHA256, &deleted)
	if errors.Is(err, sql.ErrNoRows) || deleted.Valid {
		return feedbackDocument{}, errors.New("knowledge document is unavailable")
	}
	return value, err
}

func transitionFeedback(tx *sql.Tx, source feedbackDocument, state, locator, sourceKind, now string) (int64, error) {
	if source.State == state {
		return source.VersionID, nil
	}
	return appendVersion(tx, source.ID, state, source.Content, locator, sourceKind, now)
}

func linkedGlobalDocument(tx *sql.Tx, sourceID string) (string, error) {
	var identifier string
	err := tx.QueryRow("SELECT global_document_id FROM global_sync WHERE source_document_id=?", sourceID).Scan(&identifier)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return identifier, err
}

func createGlobalCopy(tx *sql.Tx, source feedbackDocument, now string) (string, error) {
	var projectID, scope string
	err := tx.QueryRow("SELECT id,scope FROM projects WHERE alias=?", globalAlias).Scan(&projectID, &scope)
	if errors.Is(err, sql.ErrNoRows) {
		projectID, err = uuidV5(globalNamespace, globalAlias)
		if err != nil {
			return "", err
		}
		if _, err := tx.Exec("INSERT INTO projects(id,name,scope,alias,created_at) VALUES (?,?,?,?,?)", projectID, globalAlias, globalScope, globalAlias, now); err != nil {
			return "", err
		}
		if err := audit(tx, "project_created", projectID, now); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else if scope != globalScope {
		return "", errors.New("global knowledge project identity is invalid")
	}
	globalID, err := uuidV5(projectID, source.ID)
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO documents(id,project_id,title) VALUES (?,?,?)", globalID, projectID, source.Title); err != nil {
		return "", err
	}
	if _, err := appendVersion(tx, globalID, "candidate", source.Content, "project-sync:"+source.ID, "project-sync", now); err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO global_sync(source_document_id,global_document_id,created_at) VALUES (?,?,?)", source.ID, globalID, now); err != nil {
		return "", err
	}
	if err := audit(tx, "knowledge_synced_global", source.ID, now); err != nil {
		return "", err
	}
	return globalID, nil
}

func syncGlobalDocument(tx *sql.Tx, globalID string, source feedbackDocument, state, now string) (int64, error) {
	current, err := currentFeedbackDocument(tx, globalID)
	if err != nil {
		return 0, err
	}
	if current.OriginalSHA256 != source.OriginalSHA256 {
		if _, err := appendVersion(tx, globalID, current.State, source.Content, "project-sync:"+source.ID, "project-sync", now); err != nil {
			return 0, err
		}
		current, err = currentFeedbackDocument(tx, globalID)
		if err != nil {
			return 0, err
		}
	}
	return transitionFeedback(tx, current, state, "project-sync:"+source.ID, "project-sync", now)
}

func uuidV5(namespace, name string) (string, error) {
	compact := strings.ReplaceAll(namespace, "-", "")
	if len(compact) != 32 {
		return "", errors.New("UUID namespace is invalid")
	}
	namespaceBytes, err := hex.DecodeString(compact)
	if err != nil {
		return "", err
	}
	digest := sha1.New() // UUIDv5 requires SHA-1 by specification.
	_, _ = digest.Write(namespaceBytes)
	_, _ = digest.Write([]byte(name))
	value := digest.Sum(nil)[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}

package dashboard

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/document"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestIntakeDistinguishesPostCommitDurabilityFailure(t *testing.T) {
	ordinary := errors.New("write failed")
	committed := &safeio.PostCommitError{Err: errors.New("directory sync failed")}
	if got := intakeStageFailureCode(ordinary); got != "INTAKE_STAGE_FAILED" {
		t.Fatalf("ordinary stage failure code = %q", got)
	}
	if got := intakeStageFailureCode(committed); got != "INTAKE_STAGE_DURABILITY_UNKNOWN" {
		t.Fatalf("committed stage failure code = %q", got)
	}
	if got := intakePersistenceCategory(ordinary); got != "PERSIST_FAILED" {
		t.Fatalf("ordinary persistence category = %q", got)
	}
	if got := intakePersistenceCategory(committed); got != "PERSIST_DURABILITY_UNKNOWN" {
		t.Fatalf("committed persistence category = %q", got)
	}
	if intakeFailureRetryable("PERSIST_DURABILITY_UNKNOWN") {
		t.Fatal("durability-unknown candidate write was marked retryable")
	}
}

func TestIntakeSourceCleanupRejectsEscapedUploadPath(t *testing.T) {
	root := t.TempDir()
	uploads := filepath.Join(root, "service", "intake", "uploads")
	if err := os.MkdirAll(uploads, 0o700); err != nil {
		t.Fatal(err)
	}
	protected := filepath.Join(root, "protected.txt")
	if err := os.WriteFile(protected, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{KnowledgeRoot: root}
	job := document.Job{Payload: map[string]any{
		"staging_ref": "service/intake/uploads/../../../protected.txt",
	}}
	if err := server.removeIntakeSource(job); err == nil {
		t.Fatal("escaped intake source path was accepted")
	}
	if content, err := os.ReadFile(protected); err != nil || string(content) != "keep" {
		t.Fatalf("protected file changed: %q, %v", content, err)
	}
}

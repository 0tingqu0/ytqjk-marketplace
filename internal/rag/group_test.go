package rag

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestGroupIndexIncludesOnlyApprovedAndVerifiedSources(t *testing.T) {
	root := t.TempDir()
	writeGroupFixture(t, root, "verified/runbook.md", "verified recovery instructions")
	writeGroupFixture(t, root, "personal-experience/approved/lesson.md", "approved migration lesson")
	writeGroupFixture(t, root, "personal-experience/candidates/draft.md", "candidate must stay private")
	writeGroupFixture(t, root, "error-experience/approved/secret.md", "authorization: bearer must-not-index")

	receipt, err := BuildGroup(root, "operations", nil)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "REBUILT" || receipt.Documents != 2 || receipt.Chunks != 2 {
		t.Fatalf("receipt = %#v", receipt)
	}
	status := ReadGroupStatus(root, "operations")
	if status.Status != "READY" || status.Generation != receipt.Generation {
		t.Fatalf("status = %#v", status)
	}
	var index Index
	if err := safeio.ReadJSON(filepath.Join(root, "libraries", "operations", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	for _, chunk := range index.Chunks {
		if chunk.Path == "personal-experience/candidates/draft.md" || chunk.Path == "error-experience/approved/secret.md" {
			t.Fatalf("ungoverned chunk was indexed: %#v", chunk)
		}
	}

	writeGroupFixture(t, root, "personal-experience/approved/lesson.md", "changed approved lesson")
	if stale := ReadGroupStatus(root, "operations"); stale.Status != "STALE" {
		t.Fatalf("changed source status = %#v", stale)
	}
	rebuilt, err := BuildGroup(root, "operations", nil)
	if err != nil || rebuilt.Status != "REBUILT" || rebuilt.Generation == receipt.Generation {
		t.Fatalf("rebuilt = %#v, %v", rebuilt, err)
	}
}

func TestGroupIndexSelectionUsesDocumentPathDigest(t *testing.T) {
	root := t.TempDir()
	writeGroupFixture(t, root, "verified/one.md", "one")
	writeGroupFixture(t, root, "verified/two.md", "two")
	selected := sha256Text("verified/two.md")
	receipt, err := BuildGroup(root, "selected", []string{selected})
	if err != nil || receipt.Documents != 1 || receipt.Chunks != 1 {
		t.Fatalf("selected receipt = %#v, %v", receipt, err)
	}
	_, err = BuildGroup(root, "unknown", []string{sha256Text("verified/missing.md")})
	var groupErr *GroupError
	if !errors.As(err, &groupErr) || groupErr.Code != "UNKNOWN_DOCUMENT_ID" {
		t.Fatalf("unknown selection error = %v", err)
	}
}

func TestGroupIndexDetectsArtifactTampering(t *testing.T) {
	root := t.TempDir()
	writeGroupFixture(t, root, "verified/one.md", "one")
	if _, err := BuildGroup(root, "tamper", nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "libraries", "tamper", "index.json")
	if err := os.WriteFile(path, []byte(`{"schema_version":6,"project_id":"tamper","chunks":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if status := ReadGroupStatus(root, "tamper"); status.Status != "CORRUPT" {
		t.Fatalf("tampered status = %#v", status)
	}
}

func TestGroupIndexSwitchRestoresPreviousActiveOnInvalidStage(t *testing.T) {
	root := t.TempDir()
	writeGroupFixture(t, root, "verified/one.md", "one")
	if _, err := BuildGroup(root, "operations", nil); err != nil {
		t.Fatal(err)
	}
	before := ReadGroupStatus(root, "operations")
	active, staging, err := groupLocations(root, "operations", true)
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(staging, "operations-invalid")
	if err := os.Mkdir(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	invalid, err := os.Create(filepath.Join(stage, "invalid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := invalid.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := switchGroupIndex(active, stage, staging, "operations"); groupErrorCode(err) != "ACTIVE_INDEX_READBACK_FAILED" {
		t.Fatalf("switch error = %v", err)
	}
	after := ReadGroupStatus(root, "operations")
	if after.Status != "READY" || after.Generation != before.Generation {
		t.Fatalf("restored status = %#v, before = %#v", after, before)
	}
	entries, err := os.ReadDir(staging)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging residue = %#v", entries)
	}
}

func groupErrorCode(err error) string {
	var groupErr *GroupError
	if errors.As(err, &groupErr) {
		return groupErr.Code
	}
	return ""
}

func writeGroupFixture(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

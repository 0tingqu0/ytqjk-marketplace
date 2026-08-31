package rag

import (
	"path/filepath"
	"testing"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestSearchIndexValidatesDiskArtifactsBeforeQuery(t *testing.T) {
	valid := integrityTestChunk("verified/material.md", "snapshot rollback guidance")
	tests := []struct {
		name      string
		projectID string
		chunks    []Chunk
	}{
		{name: "digest mismatch", projectID: "global", chunks: []Chunk{{
			ID: valid.ID, Path: valid.Path, Start: 0, End: valid.End,
			LineStart: 1, LineEnd: 1, Content: valid.Content + " changed", Digest: valid.Digest,
		}}},
		{name: "ungoverned global path", projectID: "global", chunks: []Chunk{
			integrityTestChunk("personal-experience/candidates/draft.md", "snapshot rollback guidance"),
		}},
		{name: "secret material", projectID: "global", chunks: []Chunk{
			integrityTestChunk("verified/secret.md", "authorization: bearer this-must-not-leak"),
		}},
		{name: "duplicate ID", projectID: "project-a", chunks: []Chunk{valid, valid}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			writeIntegrityTestArtifacts(t, directory, test.projectID, test.chunks)
			if _, _, err := SearchIndex(filepath.Join(directory, "index.json"), "snapshot", 5, "test"); err == nil {
				t.Fatal("tampered index was accepted")
			}
		})
	}

	directory := t.TempDir()
	writeIntegrityTestArtifacts(t, directory, "global", []Chunk{valid})
	results, _, err := SearchIndex(filepath.Join(directory, "index.json"), "snapshot", 5, "global-fallback")
	if err != nil || len(results) != 1 || results[0].Digest != valid.Digest {
		t.Fatalf("valid index query = %#v, %v", results, err)
	}
}

func integrityTestChunk(path, content string) Chunk {
	digest := safeio.SHA256([]byte(content))
	return Chunk{
		ID: safeio.SHA256([]byte(path + ":0:" + digest)), Path: path,
		Start: 0, End: utf8.RuneCountInString(content), LineStart: 1, LineEnd: 1,
		Content: content, Digest: digest,
	}
}

func writeIntegrityTestArtifacts(t *testing.T, directory, projectID string, chunks []Chunk) {
	t.Helper()
	if err := safeio.WriteJSON(filepath.Join(directory, "index.json"), Index{
		SchemaVersion: SchemaVersion, ProjectID: projectID, Chunks: chunks,
	}); err != nil {
		t.Fatal(err)
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "manifest.json"), Manifest{
		SchemaVersion: SchemaVersion, Identity: ProjectIdentity{ID: projectID, Name: projectID},
		Stats:      Stats{Files: len(chunks), Chunks: len(chunks)},
		VectorMode: "off", Vector: map[string]any{"enabled": false},
		SourceFingerprint: safeio.SHA256([]byte(projectID)), IndexedAt: "2026-08-31T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

package knowledge

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
)

func TestImportReceiptIntegrityAndMarkerBinding(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	candidates := []CandidateImport{
		{Title: "first", Content: "same content", SourceKind: "test", SourceRef: "first", SourceSHA: strings.Repeat("a", 64)},
		{Title: "second", Content: "same content", SourceKind: "test", SourceRef: "second", SourceSHA: strings.Repeat("b", 64)},
	}
	receipt, err := service.ImportCandidates("global", "bootstrap", "marker", candidates, false)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "IMPORTED" || receipt.CreatedDocuments != 1 || receipt.DeduplicatedDocuments != 1 || receipt.ProvenanceAdded != 2 {
		t.Fatalf("receipt = %#v", receipt)
	}
	var persisted string
	if err := service.database.QueryRow("SELECT receipt FROM import_receipts WHERE marker='marker'").Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(persisted, "receipt_sha256") || strings.Contains(persisted, "same content") {
		t.Fatalf("unsafe persisted receipt = %s", persisted)
	}
	read, found, err := service.ImportReceipt("marker")
	if err != nil || !found || read != receipt {
		t.Fatalf("read receipt = %#v, %v, %v", read, found, err)
	}
	skipped, err := service.ImportCandidates("global", "bootstrap", "marker", candidates, false)
	if err != nil || skipped.Status != "SKIPPED" {
		t.Fatalf("skipped receipt = %#v, %v", skipped, err)
	}
	if _, err := service.ImportCandidates("global", "other-bootstrap", "marker", candidates, true); err == nil || !strings.Contains(err.Error(), "another project") {
		t.Fatalf("marker rebind error = %v", err)
	}
}

func TestImportReceiptRejectsRehashedInvalidPayload(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	receipt, err := service.ImportCandidates("global", "bootstrap", "marker", []CandidateImport{{
		Title: "first", Content: "content", SourceKind: "test", SourceRef: "first", SourceSHA: strings.Repeat("c", 64),
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	receipt.CreatedDocuments = 2
	encoded, err := importReceiptJSON(receipt)
	if err != nil {
		t.Fatal(err)
	}
	checksum := hexDigest(encoded)
	if _, err := service.database.Exec("UPDATE import_receipts SET receipt=?,receipt_sha256=? WHERE marker='marker'", string(encoded), checksum); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.ImportReceipt("marker"); err == nil {
		t.Fatal("rehashed invalid receipt was accepted")
	}
}

func TestConcurrentImportMarkerIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "knowledge.sqlite3")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	candidate := []CandidateImport{{Title: "candidate", Content: "content", SourceKind: "test", SourceRef: "source", SourceSHA: strings.Repeat("d", 64)}}
	services := []*Service{first, second}
	statuses := make([]string, len(services))
	errorsByIndex := make([]error, len(services))
	var wait sync.WaitGroup
	for index, service := range services {
		wait.Add(1)
		go func(index int, service *Service) {
			defer wait.Done()
			receipt, importErr := service.ImportCandidates("global", "bootstrap", "marker", candidate, false)
			statuses[index] = receipt.Status
			errorsByIndex[index] = importErr
		}(index, service)
	}
	wait.Wait()
	for _, err := range errorsByIndex {
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(statuses)
	if encoded, _ := json.Marshal(statuses); string(encoded) != `["IMPORTED","SKIPPED"]` {
		t.Fatalf("concurrent statuses = %v", statuses)
	}
	if count, err := first.Count("documents"); err != nil || count != 1 {
		t.Fatalf("document count = %d, %v", count, err)
	}
	if count, err := first.Count("import_receipts"); err != nil || count != 1 {
		t.Fatalf("receipt count = %d, %v", count, err)
	}
}

func TestImportDedupeUsesCurrentVisibleContent(t *testing.T) {
	service, err := Open(filepath.Join(t.TempDir(), "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	candidate := CandidateImport{Title: "candidate", Content: "original content", SourceKind: "test", SourceRef: "source", SourceSHA: strings.Repeat("e", 64)}
	first, err := service.ImportCandidates("global", "bootstrap", "first", []CandidateImport{candidate}, false)
	if err != nil {
		t.Fatal(err)
	}
	var originalID string
	if err := service.database.QueryRow("SELECT document_id FROM import_documents WHERE project_id=?", first.ProjectID).Scan(&originalID); err != nil {
		t.Fatal(err)
	}
	if err := service.EditCandidate(originalID, "edited away", "test-edit"); err != nil {
		t.Fatal(err)
	}
	second, err := service.ImportCandidates("global", "bootstrap", "second", []CandidateImport{candidate}, false)
	if err != nil {
		t.Fatal(err)
	}
	if second.CreatedDocuments != 1 || second.DeduplicatedDocuments != 0 {
		t.Fatalf("reimport receipt = %#v", second)
	}
	if count, err := service.Count("documents"); err != nil || count != 2 {
		t.Fatalf("document count = %d, %v", count, err)
	}
}

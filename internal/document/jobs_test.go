package document

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestPersistentJobLifecycleAndIdempotency(t *testing.T) {
	clock := time.Unix(100, 0)
	store, err := openJobStore(filepath.Join(t.TempDir(), "jobs.sqlite3"), 10*time.Second, 3, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	payload := map[string]any{"name": "report.pdf", "staging_ref": "staging/report.pdf", "source_sha256": "abc"}
	first, err := store.Enqueue(context.Background(), payload, map[string]any{"ocr": "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$`).MatchString(first.ID) {
		t.Fatalf("job id is not UUID-shaped: %q", first.ID)
	}
	same, err := store.Enqueue(context.Background(), payload, map[string]any{"ocr": "auto"})
	if err != nil || same.ID != first.ID {
		t.Fatalf("idempotent enqueue = %#v, %v", same, err)
	}
	running, found, err := store.Claim(context.Background())
	if err != nil || !found || running.Attempt != 1 || running.State != "RUNNING" {
		t.Fatalf("claim = %#v, %v, %v", running, found, err)
	}
	for _, stage := range IntakeStages[1:] {
		running, err = store.Advance(context.Background(), running.ID, running.Attempt, stage, 4)
		if err != nil {
			t.Fatal(err)
		}
	}
	finished, err := store.Succeed(context.Background(), running.ID, running.Attempt, map[string]any{"document_id": "doc"})
	if err != nil || finished.State != "SUCCEEDED" || finished.Progress != 100 || finished.PageCount == nil || *finished.PageCount != 4 || finished.Result["document_id"] != "doc" {
		t.Fatalf("finished = %#v, %v", finished, err)
	}
}

func TestConcurrentClaimHasOneWinner(t *testing.T) {
	database := filepath.Join(t.TempDir(), "jobs.sqlite3")
	first, err := OpenJobStore(database, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenJobStore(database, time.Minute, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if _, err := first.Enqueue(context.Background(), map[string]any{"name": "one.md"}, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	stores := []*JobStore{first, second}
	var wait sync.WaitGroup
	results := make(chan bool, 2)
	for _, store := range stores {
		wait.Add(1)
		go func(store *JobStore) {
			defer wait.Done()
			_, found, claimErr := store.Claim(context.Background())
			results <- claimErr == nil && found
		}(store)
	}
	wait.Wait()
	close(results)
	winners := 0
	for winner := range results {
		if winner {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("claim winners = %d", winners)
	}
}

func TestExpiredLeaseRecoveryFencesOldOwner(t *testing.T) {
	clock := time.Unix(100, 0)
	database := filepath.Join(t.TempDir(), "jobs.sqlite3")
	first, err := openJobStore(database, 10*time.Second, 3, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	job, _ := first.Enqueue(context.Background(), map[string]any{"name": "one.md"}, map[string]any{})
	running, _, _ := first.Claim(context.Background())
	clock = time.Unix(120, 0)
	second, err := openJobStore(database, 10*time.Second, 3, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	claimed, found, err := second.Claim(context.Background())
	if err != nil || !found || claimed.ID != job.ID || claimed.Attempt != 2 {
		t.Fatalf("recovered claim = %#v, %v, %v", claimed, found, err)
	}
	if _, err := first.Advance(context.Background(), running.ID, running.Attempt, "parse", 1); !errors.Is(err, ErrInvalidJobState) && !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("old owner was not fenced: %v", err)
	}
	first.Close()
}

func TestFailureStoresOnlyDigestAndRetryPolicy(t *testing.T) {
	store, err := OpenJobStore(filepath.Join(t.TempDir(), "jobs.sqlite3"), time.Minute, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	job, _ := store.Enqueue(context.Background(), map[string]any{"name": "one.md"}, map[string]any{})
	running, _, _ := store.Claim(context.Background())
	failed, err := store.Fail(context.Background(), job.ID, running.Attempt, "OCR_FAILED", errors.New(`secret at C:\Users\name\file`))
	if err != nil || len(failed.ErrorRef) != 64 || failed.ErrorCategory != "OCR_FAILED" {
		t.Fatalf("failed = %#v, %v", failed, err)
	}
	if _, err := store.Retry(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
}

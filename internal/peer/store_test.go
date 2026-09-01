package peer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreLifecycleAndSecretRedaction(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "service", "knowledge.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Load(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("unconfigured load = %v", err)
	}
	settings, err := store.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := NewSecret()
	settings, err = store.Upsert(ctx, settings.Revision, Record{
		PeerID: "peer-remote", Title: "Remote", ProjectID: "project-a",
		Endpoint: "http://127.0.0.1:8766", Secret: secret,
		RemoteNodeID: "remote-root", ExportNodeIDs: []string{"project-a"}, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Revision != 1 {
		t.Fatalf("revision = %d", settings.Revision)
	}
	public, _ := json.Marshal(settings.Public())
	if strings.Contains(string(public), secret) || !strings.Contains(string(public), "key_fingerprint") {
		t.Fatalf("unsafe public settings: %s", public)
	}
	if _, err := store.Remove(ctx, settings.Revision, "peer-remote"); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsConcurrentStaleRevision(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "peers.sqlite3")
	first, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	settings, err := first.Bootstrap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	results := make(chan error, 2)
	for _, store := range []*Store{first, second} {
		wait.Add(1)
		go func(store *Store) {
			defer wait.Done()
			_, configureErr := store.Configure(ctx, settings.Revision, false, "127.0.0.1", 8767, false)
			results <- configureErr
		}(store)
	}
	wait.Wait()
	close(results)
	succeeded, conflicted := 0, 0
	for result := range results {
		if result == nil {
			succeeded++
		} else if errors.Is(result, ErrRevisionConflict) {
			conflicted++
		} else {
			t.Fatal(result)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
}

func TestReplayGuardPersistsAcrossStores(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	path := filepath.Join(t.TempDir(), "peers.sqlite3")
	first, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := openStore(path, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	nonce := strings.Repeat("R", 22)
	accepted, err := first.AcceptReplay(ctx, "peer-client", nonce, now.Unix())
	if err != nil || !accepted {
		t.Fatalf("first accept = %v, %v", accepted, err)
	}
	accepted, err = second.AcceptReplay(ctx, "peer-client", nonce, now.Unix())
	if err != nil || accepted {
		t.Fatalf("replay accept = %v, %v", accepted, err)
	}
}

func TestStoreDetectsDocumentCorruption(t *testing.T) {
	ctx := context.Background()
	store, err := OpenStore(filepath.Join(t.TempDir(), "peers.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.Bootstrap(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.database.Exec(`UPDATE peer_config_state SET document=document || ' '`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(ctx); err == nil || !strings.Contains(err.Error(), "DIGEST") {
		t.Fatalf("corruption error = %v", err)
	}
}

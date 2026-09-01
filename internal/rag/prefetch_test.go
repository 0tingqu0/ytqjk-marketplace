package rag

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestGoPrefetchCacheRoundTripAndGenerationInvalidation(t *testing.T) {
	root, projectDirectory, chunk := writePrefetchFixture(t, "generation-a")
	result := QueryResult{
		ID: chunk.ID, Path: chunk.Path, Start: chunk.Start, End: chunk.End,
		LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, Content: chunk.Content,
		Score: 1, Scope: "global-fallback", Digest: chunk.Digest,
	}
	stats, err := UpdatePrefetch(projectDirectory, root, "snapshot rollback", "generation-a", []QueryResult{result})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Entries != 1 || stats.UsedBytes <= 0 || stats.Policy != "LFU_LRU" {
		t.Fatalf("prefetch stats = %#v", stats)
	}
	found, queryStats, err := QueryPrefetch(projectDirectory, root, "snapshot", "generation-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].Scope != "project-prefetch-cache" || found[0].Digest != chunk.Digest || queryStats.Entries != 1 {
		t.Fatalf("prefetch query = %#v, stats=%#v", found, queryStats)
	}
	listed, _, err := ListPrefetch(projectDirectory, root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].HitCount != 2 || listed[0].Scope != "project-prefetch-cache" {
		t.Fatalf("prefetch list = %#v", listed)
	}

	writePrefetchManifest(t, root, "generation-b", 1)
	invalidated, invalidatedStats, err := QueryPrefetch(projectDirectory, root, "snapshot", "generation-b", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(invalidated) != 0 || invalidatedStats.Entries != 0 {
		t.Fatalf("stale generation survived: %#v, stats=%#v", invalidated, invalidatedStats)
	}
}

func TestLegacyJSONPrefetchMigratesToGoSQLite(t *testing.T) {
	root, projectDirectory, chunk := writePrefetchFixture(t, "generation-a")
	cacheDirectory := filepath.Join(projectDirectory, "cache")
	if err := os.MkdirAll(cacheDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := struct {
		Entries []PrefetchEntry `json:"entries"`
	}{Entries: []PrefetchEntry{
		{
			Path: chunk.Path, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd,
			Content: chunk.Content, SourceSHA256: chunk.Digest, Query: "snapshot",
			CachedAt: "2026-01-01T00:00:00Z", LastAccessed: "2026-01-01T00:00:00Z", HitCount: 3,
		},
		{
			Path: "personal-experience/candidates/unapproved.md", LineStart: 1, LineEnd: 1,
			Content: chunk.Content, SourceSHA256: chunk.Digest, Query: "snapshot", HitCount: 99,
		},
	}}
	if err := safeio.WriteJSON(filepath.Join(cacheDirectory, prefetchLegacyName), legacy); err != nil {
		t.Fatal(err)
	}
	rows, stats, err := ListPrefetch(projectDirectory, root, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].HitCount != 3 || stats.Entries != 1 {
		t.Fatalf("migrated rows = %#v, stats=%#v", rows, stats)
	}
	if info, err := os.Lstat(filepath.Join(cacheDirectory, prefetchDatabaseName)); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("Go SQLite cache missing: %v, %#v", err, info)
	}
}

func TestQueryCachesApprovedGlobalFallback(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repository := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "init")
	runTestGit(t, repository, "config", "user.name", "YTQJK Test")
	runTestGit(t, repository, "config", "user.email", "ytqjk@example.invalid")
	if err := os.WriteFile(filepath.Join(repository, "local.md"), []byte("# Local\n\nproject-only material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repository, "add", "local.md")
	runTestGit(t, repository, "commit", "-m", "initial")
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	approvedPath := filepath.Join(knowledgeRoot, "verified", "upgrade.md")
	if err := os.MkdirAll(filepath.Dir(approvedPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(approvedPath, []byte("# Upgrade\n\ntransactional snapshot rollback procedure\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Bootstrap(knowledgeRoot, repository, "off"); err != nil {
		t.Fatal(err)
	}
	identity, err := IdentifyProject(repository)
	if err != nil {
		t.Fatal(err)
	}
	first, err := Query(knowledgeRoot, repository, "snapshot rollback", "prefetch-session-1", identity.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "GLOBAL_FALLBACK_HIT" || first.Scope != "global-fallback" || first.ResultCount == 0 {
		t.Fatalf("first query = %#v", first)
	}
	second, err := Query(knowledgeRoot, repository, "snapshot rollback", "prefetch-session-2", identity.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if second.Status != "PROJECT_CACHE_HIT" || second.Scope != "project-prefetch-cache" || second.ResultCount == 0 {
		t.Fatalf("second query = %#v", second)
	}
	if entries, ok := second.Cache["entries"].(int); !ok || entries < 1 {
		t.Fatalf("cache receipt = %#v", second.Cache)
	}

	if err := os.WriteFile(approvedPath, []byte("# Upgrade\n\nnew unrelated approved material\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildGlobal(knowledgeRoot, "off"); err != nil {
		t.Fatal(err)
	}
	third, err := Query(knowledgeRoot, repository, "snapshot rollback", "prefetch-session-3", identity.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if third.Status != "KNOWLEDGE_MISS" || third.ResultCount != 0 {
		t.Fatalf("stale prefetched result survived global rebuild: %#v", third)
	}
}

func writePrefetchFixture(t *testing.T, generation string) (string, string, Chunk) {
	t.Helper()
	root := t.TempDir()
	globalDirectory := filepath.Join(root, "global-cache")
	projectDirectory := filepath.Join(root, "projects", "project-a")
	if err := os.MkdirAll(globalDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(projectDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "# Snapshot\ntransactional snapshot rollback procedure"
	digest := safeio.SHA256([]byte(content))
	chunk := Chunk{
		ID:   safeio.SHA256([]byte("verified/upgrade.md\x00" + digest)),
		Path: "verified/upgrade.md", Start: 0, End: utf8.RuneCountInString(content),
		LineStart: 1, LineEnd: strings.Count(content, "\n") + 1,
		Content: content, Digest: digest,
	}
	if err := safeio.WriteJSON(filepath.Join(globalDirectory, "index.json"), Index{
		SchemaVersion: SchemaVersion, ProjectID: "global", Chunks: []Chunk{chunk},
	}); err != nil {
		t.Fatal(err)
	}
	writePrefetchManifest(t, root, generation, 1)
	return root, projectDirectory, chunk
}

func writePrefetchManifest(t *testing.T, root, generation string, chunks int) {
	t.Helper()
	if err := safeio.WriteJSON(filepath.Join(root, "global-cache", "manifest.json"), Manifest{
		SchemaVersion: SchemaVersion, Identity: ProjectIdentity{ID: "global", Name: "global"},
		Stats: Stats{Files: 1, Chunks: chunks}, VectorMode: "off",
		Vector:            map[string]any{"enabled": false, "status": "DISABLED"},
		SourceFingerprint: generation, IndexedAt: "2026-08-31T00:00:00Z", UpdatedAt: "2026-08-31T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
}

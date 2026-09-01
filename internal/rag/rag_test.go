package rag

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestBootstrapAndSessionQuery(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "config", "user.name", "YTQJK Test")
	runTestGit(t, repo, "config", "user.email", "ytqjk@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "notes.md"), []byte("# Architecture\n\nThe runtime uses the Go programming language.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "add", "notes.md")
	runTestGit(t, repo, "commit", "-m", "initial")
	knowledgeRoot := filepath.Join(t.TempDir(), "knowledge")
	bootstrap, err := Bootstrap(knowledgeRoot, repo, "off")
	if err != nil || bootstrap.Project.Stats.Files != 1 {
		t.Fatalf("bootstrap = %#v, %v", bootstrap, err)
	}
	identity, err := IdentifyProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := Query(knowledgeRoot, repo, "Go language", "session-1", identity.ID, 5)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "PROJECT_CACHE_HIT" || receipt.ResultCount != 1 || !receipt.AnchorCreated {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestGoVectorIndexSupportsMultilingualHybridQuery(t *testing.T) {
	directory := t.TempDir()
	chunks := []Chunk{
		{ID: "orchestration", Path: "docs/guide.md", Content: "总控负责拆分并行任务并监督复审。"},
		{ID: "camera", Path: "docs/camera.md", Content: "camera exposure troubleshooting"},
	}
	fingerprint := chunksFingerprint(chunks)
	status, err := writeVectors(directory, chunks, fingerprint, "on")
	if err != nil {
		t.Fatal(err)
	}
	if status["status"] != "READY" || status["backend"] != vectorBackend {
		t.Fatalf("vector status = %#v", status)
	}
	index, ready := readVectors(directory, fingerprint)
	if !ready {
		t.Fatal("vector index is not ready")
	}
	query := vectorize("如何拆分任务")
	if got, other := cosine(query, index["orchestration"]), cosine(query, index["camera"]); got <= other || got <= 0 {
		t.Fatalf("multilingual vector scores = orchestration:%f camera:%f", got, other)
	}
}

func TestChunkTextRecordsRealLineNumbers(t *testing.T) {
	chunks := chunkText("docs/lines.md", "\n\nfirst line\nsecond line\n\n")
	if len(chunks) != 1 {
		t.Fatalf("chunks = %#v", chunks)
	}
	if chunks[0].LineStart != 3 || chunks[0].LineEnd != 4 {
		t.Fatalf("line range = %d..%d, want 3..4", chunks[0].LineStart, chunks[0].LineEnd)
	}
	if chunks[0].Content != "first line\nsecond line" || safeio.SHA256([]byte(chunks[0].Content)) != chunks[0].Digest {
		t.Fatalf("chunk content = %#v", chunks[0])
	}
}

func TestGlobalIndexIncludesOnlyGovernedKnowledge(t *testing.T) {
	root := t.TempDir()
	fixtures := map[string]string{
		"global/base.md":                             "governed global base",
		"verified/fact.md":                           "verified fact",
		"personal-experience/approved/lesson.md":     "approved personal lesson",
		"error-experience/approved/recovery.md":      "approved recovery lesson",
		"personal-experience/candidates/draft.md":    "candidate must not be indexed",
		"error-experience/candidates/draft-error.md": "error candidate must not be indexed",
	}
	for relative, content := range fixtures {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	result, err := BuildGlobal(root, "off")
	if err != nil || result.Stats.Files != 4 {
		t.Fatalf("global build = %#v, %v", result, err)
	}
	var index Index
	if err := safeio.ReadJSON(filepath.Join(root, "global-cache", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, chunk := range index.Chunks {
		paths[chunk.Path] = true
		if strings.Contains(chunk.Path, "/candidates/") {
			t.Fatalf("candidate leaked into global index: %s", chunk.Path)
		}
	}
	for _, expected := range []string{"global/base.md", "verified/fact.md", "personal-experience/approved/lesson.md", "error-experience/approved/recovery.md"} {
		if !paths[expected] {
			t.Fatalf("governed path missing: %s (%#v)", expected, paths)
		}
	}
}

func TestGlobalIndexScanStaysBoundedInsideGitRepository(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	root := t.TempDir()
	runTestGit(t, root, "init")
	for relative, content := range map[string]string{
		"personal-experience/approved/inside.md": "approved inside",
		"outside.md":                             "tracked but outside governed roots",
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	runTestGit(t, root, "add", ".")
	result, err := BuildGlobal(root, "off")
	if err != nil || result.Stats.Files != 1 {
		t.Fatalf("bounded global build = %#v, %v", result, err)
	}
	var index Index
	if err := safeio.ReadJSON(filepath.Join(root, "global-cache", "index.json"), &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Chunks) != 1 || index.Chunks[0].Path != "personal-experience/approved/inside.md" {
		t.Fatalf("bounded index = %#v", index.Chunks)
	}
}

func TestGlobalAndProjectRAGExcludeCandidates(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	knowledgeRoot := t.TempDir()
	globalAllowed := []string{
		"personal-approved-global-token",
		"error-approved-global-token",
		"verified-global-token",
	}
	globalForbidden := []string{
		"personal-candidate-global-token",
		"error-candidate-global-token",
	}
	globalFiles := map[string]string{
		"personal-experience/approved/personal.md":   globalAllowed[0],
		"error-experience/approved/error.md":         globalAllowed[1],
		"verified/verified.md":                       globalAllowed[2],
		"personal-experience/candidates/personal.md": globalForbidden[0],
		"error-experience/candidates/error.md":       globalForbidden[1],
	}
	for path, token := range globalFiles {
		writeRAGTestFile(t, knowledgeRoot, path, token)
	}
	global, err := BuildGlobal(knowledgeRoot, "off")
	if err != nil {
		t.Fatal(err)
	}
	if global.Stats.Files != len(globalAllowed) {
		t.Fatalf("global files = %d, want %d", global.Stats.Files, len(globalAllowed))
	}
	assertRAGIndexGovernance(
		t,
		filepath.Join(global.ProjectDir, "index.json"),
		filepath.Join(global.ProjectDir, "manifest.json"),
		globalAllowed,
		globalForbidden,
	)

	repo := filepath.Join(t.TempDir(), "project")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	runTestGit(t, repo, "init")
	runTestGit(t, repo, "config", "user.name", "YTQJK Test")
	runTestGit(t, repo, "config", "user.email", "ytqjk@example.invalid")
	projectAllowed := []string{
		"personal-approved-project-token",
		"error-approved-project-token",
		"verified-project-token",
	}
	projectForbidden := []string{
		"personal-candidate-project-token",
		"error-candidate-project-token",
	}
	projectFiles := map[string]string{
		"personal-experience/approved/personal.md":   projectAllowed[0],
		"error-experience/approved/error.md":         projectAllowed[1],
		"verified/verified.md":                       projectAllowed[2],
		"personal-experience/candidates/personal.md": projectForbidden[0],
		"error-experience/candidates/error.md":       projectForbidden[1],
	}
	for path, token := range projectFiles {
		writeRAGTestFile(t, repo, path, token)
	}
	runTestGit(t, repo, "add", ".")
	runTestGit(t, repo, "commit", "-m", "initial")
	project, err := Build(knowledgeRoot, repo, "off")
	if err != nil {
		t.Fatal(err)
	}
	if project.Stats.Files != len(projectAllowed) {
		t.Fatalf("project files = %d, want %d", project.Stats.Files, len(projectAllowed))
	}
	assertRAGIndexGovernance(
		t,
		filepath.Join(project.ProjectDir, "index.json"),
		filepath.Join(project.ProjectDir, "manifest.json"),
		projectAllowed,
		projectForbidden,
	)
}

func TestQueryIndexRejectsCandidateChunks(t *testing.T) {
	directory := t.TempDir()
	indexPath := filepath.Join(directory, "index.json")
	if err := safeio.WriteJSON(indexPath, Index{SchemaVersion: SchemaVersion, ProjectID: "legacy", Chunks: []Chunk{
		integrityTestChunk("personal-experience/candidates/leak.md", "personal-legacy-candidate-token"),
		integrityTestChunk("error-experience/candidates/leak.md", "error-legacy-candidate-token"),
		integrityTestChunk("verified/approved.md", "legacy-approved-token"),
	}}); err != nil {
		t.Fatal(err)
	}
	assertRAGQueries(
		t,
		indexPath,
		filepath.Join(directory, "manifest.json"),
		[]string{"legacy-approved-token"},
		[]string{"personal-legacy-candidate-token", "error-legacy-candidate-token"},
	)
}

func assertRAGIndexGovernance(t *testing.T, indexPath, manifestPath string, allowed, forbidden []string) {
	t.Helper()
	data, err := os.ReadFile(indexPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range forbidden {
		if strings.Contains(string(data), token) {
			t.Errorf("candidate token %q entered index", token)
		}
	}
	assertRAGQueries(t, indexPath, manifestPath, allowed, forbidden)
}

func assertRAGQueries(t *testing.T, indexPath, manifestPath string, allowed, forbidden []string) {
	t.Helper()
	for _, token := range forbidden {
		results, _, queryErr := queryIndex(indexPath, manifestPath, token, 5, "test")
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if len(results) != 0 {
			t.Errorf("candidate token %q returned from query: %#v", token, results)
		}
	}
	for _, token := range allowed {
		results, _, queryErr := queryIndex(indexPath, manifestPath, token, 5, "test")
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		if len(results) == 0 {
			t.Errorf("approved token %q was not queryable", token)
		}
	}
}

func writeRAGTestFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func runTestGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

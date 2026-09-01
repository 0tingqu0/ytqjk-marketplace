package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/handoff"
	"github.com/0tingqu0/ytqjk-marketplace/internal/maintenance"
	"github.com/0tingqu0/ytqjk-marketplace/internal/orchestration"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

type handoffCLIFixture struct {
	repo, bundle, knowledgeRoot string
	database, key, tokenFile    string
	runID, session              string
}

func TestHandoffApplyScopeBindsExactMutationResources(t *testing.T) {
	root := t.TempDir()
	repositoryRoot := filepath.Join(root, "repo")
	bundleParent := filepath.Join(root, "handoffs")
	if err := os.MkdirAll(repositoryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(bundleParent, 0o755); err != nil {
		t.Fatal(err)
	}
	knowledgeRoot := filepath.Join(root, "not-created", "knowledge")
	database := filepath.Join(knowledgeRoot, "service", "orchestration.sqlite3")
	key := filepath.Join(knowledgeRoot, "service", "orchestration.key")
	scope, err := handoffApplyScope(
		knowledgeRoot, repositoryRoot, filepath.Join(bundleParent, "bundle"), database, key,
	)
	if err != nil {
		t.Fatal(err)
	}
	controlRoot := t.TempDir()
	if err := maintenance.BootstrapControlRoot(context.Background(), controlRoot); err != nil {
		t.Fatal(err)
	}
	scope.ControlRoot = controlRoot
	permit, err := maintenance.AcquireShared(context.Background(), scope)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = permit.Release() })
	want := []string{
		testCanonicalResource(t, bundleParent),
		testCanonicalResource(t, database),
		testCanonicalResource(t, key),
		testCanonicalResource(t, knowledgeRoot),
		testCanonicalResource(t, repositoryRoot),
	}
	sort.Strings(want)
	if got := permit.Fence().Resources; !slices.Equal(got, want) {
		t.Fatalf("handoff resources=%v, want %v", got, want)
	}
}

func testCanonicalResource(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	value := filepath.ToSlash(filepath.Clean(absolute))
	if runtime.GOOS == "windows" {
		value = strings.ToLower(value)
	}
	return value
}

func TestHandoffApplyConsumesMutationAttestation(t *testing.T) {
	fixture := newHandoffCLIFixture(t)
	original := applyHandoff
	calls := 0
	applyHandoff = func(repo, bundle string) (handoff.ApplyResult, error) {
		calls++
		return original(repo, bundle)
	}
	t.Cleanup(func() { applyHandoff = original })

	if code, output := runHandoffApply(fixture); code != 0 {
		t.Fatalf("first handoff apply exit = %d, output = %s", code, output)
	}
	if calls != 1 {
		t.Fatalf("handoff apply calls = %d, want 1", calls)
	}
	if code, output := runHandoffApply(fixture); code != 1 || !strings.Contains(output, "attestation lease is not active") {
		t.Fatalf("replayed handoff apply exit = %d, output = %s", code, output)
	}
	if calls != 1 {
		t.Fatalf("replay invoked concrete handler; calls = %d", calls)
	}
}

func TestHandoffApplySerializesLifecycleTransition(t *testing.T) {
	fixture := newHandoffCLIFixture(t)
	original := applyHandoff
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	applyHandoff = func(repo, bundle string) (handoff.ApplyResult, error) {
		close(started)
		<-release
		return original(repo, bundle)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		applyHandoff = original
	})

	type commandResult struct {
		code   int
		output string
	}
	applyDone := make(chan commandResult, 1)
	go func() {
		code, output := runHandoffApply(fixture)
		applyDone <- commandResult{code: code, output: output}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handoff apply did not reach the concrete handler")
	}

	code, output := runTransition(fixture)
	if code != 1 || !strings.Contains(output, "run has mutation in flight") {
		t.Fatalf("transition during handoff exit = %d, output = %s", code, output)
	}
	releaseOnce.Do(func() { close(release) })
	select {
	case result := <-applyDone:
		if result.code != 0 {
			t.Fatalf("handoff apply exit = %d, output = %s", result.code, result.output)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("handoff apply did not complete")
	}
	if code, output := runTransition(fixture); code != 0 {
		t.Fatalf("transition after handoff exit = %d, output = %s", code, output)
	}
}

func newHandoffCLIFixture(t *testing.T) handoffCLIFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	repo := filepath.Join(root, "integration")
	if err := os.MkdirAll(source, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, source, "init")
	runGit(t, source, "config", "user.name", "YTQJK Test")
	runGit(t, source, "config", "user.email", "ytqjk@example.invalid")
	runGit(t, source, "config", "core.autocrlf", "false")
	writeTestFile(t, filepath.Join(source, "change.txt"), "before\n")
	runGit(t, source, "add", "--", "change.txt")
	runGit(t, source, "commit", "-m", "baseline")
	runGitAt(t, root, "clone", "--no-hardlinks", source, repo)
	writeTestFile(t, filepath.Join(source, "change.txt"), "after\n")

	bundle := filepath.Join(root, "handoff-bundle")
	exported, err := handoff.Export(source, bundle, []string{"change.txt"})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := rag.IdentifyProject(repo)
	if err != nil {
		t.Fatal(err)
	}
	knowledgeRoot := filepath.Join(root, "knowledge")
	database := filepath.Join(knowledgeRoot, "service", "orchestration.sqlite3")
	key := filepath.Join(knowledgeRoot, "service", "orchestration.key")
	ledger, _, err := orchestration.Open(database, key)
	if err != nil {
		t.Fatal(err)
	}
	session := strings.Repeat("9", 64)
	run, err := ledger.StartRun(identity.ID, strings.Repeat("8", 64), session, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Grant(orchestration.Grant{
		RunID: run.RunID, SessionKey: session, Role: "director",
		Capabilities: []string{"run:lifecycle"},
	}, session); err != nil {
		t.Fatal(err)
	}
	grant := orchestration.Grant{
		RunID: run.RunID, SessionKey: session, Role: "git", Mutation: true,
		ReadScope: exported.Paths, WriteScope: exported.Paths,
	}
	if err := ledger.Grant(grant, session); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	var attestationOutput bytes.Buffer
	attestArguments := []string{
		"orchestration", "attest", "--knowledge-root", knowledgeRoot,
		"--database", database, "--key-file", key, "--run-id", run.RunID,
		"--session-key", session, "--role", "git", "--mutation",
		"--operation", orchestration.HandoffApplyOperation,
		"--staged-hash", exported.BundleSHA256,
	}
	for _, path := range exported.Paths {
		attestArguments = append(attestArguments, "--read", path, "--write", path)
	}
	if code := Main(attestArguments, strings.NewReader(""), &attestationOutput, &attestationOutput); code != 0 {
		t.Fatalf("orchestration attest exit = %d, output = %s", code, attestationOutput.String())
	}
	var attestationResponse struct {
		Attestation orchestration.Attestation `json:"attestation"`
	}
	if err := json.Unmarshal(attestationOutput.Bytes(), &attestationResponse); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(root, "attestation.json")
	if err := safeio.WriteJSON(tokenFile, attestationResponse.Attestation); err != nil {
		t.Fatal(err)
	}
	return handoffCLIFixture{
		repo: repo, bundle: bundle, knowledgeRoot: knowledgeRoot,
		database: database, key: key, tokenFile: tokenFile,
		runID: run.RunID, session: session,
	}
}

func runHandoffApply(fixture handoffCLIFixture) (int, string) {
	var output bytes.Buffer
	code := Main([]string{
		"handoff", "apply", "--repo", fixture.repo, "--bundle", fixture.bundle,
		"--knowledge-root", fixture.knowledgeRoot,
		"--orchestration-database", fixture.database,
		"--orchestration-key-file", fixture.key,
		"--session-key", fixture.session, "--token-file", fixture.tokenFile,
	}, strings.NewReader(""), &output, &output)
	return code, output.String()
}

func runTransition(fixture handoffCLIFixture) (int, string) {
	var output bytes.Buffer
	code := Main([]string{
		"orchestration", "transition", "--knowledge-root", fixture.knowledgeRoot,
		"--database", fixture.database, "--key-file", fixture.key,
		"--run-id", fixture.runID, "--session-key", fixture.session,
		"--state", "STOPPED", "--expected-version", "0",
	}, strings.NewReader(""), &output, &output)
	return code, output.String()
}

func runGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func runGitAt(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

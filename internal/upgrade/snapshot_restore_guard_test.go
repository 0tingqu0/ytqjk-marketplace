package upgrade

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestRestoreGuardRejectsConcurrentSecondRecovery(t *testing.T) {
	if os.Getenv("YTQJK_RESTORE_SECOND_RECOVERY") == "1" {
		plan := restoreGuardHelperPlan(os.Getenv("YTQJK_RESTORE_ROOT"))
		if err := recoverPendingRestoreJournals(plan); errorContainsCode(err, "UPGRADE_RESTORE_IN_PROGRESS") {
			fmt.Fprintln(os.Stdout, "BLOCKED")
			return
		} else {
			t.Fatalf("second recovery = %v", err)
		}
	}
	fixture := newPersistedRestoreFixture(t)
	guard, err := acquireRestoreGuard(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.release() })
	output := runRestoreGuardHelper(t, fixture.plan, "YTQJK_RESTORE_SECOND_RECOVERY=1")
	if !strings.Contains(output, "BLOCKED") {
		t.Fatalf("second recovery output = %q", output)
	}
}

func TestRestoreGuardBlocksReplacementThenExpectedWrite(t *testing.T) {
	if os.Getenv("YTQJK_RESTORE_REPLACE_WRITER") == "1" {
		plan := restoreGuardHelperPlan(os.Getenv("YTQJK_RESTORE_ROOT"))
		guard, err := acquireRestoreGuard(plan)
		if errorContainsCode(err, "UPGRADE_RESTORE_IN_PROGRESS") {
			fmt.Fprintln(os.Stdout, "BLOCKED")
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer guard.release()
		path := os.Getenv("YTQJK_RESTORE_JOURNAL")
		journal, err := readRestoreJournalBound(guard, path)
		if err != nil {
			t.Fatal(err)
		}
		replacement := journal
		replacement.ManifestSHA256 = testOperationB
		if err := safeio.WriteJSON(path, replacement); err != nil {
			t.Fatal(err)
		}
		if err := safeio.WriteJSON(path, journal); err != nil {
			t.Fatal(err)
		}
		t.Fatal("replacement writer acquired restore guard")
	}
	fixture := newPersistedRestoreFixture(t)
	guard, err := acquireRestoreGuard(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.release() })
	output := runRestoreGuardHelper(
		t, fixture.plan, "YTQJK_RESTORE_REPLACE_WRITER=1", "YTQJK_RESTORE_JOURNAL="+fixture.journalPath,
	)
	if !strings.Contains(output, "BLOCKED") {
		t.Fatalf("replacement writer output = %q", output)
	}
	if _, err := readRestoreJournalBound(guard, fixture.journalPath); err != nil {
		t.Fatalf("blocked writer changed journal: %v", err)
	}
}

func TestRestoreBootstrapRejectsRecreatedUpgradeDirectory(t *testing.T) {
	plan := newRestoreGuardTestPlan(t)
	guard, err := acquireRestoreGuard(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.release(); err != nil {
		t.Fatal(err)
	}
	upgradePath := filepath.Join(plan.RuntimeRoot, "upgrade")
	if err := os.Rename(upgradePath, upgradePath+".retired"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(upgradePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRestoreGuard(plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("recreated upgrade directory error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(upgradePath, restoreGuardName)); !os.IsNotExist(err) {
		t.Fatalf("restore guard was recreated after identity drift: %v", err)
	}
}

func TestRestoreBootstrapRejectsRecreatedGuardFile(t *testing.T) {
	plan := newRestoreGuardTestPlan(t)
	guard, err := acquireRestoreGuard(plan)
	if err != nil {
		t.Fatal(err)
	}
	if err := guard.release(); err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(plan.RuntimeRoot, "upgrade", restoreGuardName)
	if err := os.Rename(guardPath, guardPath+".retired"); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.OpenFile(guardPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRestoreGuard(plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("recreated restore guard error = %v", err)
	}
}

func TestRestoreBootstrapAdoptsLegacyUpgradeDirectory(t *testing.T) {
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	upgradeRoot := filepath.Join(runtimeRoot, "upgrade")
	if err := os.MkdirAll(upgradeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyState := filepath.Join(upgradeRoot, "state.json")
	if err := os.WriteFile(legacyState, []byte("legacy-state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(upgradeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapRestoreControlRoot(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(upgradeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("legacy upgrade directory was replaced during bootstrap")
	}
	data, err := os.ReadFile(legacyState)
	if err != nil || string(data) != "legacy-state\n" {
		t.Fatalf("legacy state changed: data=%q error=%v", data, err)
	}
	guard, err := acquireRestoreGuardRoot(runtimeRoot)
	if err != nil {
		t.Fatalf("adopted restore guard unavailable: %v", err)
	}
	if err := guard.release(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreBootstrapPreservesLegacyGuardIdentity(t *testing.T) {
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	upgradeRoot := filepath.Join(runtimeRoot, "upgrade")
	if err := os.MkdirAll(upgradeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	guardPath := filepath.Join(upgradeRoot, restoreGuardName)
	guard, err := os.OpenFile(guardPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	before, err := guard.Stat()
	closeErr := guard.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	if err := bootstrapRestoreControlRoot(runtimeRoot); err != nil {
		t.Fatal(err)
	}
	after, err := os.Lstat(guardPath)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("legacy restore guard identity changed: error=%v", err)
	}
}

func TestRestoreAcquireDoesNotRebootstrapMissingPermanentRecord(t *testing.T) {
	plan := newRestoreGuardTestPlan(t)
	markerPath := filepath.Join(plan.RuntimeRoot, restoreBootstrapName)
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRestoreGuard(plan); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("missing restore bootstrap error = %v", err)
	}
	if _, err := os.Lstat(markerPath); !os.IsNotExist(err) {
		t.Fatalf("restore bootstrap record was recreated: %v", err)
	}
}

func TestHeldRestoreGuardDetectsDirectoryReplacementBeforeJournalWrite(t *testing.T) {
	fixture := newPersistedRestoreFixture(t)
	guard, err := acquireRestoreGuard(fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = guard.release() })
	upgradePath := filepath.Join(fixture.plan.RuntimeRoot, "upgrade")
	if err := os.Rename(upgradePath, upgradePath+".retired"); err != nil {
		t.Skipf("platform prevents replacement of an opened upgrade directory: %v", err)
	}
	if err := os.Mkdir(upgradePath, 0o700); err != nil {
		t.Fatal(err)
	}
	fixture.journal.guard = guard
	if err := writeRestoreJournal(
		fixture.journalPath, &fixture.journal, "SWAPPING", restoreDecisionRollback, 0, 1,
	); errorCodeOf(err) != "UPGRADE_RECOVERY_REQUIRED" {
		t.Fatalf("journal write after directory replacement = %v", err)
	}
	entries, err := os.ReadDir(upgradePath)
	if err != nil || len(entries) != 0 {
		t.Fatalf("replacement directory was mutated: entries=%d err=%v", len(entries), err)
	}
}

func runRestoreGuardHelper(t *testing.T, plan Plan, environment ...string) string {
	t.Helper()
	root := filepath.Dir(plan.RuntimeRoot)
	command := exec.Command(os.Args[0], "-test.run=^"+t.Name()+"$")
	command.Env = append(os.Environ(), append(environment, "YTQJK_RESTORE_ROOT="+root)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("restore guard helper: %v\n%s", err, output)
	}
	return string(output)
}

func restoreGuardHelperPlan(root string) Plan {
	return Plan{
		RuntimeRoot: filepath.Join(root, "runtime"), CodexRoot: filepath.Join(root, "codex"),
		KnowledgeRoot: filepath.Join(root, "knowledge"),
	}
}

func newRestoreGuardTestPlan(t *testing.T) Plan {
	t.Helper()
	plan := restoreGuardHelperPlan(t.TempDir())
	for _, path := range []string{plan.RuntimeRoot, plan.CodexRoot, plan.KnowledgeRoot} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := bootstrapRestoreControlRoot(plan.RuntimeRoot); err != nil {
		t.Fatal(err)
	}
	return plan
}

package knowledge

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	_ "modernc.org/sqlite"
)

func TestCreateProjectionPublishesVerifiedIsolatedDatabase(t *testing.T) {
	fixture := newProjectionFixture(t)
	beforeHash, err := safeio.FileSHA256(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(fixture.source)
	if err != nil {
		t.Fatal(err)
	}

	receipt, err := CreateProjection(context.Background(), fixture.request("verified"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "VERIFIED" || !receipt.SourceUnchanged {
		t.Fatalf("receipt = %#v", receipt)
	}
	final := filepath.Join(fixture.projections, "verified")
	manifestPath := filepath.Join(final, "manifest.json")
	projectionPath := filepath.Join(final, "projection.sqlite3")
	storedReceiptPath := filepath.Join(final, "receipt.json")
	for _, path := range []string{manifestPath, projectionPath, storedReceiptPath} {
		if info, statErr := os.Stat(path); statErr != nil || !info.Mode().IsRegular() {
			t.Fatalf("published file %s: info=%v err=%v", path, info, statErr)
		}
	}
	if _, err := os.Stat(filepath.Join(fixture.projections, "verified.staging")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging still exists: %v", err)
	}

	var manifest ProjectionManifest
	if err := safeio.ReadJSON(manifestPath, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Schema != ProjectionManifestSchema || manifest.OperationID != "verified" {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if manifest.Source.SHA256Before != beforeHash || manifest.Source.SHA256After != beforeHash ||
		manifest.Source.SizeBytesBefore != beforeInfo.Size() || manifest.Source.SizeBytesAfter != beforeInfo.Size() {
		t.Fatalf("source evidence = %#v", manifest.Source)
	}
	if manifest.Source.UserVersion != LatestSchema || manifest.Projection.UserVersion != LatestSchema ||
		manifest.Source.IntegrityCheck != "ok" || manifest.Projection.IntegrityCheck != "ok" ||
		manifest.Source.ForeignKeyViolations != 0 || manifest.Projection.ForeignKeyViolations != 0 {
		t.Fatalf("database evidence = source %#v projection %#v", manifest.Source, manifest.Projection)
	}
	if got, err := safeio.FileSHA256(projectionPath); err != nil || got != receipt.ProjectionSHA256 {
		t.Fatalf("projection SHA = %q, %v; want %q", got, err, receipt.ProjectionSHA256)
	}
	if got, err := safeio.FileSHA256(manifestPath); err != nil || got != receipt.ManifestSHA256 {
		t.Fatalf("manifest SHA = %q, %v; want %q", got, err, receipt.ManifestSHA256)
	}
	assertProjectionData(t, projectionPath)
	if got, err := safeio.FileSHA256(fixture.source); err != nil || got != beforeHash {
		t.Fatalf("source changed: SHA=%q err=%v want=%q", got, err, beforeHash)
	}

	verified, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
		KnowledgeRoot: fixture.root,
		ManifestPath:  manifestPath,
	})
	if err != nil || verified.Status != "VERIFIED" || verified.ProjectionSHA256 != receipt.ProjectionSHA256 {
		t.Fatalf("VerifyProjection = %#v, %v", verified, err)
	}
}

func TestCreateProjectionIsIdempotentWithoutRewritingArtifacts(t *testing.T) {
	fixture := newProjectionFixture(t)
	request := fixture.request("repeat")
	first, err := CreateProjection(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(fixture.projections, "repeat")
	paths := []string{
		filepath.Join(final, "projection.sqlite3"),
		filepath.Join(final, "manifest.json"),
		filepath.Join(final, "receipt.json"),
	}
	type fileEvidence struct {
		hash    string
		modTime time.Time
	}
	before := map[string]fileEvidence{}
	for _, path := range paths {
		hash, hashErr := safeio.FileSHA256(path)
		info, statErr := os.Stat(path)
		if hashErr != nil || statErr != nil {
			t.Fatalf("read %s: hash=%v stat=%v", path, hashErr, statErr)
		}
		before[path] = fileEvidence{hash: hash, modTime: info.ModTime()}
	}

	second, err := CreateProjection(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.ProjectionSHA256 != second.ProjectionSHA256 || second.Status != "VERIFIED" {
		t.Fatalf("idempotent receipt: first=%#v second=%#v", first, second)
	}
	for _, path := range paths {
		hash, hashErr := safeio.FileSHA256(path)
		info, statErr := os.Stat(path)
		if hashErr != nil || statErr != nil {
			t.Fatalf("re-read %s: hash=%v stat=%v", path, hashErr, statErr)
		}
		if evidence := before[path]; hash != evidence.hash || !info.ModTime().Equal(evidence.modTime) {
			t.Fatalf("idempotent create rewrote %s", path)
		}
	}
}

func TestCreateProjectionRejectsUnquiescedOrInvalidSource(t *testing.T) {
	tests := []struct {
		name string
		code string
		set  func(*testing.T, projectionFixture)
	}{
		{
			name: "wal sidecar",
			code: "SOURCE_NOT_QUIESCENT",
			set: func(t *testing.T, fixture projectionFixture) {
				if err := os.WriteFile(fixture.source+"-wal", []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "shm sidecar",
			code: "SOURCE_NOT_QUIESCENT",
			set: func(t *testing.T, fixture projectionFixture) {
				if err := os.WriteFile(fixture.source+"-shm", []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "rollback journal sidecar",
			code: "SOURCE_NOT_QUIESCENT",
			set: func(t *testing.T, fixture projectionFixture) {
				if err := os.WriteFile(fixture.source+"-journal", []byte("active"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "running lease",
			code: "SOURCE_NOT_QUIESCENT",
			set: func(t *testing.T, fixture projectionFixture) {
				database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(fixture.source))
				if err != nil {
					t.Fatal(err)
				}
				result, err := database.Exec(`INSERT INTO jobs(kind,payload,state,dedupe_key,created_at)
VALUES ('create_project','{}','QUEUED','projection-running','created')`)
				if err != nil {
					t.Fatal(err)
				}
				identifier, err := result.LastInsertId()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec(`UPDATE jobs SET state='RUNNING',owner='owner',
lease_expires_at='2999-01-01T00:00:00Z',heartbeat_at='heartbeat',attempt=attempt+1 WHERE id=?`, identifier); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
					t.Fatal(err)
				}
				if _, err := database.Exec("PRAGMA journal_mode=DELETE"); err != nil {
					t.Fatal(err)
				}
				if err := database.Close(); err != nil {
					t.Fatal(err)
				}
				assertSQLiteSidecarsAbsent(t, fixture.source)
			},
		},
		{
			name: "corrupt database",
			code: "SOURCE_INTEGRITY_FAILED",
			set: func(t *testing.T, fixture projectionFixture) {
				if err := os.WriteFile(fixture.source, []byte("not sqlite"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			test.set(t, fixture)
			_, err := CreateProjection(context.Background(), fixture.request("rejected"))
			assertProjectionCode(t, err, test.code)
			if _, statErr := os.Stat(filepath.Join(fixture.projections, "rejected")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("final artifact exists after rejection: %v", statErr)
			}
		})
	}
}

func TestVerifyProjectionRejectsTampering(t *testing.T) {
	for _, target := range []string{"manifest.json", "projection.sqlite3", "receipt.json"} {
		t.Run(target, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			if _, err := CreateProjection(context.Background(), fixture.request("tamper")); err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(fixture.projections, "tamper")
			path := filepath.Join(final, target)
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.Write([]byte("tampered")); err != nil {
				file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = VerifyProjection(context.Background(), VerifyProjectionRequest{
				KnowledgeRoot: fixture.root,
				ManifestPath:  filepath.Join(final, "manifest.json"),
			})
			assertProjectionCode(t, err, ProjectionInvalid)
		})
	}
}

func TestCreateProjectionRejectsPathEscapeAndIncompleteStaging(t *testing.T) {
	fixture := newProjectionFixture(t)
	escape := fixture.request("escape")
	escape.ProjectionRoot = filepath.Join(filepath.Dir(fixture.root), "outside")
	if _, err := CreateProjection(context.Background(), escape); err == nil {
		t.Fatal("CreateProjection accepted projection root escape")
	} else {
		assertProjectionCode(t, err, "PROJECTION_PATH_ESCAPE")
	}

	invalid := fixture.request("../invalid")
	_, err := CreateProjection(context.Background(), invalid)
	assertProjectionCode(t, err, "INVALID_PROJECTION_REQUEST")

	staging := filepath.Join(fixture.projections, "incomplete.staging")
	if err := os.MkdirAll(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(staging, "keep-me")
	if err := os.WriteFile(marker, []byte("evidence"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = CreateProjection(context.Background(), fixture.request("incomplete"))
	assertProjectionCode(t, err, "PROJECTION_INCOMPLETE")
	if data, readErr := os.ReadFile(marker); readErr != nil || string(data) != "evidence" {
		t.Fatalf("incomplete evidence changed: %q, %v", data, readErr)
	}
}

type projectionFixture struct {
	root, source, projections string
}

func newProjectionFixture(t *testing.T) projectionFixture {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "service", "knowledge.sqlite3")
	projections := filepath.Join(root, "handoffs", "projections")
	if err := os.MkdirAll(projections, 0o700); err != nil {
		t.Fatal(err)
	}
	service, err := Open(source)
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := service.CreateProject("project", "projection-fixture")
	if err == nil {
		_, err = service.CreateCandidate(projectID, "fixture", "projection content", "test")
	}
	if closeErr := service.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	return projectionFixture{root: root, source: source, projections: projections}
}

func (fixture projectionFixture) request(operationID string) ProjectionRequest {
	return ProjectionRequest{
		KnowledgeRoot:  fixture.root,
		SourcePath:     fixture.source,
		ProjectionRoot: fixture.projections,
		OperationID:    operationID,
	}
}

func assertProjectionData(t *testing.T, path string) {
	t.Helper()
	database, err := openProjectionDatabase(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow("SELECT COUNT(*) FROM documents").Scan(&count); err != nil || count != 1 {
		t.Fatalf("projection document count = %d, %v", count, err)
	}
}

func assertProjectionCode(t *testing.T, err error, code string) {
	t.Helper()
	var projectionErr *ProjectionError
	if !errors.As(err, &projectionErr) {
		t.Fatalf("error = %T %v, want *ProjectionError %s", err, err, code)
	}
	if projectionErr.Code != code {
		t.Fatalf("projection error code = %q, want %q", projectionErr.Code, code)
	}
}

func assertSQLiteSidecarsAbsent(t *testing.T, path string) {
	t.Helper()
	for _, suffix := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Lstat(path + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("SQLite sidecar %s is still present: %v", suffix, err)
		}
	}
}

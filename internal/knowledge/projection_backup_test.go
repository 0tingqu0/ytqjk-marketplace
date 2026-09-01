package knowledge

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func TestCreateProjectionPreservesImplicitRowIDs(t *testing.T) {
	fixture := newProjectionFixture(t)
	database, err := openDatabase(fixture.source)
	if err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(`CREATE TABLE projection_rowids(value TEXT);
INSERT INTO projection_rowids(rowid,value) VALUES (7,'seven'),(9001,'nine-thousand-one')`)
	if closeErr := database.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}

	if _, err := CreateProjection(context.Background(), fixture.request("rowids")); err != nil {
		t.Fatal(err)
	}
	projection := filepath.Join(fixture.projections, "rowids", projectionDatabaseName)
	readOnly, err := openProjectionDatabase(projection, true)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	rows, err := readOnly.Query("SELECT rowid,value FROM projection_rowids ORDER BY rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := []struct {
		rowID int64
		value string
	}{{7, "seven"}, {9001, "nine-thousand-one"}}
	index := 0
	for rows.Next() {
		if index >= len(want) {
			t.Fatal("projection contains unexpected implicit rowid")
		}
		var rowID int64
		var value string
		if err := rows.Scan(&rowID, &value); err != nil {
			t.Fatal(err)
		}
		if rowID != want[index].rowID || value != want[index].value {
			t.Fatalf("row %d = (%d,%q), want (%d,%q)", index, rowID, value, want[index].rowID, want[index].value)
		}
		index++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if index != len(want) {
		t.Fatalf("projection row count = %d, want %d", index, len(want))
	}
}

func TestCreateProjectionSupportsEscapedSQLitePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "knowledge 空格 # percent%")
	fixture := newProjectionFixtureAt(t, root)
	receipt, err := CreateProjection(context.Background(), fixture.request("escaped-path"))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "VERIFIED" {
		t.Fatalf("receipt = %#v", receipt)
	}
	assertProjectionData(t, filepath.Join(fixture.projections, "escaped-path", projectionDatabaseName))
}

func TestSQLiteFileURIEscapesReservedCharacters(t *testing.T) {
	uri, err := sqliteFileURI(filepath.Join("segment ?#% 汉字", "knowledge.sqlite3"), url.Values{"mode": {"ro"}})
	if err != nil {
		t.Fatal(err)
	}
	pathPart, _, found := strings.Cut(uri, "?")
	if !found {
		t.Fatalf("SQLite URI has no query: %q", uri)
	}
	for _, escaped := range []string{"%3F", "%23", "%25", "%E6%B1%89%E5%AD%97"} {
		if !strings.Contains(pathPart, escaped) {
			t.Errorf("SQLite URI path %q does not contain %q", pathPart, escaped)
		}
	}
}

func TestVerifyProjectionBindsStoredReceipt(t *testing.T) {
	fixture := newProjectionFixture(t)
	if _, err := CreateProjection(context.Background(), fixture.request("receipt-binding")); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(fixture.projections, "receipt-binding")
	receiptPath := filepath.Join(final, projectionReceiptName)
	var receipt ProjectionReceipt
	if err := safeio.ReadJSON(receiptPath, &receipt); err != nil {
		t.Fatal(err)
	}
	receipt.VerifiedAt = "2000-01-01T00:00:00Z"
	if err := safeio.WriteJSON(receiptPath, receipt); err != nil {
		t.Fatal(err)
	}
	_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
		KnowledgeRoot: fixture.root,
		ManifestPath:  filepath.Join(final, projectionManifestName),
	})
	assertProjectionCode(t, err, ProjectionInvalid)
}

func TestVerifyProjectionRejectsReceiptFieldTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ProjectionReceipt)
	}{
		{"operation ID", func(receipt *ProjectionReceipt) { receipt.OperationID = "other-operation" }},
		{"status", func(receipt *ProjectionReceipt) { receipt.Status = "ALREADY_VERIFIED" }},
		{"manifest path", func(receipt *ProjectionReceipt) {
			receipt.ManifestRelativePath = "handoffs/projections/other/manifest.json"
		}},
		{"manifest path alias", func(receipt *ProjectionReceipt) {
			receipt.ManifestRelativePath = "./" + receipt.ManifestRelativePath
		}},
		{"manifest hash", func(receipt *ProjectionReceipt) { receipt.ManifestSHA256 = strings.Repeat("0", 64) }},
		{"projection hash", func(receipt *ProjectionReceipt) { receipt.ProjectionSHA256 = strings.Repeat("0", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			if _, err := CreateProjection(context.Background(), fixture.request("receipt-fields")); err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(fixture.projections, "receipt-fields")
			receiptPath := filepath.Join(final, projectionReceiptName)
			var receipt ProjectionReceipt
			if err := safeio.ReadJSON(receiptPath, &receipt); err != nil {
				t.Fatal(err)
			}
			test.mutate(&receipt)
			if err := safeio.WriteJSON(receiptPath, receipt); err != nil {
				t.Fatal(err)
			}
			_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
				KnowledgeRoot: fixture.root,
				ManifestPath:  filepath.Join(final, projectionManifestName),
			})
			assertProjectionCode(t, err, ProjectionInvalid)
		})
	}
}

func TestVerifyProjectionRejectsUnexpectedProjectionFiles(t *testing.T) {
	for _, name := range []string{
		projectionDatabaseName + "-wal",
		projectionDatabaseName + "-shm",
		projectionDatabaseName + "-journal",
		"unexpected.txt",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newProjectionFixture(t)
			if _, err := CreateProjection(context.Background(), fixture.request("unexpected-file")); err != nil {
				t.Fatal(err)
			}
			final := filepath.Join(fixture.projections, "unexpected-file")
			if err := os.WriteFile(filepath.Join(final, name), []byte("unexpected"), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
				KnowledgeRoot: fixture.root,
				ManifestPath:  filepath.Join(final, projectionManifestName),
			})
			assertProjectionCode(t, err, ProjectionInvalid)
		})
	}
}

func TestVerifyProjectionRejectsLinkedManifest(t *testing.T) {
	fixture := newProjectionFixture(t)
	if _, err := CreateProjection(context.Background(), fixture.request("linked-manifest")); err != nil {
		t.Fatal(err)
	}
	final := filepath.Join(fixture.projections, "linked-manifest")
	manifestPath := filepath.Join(final, projectionManifestName)
	realManifest := filepath.Join(fixture.root, "manifest.real.json")
	if err := os.Rename(manifestPath, realManifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realManifest, manifestPath); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
		KnowledgeRoot: fixture.root,
		ManifestPath:  manifestPath,
	})
	assertProjectionCode(t, err, ProjectionInvalid)
}

func TestVerifyProjectionRejectsMissingOrLinkedReceipt(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		fixture := newProjectionFixture(t)
		if _, err := CreateProjection(context.Background(), fixture.request("missing-receipt")); err != nil {
			t.Fatal(err)
		}
		final := filepath.Join(fixture.projections, "missing-receipt")
		if err := os.Remove(filepath.Join(final, projectionReceiptName)); err != nil {
			t.Fatal(err)
		}
		_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
			KnowledgeRoot: fixture.root,
			ManifestPath:  filepath.Join(final, projectionManifestName),
		})
		assertProjectionCode(t, err, ProjectionInvalid)
	})

	t.Run("symbolic link", func(t *testing.T) {
		fixture := newProjectionFixture(t)
		if _, err := CreateProjection(context.Background(), fixture.request("linked-receipt")); err != nil {
			t.Fatal(err)
		}
		final := filepath.Join(fixture.projections, "linked-receipt")
		receiptPath := filepath.Join(final, projectionReceiptName)
		realReceipt := filepath.Join(final, "receipt.real.json")
		if err := os.Rename(receiptPath, realReceipt); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Base(realReceipt), receiptPath); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
		_, err := VerifyProjection(context.Background(), VerifyProjectionRequest{
			KnowledgeRoot: fixture.root,
			ManifestPath:  filepath.Join(final, projectionManifestName),
		})
		assertProjectionCode(t, err, ProjectionInvalid)
	})
}

func newProjectionFixtureAt(t *testing.T, root string) projectionFixture {
	t.Helper()
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

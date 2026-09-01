package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	_ "modernc.org/sqlite"
)

const (
	snapshotSchema       = "ytqjk-upgrade-snapshot/v2"
	snapshotManifestName = "snapshot.json"
	snapshotDigestName   = "snapshot.sha256"
)

type Snapshot struct {
	Schema            string         `json:"schema"`
	ID                string         `json:"id"`
	CreatedAt         time.Time      `json:"created_at"`
	FromVersion       string         `json:"from_version"`
	ToVersion         string         `json:"to_version"`
	PreviousMaxSchema int            `json:"previous_max_schema"`
	DatabaseSchema    int            `json:"database_schema"`
	Items             []SnapshotItem `json:"items"`
	RuntimeBinary     bool           `json:"-"`
	ManifestSHA256    string         `json:"-"`
}

type SnapshotItem struct {
	Root         string `json:"root"`
	RelativePath string `json:"relative_path"`
	Class        string `json:"class"`
	Kind         string `json:"kind"`
	Present      bool   `json:"present"`
	Mode         uint32 `json:"mode,omitempty"`
	Size         int64  `json:"size,omitempty"`
	SHA256       string `json:"sha256,omitempty"`
}

func captureSnapshot(ctx context.Context, plan Plan) (Snapshot, error) {
	if err := bootstrapRestoreControlRoot(plan.RuntimeRoot); err != nil {
		return Snapshot{}, err
	}
	identifier, err := safeio.RandomHex(32)
	if err != nil {
		return Snapshot{}, err
	}
	root := snapshotRoot(plan.RuntimeRoot, identifier)
	if err := os.MkdirAll(root, 0o700); err != nil {
		return Snapshot{}, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	items, err := captureSnapshotInventory(ctx, plan, root)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot := Snapshot{
		Schema: snapshotSchema, ID: identifier, CreatedAt: time.Now().UTC(),
		FromVersion: plan.FromVersion, ToVersion: plan.ToVersion,
		PreviousMaxSchema: plan.PreviousMaxSchema, DatabaseSchema: plan.DatabaseSchema,
		Items: items,
	}
	hydrateSnapshotCompatibility(&snapshot)
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	manifest := filepath.Join(root, snapshotManifestName)
	if err := safeio.WriteJSON(manifest, snapshot); err != nil {
		return Snapshot{}, err
	}
	digest, err := safeio.FileSHA256(manifest)
	if err != nil {
		return Snapshot{}, err
	}
	if err := safeio.AtomicWrite(filepath.Join(root, snapshotDigestName), []byte(digest+"\n"), 0o600); err != nil {
		return Snapshot{}, err
	}
	snapshot.ManifestSHA256 = digest
	if err := verifySnapshot(root, snapshot); err != nil {
		return Snapshot{}, err
	}
	complete = true
	return snapshot, nil
}

func readSnapshot(runtimeRoot, identifier string) (Snapshot, error) {
	if !hexDigestPattern.MatchString(identifier) {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	root := snapshotRoot(runtimeRoot, identifier)
	if err := verifySnapshotManifest(root); err != nil {
		return Snapshot{}, err
	}
	manifest := filepath.Join(root, snapshotManifestName)
	data, err := os.ReadFile(manifest)
	if err != nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	var snapshot Snapshot
	if err := decodeStrictJSON(data, &snapshot); err != nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	if snapshot.ID != identifier {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	snapshot.ManifestSHA256, err = safeio.FileSHA256(manifest)
	if err != nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_CORRUPT", err)
	}
	hydrateSnapshotCompatibility(&snapshot)
	return snapshot, verifySnapshot(root, snapshot)
}

func hydrateSnapshotCompatibility(snapshot *Snapshot) {
	expected := filepath.ToSlash(filepath.Join("bin", runtimeBinaryName()))
	for _, item := range snapshot.Items {
		if item.Root == snapshotRootRuntime && item.RelativePath == expected && item.Present {
			snapshot.RuntimeBinary = true
			return
		}
	}
}

func verifySnapshotManifest(root string) error {
	data, err := os.ReadFile(filepath.Join(root, snapshotDigestName))
	if err != nil {
		return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
	}
	digest := strings.TrimSuffix(string(data), "\n")
	if string(data) != digest+"\n" || !hexDigestPattern.MatchString(digest) {
		return failure("UPGRADE_SNAPSHOT_CORRUPT", nil)
	}
	actual, err := safeio.FileSHA256(filepath.Join(root, snapshotManifestName))
	if err != nil || actual != digest {
		return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
	}
	return nil
}

func pruneSnapshots(runtimeRoot, keep string) error {
	root := filepath.Join(runtimeRoot, "upgrade", "snapshots")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && hexDigestPattern.MatchString(entry.Name()) && entry.Name() != keep {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		path, err := safeio.Contained(root, filepath.Join(root, name))
		if err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func snapshotRoot(runtimeRoot, identifier string) string {
	return filepath.Join(runtimeRoot, "upgrade", "snapshots", identifier)
}

func runtimeBinaryName() string {
	if runtime.GOOS == "windows" {
		return "ytqjk.exe"
	}
	return "ytqjk"
}

func fileModeFor(path string) os.FileMode {
	if filepath.Base(path) == runtimeBinaryName() {
		return 0o700
	}
	return 0o600
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func backupDatabase(ctx context.Context, source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	database, err := sql.Open("sqlite", "file:"+filepath.ToSlash(source)+"?_pragma=busy_timeout(15000)")
	if err != nil {
		return err
	}
	defer database.Close()
	var busy, logFrames, checkpointed int
	if err := database.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return errors.New("database checkpoint is busy")
	}
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	return nil
}

package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	_ "modernc.org/sqlite"
)

const snapshotSchema = "ytqjk-upgrade-snapshot/v1"

type Snapshot struct {
	Schema            string            `json:"schema"`
	ID                string            `json:"id"`
	CreatedAt         time.Time         `json:"created_at"`
	FromVersion       string            `json:"from_version"`
	ToVersion         string            `json:"to_version"`
	PreviousMaxSchema int               `json:"previous_max_schema"`
	DatabaseSchema    int               `json:"database_schema"`
	RuntimeBinary     bool              `json:"runtime_binary"`
	ManagedManifest   bool              `json:"managed_manifest"`
	Plugins           map[string]bool   `json:"plugins"`
	Database          bool              `json:"database"`
	Hashes            map[string]string `json:"hashes"`
}

func captureSnapshot(ctx context.Context, plan Plan) (Snapshot, error) {
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
	snapshot := Snapshot{
		Schema: snapshotSchema, ID: identifier, CreatedAt: time.Now().UTC(),
		FromVersion: plan.FromVersion, ToVersion: plan.ToVersion,
		PreviousMaxSchema: plan.PreviousMaxSchema, DatabaseSchema: plan.DatabaseSchema,
		Plugins: map[string]bool{}, Hashes: map[string]string{},
	}
	binary := filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName())
	if regularFile(binary) {
		target := filepath.Join(root, "runtime", "bin", runtimeBinaryName())
		if err := safeio.CopyFile(binary, target, 0o700); err != nil {
			return Snapshot{}, err
		}
		snapshot.RuntimeBinary = true
		hash, err := safeio.FileSHA256(target)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Hashes["runtime"] = hash
	}
	pluginsRoot := filepath.Join(plan.CodexRoot, "plugins")
	for _, name := range pluginNames {
		source := filepath.Join(pluginsRoot, name)
		if directoryExists(source) {
			target := filepath.Join(root, "codex", "plugins", name)
			if err := safeio.CopyTree(source, target); err != nil {
				return Snapshot{}, err
			}
			snapshot.Plugins[name] = true
			hash, err := safeio.TreeHash(target)
			if err != nil {
				return Snapshot{}, err
			}
			snapshot.Hashes["plugin:"+name] = hash
		}
	}
	manifest := filepath.Join(pluginsRoot, ".ytqjk-managed-plugins.json")
	if regularFile(manifest) {
		target := filepath.Join(root, "codex", "plugins", ".ytqjk-managed-plugins.json")
		if err := safeio.CopyFile(manifest, target, 0o600); err != nil {
			return Snapshot{}, err
		}
		snapshot.ManagedManifest = true
		hash, err := safeio.FileSHA256(target)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Hashes["manifest"] = hash
	}
	database := filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3")
	if regularFile(database) {
		backup := filepath.Join(root, "knowledge", "service", "knowledge.sqlite3")
		if err := backupDatabase(ctx, database, backup); err != nil {
			return Snapshot{}, err
		}
		snapshot.Database = true
		hash, err := safeio.FileSHA256(backup)
		if err != nil {
			return Snapshot{}, err
		}
		snapshot.Hashes["database"] = hash
	}
	if err := safeio.WriteJSON(filepath.Join(root, "snapshot.json"), snapshot); err != nil {
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
	var snapshot Snapshot
	if err := safeio.ReadJSON(filepath.Join(root, "snapshot.json"), &snapshot); err != nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	if snapshot.Schema != snapshotSchema || snapshot.ID != identifier || snapshot.Plugins == nil || snapshot.Hashes == nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", nil)
	}
	if _, err := parseVersion(snapshot.FromVersion); err != nil {
		return Snapshot{}, failure("UPGRADE_SNAPSHOT_INVALID", err)
	}
	return snapshot, verifySnapshot(root, snapshot)
}

func verifySnapshot(root string, snapshot Snapshot) error {
	checks := map[string]string{}
	if snapshot.RuntimeBinary {
		checks["runtime"] = filepath.Join(root, "runtime", "bin", runtimeBinaryName())
	}
	if snapshot.ManagedManifest {
		checks["manifest"] = filepath.Join(root, "codex", "plugins", ".ytqjk-managed-plugins.json")
	}
	for _, name := range pluginNames {
		if snapshot.Plugins[name] {
			path := filepath.Join(root, "codex", "plugins", name)
			hash, err := safeio.TreeHash(path)
			if err != nil || hash != snapshot.Hashes["plugin:"+name] {
				return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
			}
		}
	}
	if snapshot.Database {
		checks["database"] = filepath.Join(root, "knowledge", "service", "knowledge.sqlite3")
	}
	for key, path := range checks {
		hash, err := safeio.FileSHA256(path)
		if err != nil || hash != snapshot.Hashes[key] {
			return failure("UPGRADE_SNAPSHOT_CORRUPT", err)
		}
	}
	return nil
}

func restoreSnapshot(plan Plan, snapshot Snapshot, restoreDatabase bool) error {
	root := snapshotRoot(plan.RuntimeRoot, snapshot.ID)
	var items []restoreItem
	pluginsRoot := filepath.Join(plan.CodexRoot, "plugins")
	for _, name := range pluginNames {
		items = append(items, restoreItem{
			Target: filepath.Join(pluginsRoot, name), Source: filepath.Join(root, "codex", "plugins", name),
			Present: snapshot.Plugins[name], Directory: true,
		})
	}
	items = append(items, restoreItem{
		Target: filepath.Join(pluginsRoot, ".ytqjk-managed-plugins.json"),
		Source: filepath.Join(root, "codex", "plugins", ".ytqjk-managed-plugins.json"), Present: snapshot.ManagedManifest,
	})
	items = append(items, restoreItem{
		Target: filepath.Join(plan.RuntimeRoot, "bin", runtimeBinaryName()),
		Source: filepath.Join(root, "runtime", "bin", runtimeBinaryName()), Present: snapshot.RuntimeBinary,
	})
	if err := transactionalRestore(snapshot.ID, items); err != nil {
		return err
	}
	if restoreDatabase && snapshot.Database {
		target := filepath.Join(plan.KnowledgeRoot, "service", "knowledge.sqlite3")
		if err := restoreDatabaseFile(filepath.Join(root, "knowledge", "service", "knowledge.sqlite3"), target); err != nil {
			return err
		}
	}
	return nil
}

type restoreItem struct {
	Target    string
	Source    string
	Present   bool
	Directory bool
	staged    string
	backup    string
}

func transactionalRestore(identifier string, items []restoreItem) error {
	for index := range items {
		item := &items[index]
		if err := os.MkdirAll(filepath.Dir(item.Target), 0o700); err != nil {
			cleanupRestore(items)
			return err
		}
		item.staged = filepath.Join(filepath.Dir(item.Target), ".ytqjk-restore-stage-"+identifier+"-"+filepath.Base(item.Target))
		item.backup = filepath.Join(filepath.Dir(item.Target), ".ytqjk-restore-backup-"+identifier+"-"+filepath.Base(item.Target))
		if _, err := os.Lstat(item.staged); !errors.Is(err, os.ErrNotExist) {
			cleanupRestore(items)
			return errors.New("restore staging path already exists")
		}
		if _, err := os.Lstat(item.backup); !errors.Is(err, os.ErrNotExist) {
			cleanupRestore(items)
			return errors.New("restore backup path already exists")
		}
		if item.Present {
			if item.Directory {
				if err := safeio.CopyTree(item.Source, item.staged); err != nil {
					cleanupRestore(items)
					return err
				}
			} else if err := safeio.CopyFile(item.Source, item.staged, fileModeFor(item.Target)); err != nil {
				cleanupRestore(items)
				return err
			}
		}
	}
	changed := -1
	for index := range items {
		item := &items[index]
		if _, err := os.Lstat(item.Target); err == nil {
			if err := os.Rename(item.Target, item.backup); err != nil {
				rollbackRestore(items, changed)
				return err
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			rollbackRestore(items, changed)
			return err
		}
		changed = index
		if item.Present {
			if err := os.Rename(item.staged, item.Target); err != nil {
				rollbackRestore(items, changed)
				return err
			}
		}
	}
	cleanupRestore(items)
	return nil
}

func rollbackRestore(items []restoreItem, changed int) {
	for index := changed; index >= 0; index-- {
		item := items[index]
		_ = os.RemoveAll(item.Target)
		if _, err := os.Lstat(item.backup); err == nil {
			_ = os.Rename(item.backup, item.Target)
		}
	}
	cleanupRestore(items)
}

func cleanupRestore(items []restoreItem) {
	for _, item := range items {
		_ = os.RemoveAll(item.staged)
		_ = os.RemoveAll(item.backup)
	}
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
	if _, err := database.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, "VACUUM INTO ?", destination); err != nil {
		return err
	}
	return nil
}

func restoreDatabaseFile(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary := destination + ".rollback"
	prior := destination + ".pre-rollback"
	_ = os.Remove(temporary)
	if _, err := os.Lstat(prior); !errors.Is(err, os.ErrNotExist) {
		return errors.New("database rollback backup already exists")
	}
	if err := safeio.CopyFile(source, temporary, 0o600); err != nil {
		return err
	}
	hadPrior := false
	if _, err := os.Lstat(destination); err == nil {
		if err := os.Rename(destination, prior); err != nil {
			_ = os.Remove(temporary)
			return err
		}
		hadPrior = true
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(temporary)
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Remove(destination + suffix)
	}
	if err := os.Rename(temporary, destination); err != nil {
		if hadPrior {
			_ = os.Rename(prior, destination)
		}
		_ = os.Remove(temporary)
		return err
	}
	if hadPrior {
		_ = os.Remove(prior)
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

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0
}

func directoryExists(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0
}

func fileModeFor(path string) os.FileMode {
	if filepath.Base(path) == runtimeBinaryName() {
		return 0o700
	}
	return 0o600
}

package knowledge

var schemaV2Statements = splitStatements(`
CREATE TABLE IF NOT EXISTS snapshots (
  id TEXT PRIMARY KEY, project_id TEXT NOT NULL REFERENCES projects(id),
  generation INTEGER NOT NULL, state TEXT NOT NULL CHECK(state IN ('BUILDING','ACTIVE')),
  created_at TEXT NOT NULL, UNIQUE(project_id, generation)
);
CREATE TABLE IF NOT EXISTS snapshot_versions (
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id),
  document_id TEXT NOT NULL REFERENCES documents(id),
  version_id INTEGER NOT NULL REFERENCES versions(id),
  PRIMARY KEY(snapshot_id, document_id)
);
CREATE TABLE IF NOT EXISTS active_snapshots (
  project_id TEXT PRIMARY KEY REFERENCES projects(id),
  snapshot_id TEXT NOT NULL REFERENCES snapshots(id)
);
CREATE TRIGGER IF NOT EXISTS snapshots_insert_guard BEFORE INSERT ON snapshots
WHEN NEW.state!='BUILDING'
BEGIN SELECT RAISE(ABORT, 'snapshots begin building'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_immutable BEFORE UPDATE ON snapshots WHEN NOT (
  OLD.state='BUILDING' AND NEW.state='ACTIVE' AND NEW.id=OLD.id
  AND NEW.project_id=OLD.project_id AND NEW.generation=OLD.generation
  AND NEW.created_at=OLD.created_at)
BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshots_no_delete BEFORE DELETE ON snapshots
BEGIN SELECT RAISE(ABORT, 'snapshots are immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_insert_guard BEFORE INSERT ON snapshot_versions
WHEN NOT EXISTS (SELECT 1 FROM snapshots s JOIN documents d ON d.id=NEW.document_id
  JOIN versions v ON v.id=NEW.version_id AND v.document_id=d.id
  WHERE s.id=NEW.snapshot_id AND s.project_id=d.project_id AND s.state='BUILDING')
BEGIN SELECT RAISE(ABORT, 'snapshot membership requires building snapshot'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_immutable BEFORE UPDATE ON snapshot_versions
BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END;
CREATE TRIGGER IF NOT EXISTS snapshot_versions_no_delete BEFORE DELETE ON snapshot_versions
BEGIN SELECT RAISE(ABORT, 'snapshot membership is immutable'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_insert_guard BEFORE INSERT ON active_snapshots
WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id=NEW.snapshot_id
  AND project_id=NEW.project_id AND state='ACTIVE')
BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_update_guard BEFORE UPDATE ON active_snapshots
WHEN NOT EXISTS (SELECT 1 FROM snapshots WHERE id=NEW.snapshot_id
  AND project_id=NEW.project_id AND state='ACTIVE')
BEGIN SELECT RAISE(ABORT, 'active snapshot must be active generation'); END;
CREATE TRIGGER IF NOT EXISTS active_snapshots_no_delete BEFORE DELETE ON active_snapshots
BEGIN SELECT RAISE(ABORT, 'active snapshot pointer is immutable'); END;
`)

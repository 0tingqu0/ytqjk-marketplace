from __future__ import annotations

import io
import json
import os
import subprocess
import sys
import tempfile
import threading
import unittest
from contextlib import contextmanager, redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import orphan_cleanup_transaction as transaction  # noqa: E402
import orphan_project_cleanup as cleanup  # noqa: E402
import session_memory  # noqa: E402
from file_lock import exclusive_file_lock  # noqa: E402
from rag_common import SCHEMA_VERSION, atomic_json  # noqa: E402
from rag_locks import maintenance_lock  # noqa: E402


def make_directory_link(link: Path, target: Path) -> None:
    try:
        link.symlink_to(target, target_is_directory=True)
        return
    except OSError:
        if os.name != "nt":
            raise
    subprocess.run(
        ["cmd.exe", "/c", "mklink", "/J", str(link), str(target)],
        check=True,
        capture_output=True,
    )


class OrphanCleanupSafetyTest(unittest.TestCase):
    def fixture(self) -> tuple[Path, str, str]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name) / "knowledge"
        project_id = "orphan-safe"
        alias = str(Path(temporary.name) / "missing-source")
        project = root / "projects" / project_id
        project.mkdir(parents=True)
        (project / "payload.txt").write_text("original", encoding="utf-8")
        identity = {"id": project_id, "root": alias, "remote": ""}
        atomic_json(
            project / "manifest.json",
            {
                "schema_version": SCHEMA_VERSION,
                "identity": identity,
                "indexed_identity": identity,
            },
        )
        atomic_json(
            root / "catalog.json",
            {
                "schema_version": SCHEMA_VERSION,
                "projects": {
                    project_id: {
                        "name": project_id,
                        "remote": "",
                        "path_aliases": [alias],
                    }
                },
            },
        )
        return root, project_id, alias

    def test_catalog_schema_must_match(self) -> None:
        root, project_id, _ = self.fixture()
        catalog = json.loads(
            (root / "catalog.json").read_text(encoding="utf-8")
        )
        catalog["schema_version"] = SCHEMA_VERSION + 1
        atomic_json(root / "catalog.json", catalog)

        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "INVALID_CATALOG_SCHEMA"
        ):
            cleanup.cleanup_orphan_projects(root, [project_id])

    def test_catalog_rejects_invalid_entry_and_file_reparse(self) -> None:
        root, project_id, _ = self.fixture()
        catalog_path = root / "catalog.json"
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
        catalog["projects"]["invalid"] = {"remote": 3}
        atomic_json(catalog_path, catalog)
        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "INVALID_CATALOG_ENTRY"
        ):
            cleanup.cleanup_orphan_projects(root, [project_id])

        catalog["projects"].pop("invalid")
        target = root.parent / "external-catalog"
        target.mkdir()
        atomic_json(target / "catalog.json", catalog)
        catalog_path.unlink()
        make_directory_link(catalog_path, target)
        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "UNSAFE_CATALOG_PATH"
        ):
            cleanup.cleanup_orphan_projects(root, [project_id])

    def test_manifest_schema_and_identities_must_match(self) -> None:
        mutations = (
            ("schema_version", SCHEMA_VERSION + 1),
            ("identity.id", "wrong"),
            ("identity.root", "C:\\wrong"),
            ("indexed_identity.remote", "https://example.test/repo"),
        )
        for field, value in mutations:
            with self.subTest(field=field):
                root, project_id, _ = self.fixture()
                path = root / "projects" / project_id / "manifest.json"
                manifest = json.loads(path.read_text(encoding="utf-8"))
                if "." in field:
                    section, key = field.split(".")
                    manifest[section][key] = value
                else:
                    manifest[field] = value
                atomic_json(path, manifest)

                result = cleanup.cleanup_orphan_projects(
                    root, [project_id]
                )

                self.assertFalse(result["projects"][0]["eligible"])

    def test_recreated_source_preserves_backup_on_rollback_conflict(
        self,
    ) -> None:
        root, project_id, _ = self.fixture()
        original_catalog = (root / "catalog.json").read_bytes()
        source = root / "projects" / project_id
        real_atomic = transaction.atomic_json

        def race_at_receipt(path: Path, value: object) -> None:
            if path.name == "receipt.json":
                source.mkdir(parents=True)
                (source / "concurrent.txt").write_text(
                    "new", encoding="utf-8"
                )
                raise OSError("receipt failure")
            real_atomic(path, value)

        with mock.patch.object(transaction, "atomic_json", race_at_receipt):
            with self.assertRaisesRegex(
                RuntimeError, "ROLLBACK_FAILED:source-conflict"
            ):
                cleanup.cleanup_orphan_projects(
                    root,
                    [project_id],
                    apply=True,
                    yes=True,
                    maintenance_window=True,
                    batch_id="source-conflict",
                )

        batch = root / ".backups/orphan-projects/source-conflict"
        self.assertEqual(
            (root / "catalog.json").read_bytes(), original_catalog
        )
        self.assertTrue((source / "concurrent.txt").is_file())
        self.assertTrue((batch / "projects" / project_id).is_dir())

    def test_foreign_batch_race_is_not_deleted(self) -> None:
        root, project_id, _ = self.fixture()
        batch = root / ".backups/orphan-projects/foreign"
        real_mkdir = Path.mkdir

        def race(path: Path, *args: object, **kwargs: object) -> None:
            if path == batch and not os.path.lexists(path):
                real_mkdir(path, parents=True)
                (path / "foreign.txt").write_text("keep", encoding="utf-8")
                raise FileExistsError("foreign batch")
            real_mkdir(path, *args, **kwargs)

        with mock.patch.object(Path, "mkdir", race):
            with self.assertRaises(cleanup.CleanupRejected):
                cleanup.cleanup_orphan_projects(
                    root, [project_id], apply=True, yes=True,
                    maintenance_window=True, batch_id="foreign",
                )
        self.assertEqual(
            (batch / "foreign.txt").read_text(encoding="utf-8"), "keep"
        )

    def test_anchor_creation_waits_for_maintenance_lock(self) -> None:
        root, project_id, _ = self.fixture()
        started = threading.Event()
        finished = threading.Event()

        def create_anchor() -> None:
            started.set()
            session_memory.ensure_anchor(root, "racing-session", project_id)
            finished.set()

        with exclusive_file_lock(maintenance_lock(root)):
            thread = threading.Thread(target=create_anchor)
            thread.start()
            self.assertTrue(started.wait(1))
            thread.join(0.1)
            self.assertTrue(thread.is_alive())

        thread.join(2)
        self.assertTrue(finished.is_set())
        self.assertTrue(
            session_memory.anchor_path(root, "racing-session").is_file()
        )

    def test_reparse_backup_and_lock_parents_are_rejected(self) -> None:
        for managed in (".backups", ".locks"):
            with self.subTest(managed=managed):
                root, project_id, _ = self.fixture()
                external = root.parent / f"external-{managed[1:]}"
                external.mkdir()
                linked = root / managed
                make_directory_link(linked, external)

                with self.assertRaisesRegex(
                    cleanup.CleanupRejected,
                    "UNSAFE_MANAGED_DIRECTORY",
                ):
                    cleanup.cleanup_orphan_projects(
                        root,
                        [project_id],
                        apply=True,
                        yes=True,
                        maintenance_window=True,
                        batch_id=f"unsafe-{managed[1:]}",
                    )

    def test_managed_directory_races_are_rejected(self) -> None:
        root, project_id, _ = self.fixture()
        (external := root.parent / "racing-locks").mkdir()

        @contextmanager
        def racing_lock(path: Path):
            if path.name.startswith("project-"):
                (root / ".locks").rmdir()
                make_directory_link(root / ".locks", external)
            yield

        with mock.patch.object(cleanup, "exclusive_file_lock", racing_lock):
            with self.assertRaisesRegex(
                cleanup.CleanupRejected, "UNSAFE_MANAGED_DIRECTORY"
            ):
                cleanup.cleanup_orphan_projects(root, [project_id])

        patch_apply = mock.patch.object
        for race in ("backup", "source"):
            with self.subTest(race=race):
                root, project_id, _ = self.fixture()
                original_apply = cleanup.apply_transaction

                def race_apply(*args: object, **kwargs: object):
                    if race == "backup":
                        backup = root / ".backups"
                        (backup / "orphan-projects").rmdir()
                        backup.rmdir()
                        target = root.parent / "racing-backups"
                    else:
                        source = root / "projects" / project_id
                        os.replace(source, root.parent / "saved-project")
                        backup = source
                        target = root.parent / "racing-source"
                    target.mkdir()
                    make_directory_link(backup, target)
                    return original_apply(*args, **kwargs)

                with patch_apply(cleanup, "apply_transaction", race_apply):
                    with self.assertRaises(cleanup.CleanupRejected):
                        cleanup.cleanup_orphan_projects(
                            root, [project_id], apply=True, yes=True,
                            maintenance_window=True, batch_id=f"race-{race}",
                        )

    def test_linked_project_directory_is_rejected(self) -> None:
        root, project_id, _ = self.fixture()
        project = root / "projects" / project_id
        target = root.parent / "external-project"
        target.mkdir()
        for child in project.iterdir():
            child.unlink()
        project.rmdir()
        make_directory_link(project, target)

        result = cleanup.cleanup_orphan_projects(root, [project_id])

        self.assertIn(
            "UNSAFE_PROJECT_DIRECTORY", result["projects"][0]["reasons"]
        )

    def test_shared_alias_and_missing_directory_are_rejected(self) -> None:
        root, project_id, alias = self.fixture()
        catalog_path = root / "catalog.json"
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
        catalog["projects"]["other-project"] = {
            "name": "other-project",
            "remote": "",
            "path_aliases": [alias],
        }
        atomic_json(catalog_path, catalog)

        shared = cleanup.cleanup_orphan_projects(root, [project_id])

        self.assertIn(
            "SHARED_PATH_ALIAS", shared["projects"][0]["reasons"]
        )
        project = root / "projects" / project_id
        for child in project.iterdir():
            child.unlink()
        project.rmdir()
        missing = cleanup.cleanup_orphan_projects(root, [project_id])
        self.assertIn(
            "UNSAFE_PROJECT_DIRECTORY", missing["projects"][0]["reasons"]
        )

    def test_cli_reports_rollback_failure_structurally(self) -> None:
        output = io.StringIO()
        failure = RuntimeError("ROLLBACK_FAILED:source-conflict:demo")
        with mock.patch.object(
            cleanup, "cleanup_orphan_projects", side_effect=failure
        ):
            with redirect_stdout(output):
                code = cleanup.main(
                    ["--knowledge-root", "unused", "--project-id", "demo"]
                )

        receipt = json.loads(output.getvalue())
        self.assertEqual(code, 1)
        self.assertEqual(receipt["status"], "ROLLBACK_FAILED")


if __name__ == "__main__":
    unittest.main()

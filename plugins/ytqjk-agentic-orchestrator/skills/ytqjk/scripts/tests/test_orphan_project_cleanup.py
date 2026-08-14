from __future__ import annotations

import json
import os
import shutil
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import orphan_project_cleanup as cleanup  # noqa: E402
import orphan_cleanup_transaction as transaction  # noqa: E402
from rag_common import atomic_json  # noqa: E402


class OrphanProjectCleanupTest(unittest.TestCase):
    def fixture(self, count: int = 1) -> tuple[Path, list[str]]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name) / "knowledge"
        projects: dict[str, object] = {}
        ids = []
        for number in range(count):
            project_id = f"orphan-{number}"
            ids.append(project_id)
            project = root / "projects" / project_id
            project.mkdir(parents=True)
            (project / "data.txt").write_text(
                project_id, encoding="utf-8"
            )
            projects[project_id] = {
                "name": project_id,
                "remote": "",
                "path_aliases": [
                    str(Path(temporary.name) / f"missing-{number}")
                ],
            }
            identity = {
                "id": project_id,
                "root": projects[project_id]["path_aliases"][0],
                "remote": "",
            }
            atomic_json(
                project / "manifest.json",
                {
                    "schema_version": 2,
                    "identity": identity,
                    "indexed_identity": identity,
                },
            )
        atomic_json(
            root / "catalog.json",
            {"schema_version": 2, "projects": projects},
        )
        return root, ids

    def catalog(self, root: Path) -> dict[str, object]:
        return json.loads(
            (root / "catalog.json").read_text(encoding="utf-8")
        )

    def test_default_is_dry_run_without_mutation(self) -> None:
        root, ids = self.fixture()

        result = cleanup.cleanup_orphan_projects(root)

        self.assertEqual(result["status"], "DRY_RUN")
        self.assertEqual(result["eligible_count"], 1)
        self.assertTrue((root / "projects" / ids[0]).is_dir())
        self.assertIn(ids[0], self.catalog(root)["projects"])

    def test_apply_requires_yes_and_explicit_project_id(self) -> None:
        root, ids = self.fixture()
        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "PROJECT_ID_REQUIRED"
        ):
            cleanup.cleanup_orphan_projects(root, apply=True, yes=True)
        with self.assertRaisesRegex(cleanup.CleanupRejected, "YES_REQUIRED"):
            cleanup.cleanup_orphan_projects(root, ids, apply=True)
        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "MAINTENANCE_WINDOW_REQUIRED"
        ):
            cleanup.cleanup_orphan_projects(
                root, ids, apply=True, yes=True
            )

    def test_apply_moves_project_and_saves_sanitized_receipt(self) -> None:
        root, ids = self.fixture()
        original_catalog = (root / "catalog.json").read_bytes()

        result = cleanup.cleanup_orphan_projects(
            root,
            ids,
            apply=True,
            yes=True,
            maintenance_window=True,
            batch_id="batch-one",
        )

        batch = root / ".backups/orphan-projects/batch-one"
        self.assertFalse((root / "projects" / ids[0]).exists())
        self.assertTrue((batch / "projects" / ids[0]).is_dir())
        self.assertNotIn(ids[0], self.catalog(root)["projects"])
        self.assertTrue((batch / "catalog.before.json").is_file())
        self.assertEqual(
            (batch / "catalog.before.json").read_bytes(),
            original_catalog,
        )
        self.assertTrue((batch / "plan.json").is_file())
        persisted = json.loads(
            (batch / "receipt.json").read_text(encoding="utf-8")
        )
        self.assertEqual(persisted, result)
        self.assertNotIn("path_aliases", json.dumps(result))

    def test_failure_rolls_back_all_moves_and_catalog(self) -> None:
        root, ids = self.fixture(2)
        original = self.catalog(root)
        original_bytes = (root / "catalog.json").read_bytes()
        real_atomic = transaction.atomic_json

        def fail_receipt(path: Path, value: object) -> None:
            if path.name == "receipt.json":
                raise OSError("injected")
            real_atomic(path, value)

        with mock.patch.object(transaction, "atomic_json", fail_receipt):
            with self.assertRaisesRegex(OSError, "injected"):
                cleanup.cleanup_orphan_projects(
                    root,
                    ids,
                    apply=True,
                    yes=True,
                    maintenance_window=True,
                    batch_id="rollback",
                )

        self.assertEqual(self.catalog(root), original)
        self.assertEqual((root / "catalog.json").read_bytes(), original_bytes)
        for project_id in ids:
            self.assertTrue((root / "projects" / project_id).is_dir())
        self.assertFalse(
            (root / ".backups/orphan-projects/rollback").exists()
        )

    def test_missing_backup_fails_rollback_and_preserves_batch(self) -> None:
        root, ids = self.fixture()
        batch = root / ".backups/orphan-projects/missing-backup"
        backup = batch / "projects" / ids[0]
        real_atomic = transaction.atomic_json

        def remove_backup(path: Path, value: object) -> None:
            if path.name == "receipt.json":
                shutil.rmtree(backup)
                raise OSError("receipt failure")
            real_atomic(path, value)

        with mock.patch.object(transaction, "atomic_json", remove_backup):
            with self.assertRaisesRegex(
                RuntimeError, "ROLLBACK_FAILED:backup-missing"
            ):
                cleanup.cleanup_orphan_projects(
                    root, ids, apply=True, yes=True,
                    maintenance_window=True, batch_id="missing-backup",
                )

        self.assertTrue(batch.is_dir())
        self.assertFalse((root / "projects" / ids[0]).exists())

    def test_anchor_blocks_cleanup(self) -> None:
        root, ids = self.fixture()
        anchor = root / "sessions" / "opaque" / "anchor.json"
        atomic_json(anchor, {"schema_version": 1, "project_id": ids[0]})

        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "PROJECT_NOT_ELIGIBLE"
        ):
            cleanup.cleanup_orphan_projects(
                root,
                ids,
                apply=True,
                yes=True,
                maintenance_window=True,
            )

    def test_existing_alias_blocks_cleanup(self) -> None:
        root, ids = self.fixture()
        alias = root.parent / "existing"
        alias.mkdir()
        catalog = self.catalog(root)
        catalog["projects"][ids[0]]["path_aliases"] = [str(alias)]
        atomic_json(root / "catalog.json", catalog)

        result = cleanup.cleanup_orphan_projects(root, ids)

        self.assertIn(
            "PATH_ALIAS_EXISTS", result["projects"][0]["reasons"]
        )

    def test_remote_blocks_cleanup(self) -> None:
        for remote in ("https://example.test/repo", "   "):
            with self.subTest(remote=remote):
                root, ids = self.fixture()
                catalog = self.catalog(root)
                catalog["projects"][ids[0]]["remote"] = remote
                atomic_json(root / "catalog.json", catalog)
                result = cleanup.cleanup_orphan_projects(root, ids)
                self.assertIn(
                    "REMOTE_PRESENT", result["projects"][0]["reasons"]
                )

    def test_unknown_duplicate_and_escape_ids_are_rejected(self) -> None:
        root, ids = self.fixture()
        cases = [
            (["unknown"], "UNKNOWN_PROJECT_ID"),
            ([ids[0], ids[0]], "DUPLICATE_PROJECT_ID"),
            (["../escape"], "INVALID_PROJECT_ID"),
        ]
        for values, reason in cases:
            with self.subTest(reason=reason):
                with self.assertRaisesRegex(cleanup.CleanupRejected, reason):
                    cleanup.cleanup_orphan_projects(root, values)

        catalog = self.catalog(root)
        catalog["projects"]["../catalog-escape"] = catalog["projects"].pop(
            ids[0]
        )
        atomic_json(root / "catalog.json", catalog)
        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "INVALID_PROJECT_ID"
        ):
            cleanup.cleanup_orphan_projects(root)

    def test_batch_apply_is_all_or_nothing(self) -> None:
        root, ids = self.fixture(3)

        result = cleanup.cleanup_orphan_projects(
            root,
            ids,
            apply=True,
            yes=True,
            maintenance_window=True,
            batch_id="many",
        )

        self.assertEqual(result["removed_count"], 3)
        self.assertEqual(self.catalog(root)["projects"], {})
        for project_id in ids:
            self.assertTrue(
                (root / ".backups/orphan-projects/many/projects" /
                 project_id).is_dir()
            )

    def test_second_move_failure_restores_first_move(self) -> None:
        root, ids = self.fixture(2)
        original = (root / "catalog.json").read_bytes()
        real_replace = transaction.os.replace

        def fail_second(source: object, target: object) -> None:
            source_path = Path(source)
            if source_path.parent == root / "projects":
                if source_path.name == ids[1]:
                    raise OSError("second move")
            real_replace(source, target)

        with mock.patch.object(transaction.os, "replace", fail_second):
            with self.assertRaisesRegex(OSError, "second move"):
                cleanup.cleanup_orphan_projects(
                    root,
                    ids,
                    apply=True,
                    yes=True,
                    maintenance_window=True,
                    batch_id="move-failure",
                )

        self.assertEqual((root / "catalog.json").read_bytes(), original)
        self.assertTrue(all((root / "projects" / value).is_dir()
                            for value in ids))

    def test_catalog_write_failure_restores_all_moves(self) -> None:
        root, ids = self.fixture(2)
        original = (root / "catalog.json").read_bytes()
        real_atomic = transaction.atomic_json

        def fail_catalog(path: Path, value: object) -> None:
            if path == root / "catalog.json":
                raise OSError("catalog write")
            real_atomic(path, value)

        with mock.patch.object(transaction, "atomic_json", fail_catalog):
            with self.assertRaisesRegex(OSError, "catalog write"):
                cleanup.cleanup_orphan_projects(
                    root,
                    ids,
                    apply=True,
                    yes=True,
                    maintenance_window=True,
                    batch_id="catalog-failure",
                )

        self.assertEqual((root / "catalog.json").read_bytes(), original)
        self.assertTrue(all((root / "projects" / value).is_dir()
                            for value in ids))

    def test_project_lock_precedes_catalog_lock(self) -> None:
        root, ids = self.fixture()
        original_lock = cleanup.exclusive_file_lock

        with mock.patch.object(
            cleanup,
            "exclusive_file_lock",
            wraps=original_lock,
        ) as tracked:
            cleanup.cleanup_orphan_projects(root, ids)

        paths = [call.args[0] for call in tracked.call_args_list]
        self.assertEqual(paths[0].name, f"project-{ids[0]}.lock")
        self.assertEqual(paths[1].name, "maintenance.lock")
        self.assertEqual(paths[2], (root / "catalog.json").with_suffix(".lock"))

    def test_existing_backup_target_is_rejected(self) -> None:
        root, ids = self.fixture()
        target = root / ".backups/orphan-projects/reused"
        target.mkdir(parents=True)

        with self.assertRaisesRegex(
            cleanup.CleanupRejected, "BACKUP_TARGET_EXISTS"
        ):
            cleanup.cleanup_orphan_projects(
                root,
                ids,
                apply=True,
                yes=True,
                maintenance_window=True,
                batch_id="reused",
            )

        self.assertTrue((root / "projects" / ids[0]).is_dir())


if __name__ == "__main__":
    unittest.main()

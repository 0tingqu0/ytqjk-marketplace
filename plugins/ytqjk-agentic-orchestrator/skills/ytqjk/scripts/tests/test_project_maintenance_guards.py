from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import project_tracking  # noqa: E402
import session_memory  # noqa: E402
from file_lock import exclusive_file_lock  # noqa: E402
from orphan_cleanup_validation import (  # noqa: E402
    CleanupRejected,
    anchor_projects,
)
from rag_common import atomic_json, ensure_layout  # noqa: E402
from rag_locks import maintenance_lock, project_id_lock  # noqa: E402


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


class ProjectMaintenanceGuardTest(unittest.TestCase):
    def fixture(self) -> tuple[Path, Path, dict[str, str]]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        base = Path(temporary.name)
        source = base / "source"
        source.mkdir()
        knowledge = base / "knowledge"
        identity = project_tracking.identify_project(source)
        project_tracking.track_project(knowledge, source, identity)
        return knowledge, source, identity

    def remove_project(
        self,
        knowledge: Path,
        source: Path,
        project_id: str,
    ) -> None:
        shutil.rmtree(source)
        shutil.rmtree(knowledge / "projects" / project_id)
        catalog_path = knowledge / "catalog.json"
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
        del catalog["projects"][project_id]
        atomic_json(catalog_path, catalog)

    def test_waiting_tracker_cannot_recreate_removed_project(self) -> None:
        knowledge, source, identity = self.fixture()
        started = threading.Event()
        failures: list[BaseException] = []

        def track() -> None:
            started.set()
            try:
                project_tracking.track_project(
                    knowledge,
                    source,
                    identity,
                )
            except BaseException as exc:
                failures.append(exc)

        lock = project_id_lock(knowledge, identity["id"])
        with exclusive_file_lock(lock):
            thread = threading.Thread(target=track)
            thread.start()
            self.assertTrue(started.wait(1))
            thread.join(0.1)
            self.assertTrue(thread.is_alive())
            self.remove_project(knowledge, source, identity["id"])

        thread.join(2)
        self.assertEqual(str(failures[0]), "PROJECT_SOURCE_MISSING")
        self.assertFalse(
            (knowledge / "projects" / identity["id"]).exists()
        )
        catalog = json.loads(
            (knowledge / "catalog.json").read_text(encoding="utf-8")
        )
        self.assertNotIn(identity["id"], catalog["projects"])

    def test_waiting_anchor_rejects_removed_project(self) -> None:
        knowledge, source, identity = self.fixture()
        started = threading.Event()
        failures: list[BaseException] = []

        def anchor() -> None:
            started.set()
            try:
                session_memory.ensure_anchor(
                    knowledge,
                    "waiting-session",
                    identity["id"],
                )
            except BaseException as exc:
                failures.append(exc)

        with exclusive_file_lock(maintenance_lock(knowledge)):
            thread = threading.Thread(target=anchor)
            thread.start()
            self.assertTrue(started.wait(1))
            thread.join(0.1)
            self.assertTrue(thread.is_alive())
            self.remove_project(knowledge, source, identity["id"])

        thread.join(2)
        self.assertEqual(str(failures[0]), "PROJECT_REMOVED")
        self.assertFalse(
            session_memory.anchor_path(
                knowledge,
                "waiting-session",
            ).exists()
        )

    def test_layout_rejects_missing_project_source(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            knowledge = base / "knowledge"
            missing = base / "missing"

            with self.assertRaisesRegex(ValueError, "不存在"):
                ensure_layout(knowledge, missing)

            self.assertFalse((knowledge / "catalog.json").exists())
            self.assertEqual(list((knowledge / "projects").iterdir()), [])

    def test_anchor_requires_registered_project(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"

            with self.assertRaisesRegex(ValueError, "PROJECT_REMOVED"):
                session_memory.ensure_anchor(
                    knowledge,
                    "ghost-session",
                    "ghost-project",
                )

            self.assertFalse((knowledge / "sessions").exists())

    def test_sibling_junction_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            source = base / "source"
            source.mkdir()
            knowledge = base / "knowledge"
            projects = knowledge / "projects"
            sibling = projects / "sibling"
            sibling.mkdir(parents=True)
            identity = project_tracking.identify_project(source)
            linked = projects / identity["id"]
            make_directory_link(linked, sibling)

            with self.assertRaisesRegex(
                ValueError,
                "UNSAFE_PROJECT_DIRECTORY",
            ):
                project_tracking.track_project(
                    knowledge,
                    source,
                    identity,
                )

            atomic_json(
                knowledge / "catalog.json",
                {
                    "schema_version": 2,
                    "projects": {identity["id"]: {}},
                },
            )
            with self.assertRaisesRegex(ValueError, "PROJECT_REMOVED"):
                project_tracking.require_tracked_project(
                    knowledge,
                    identity["id"],
                )

    def test_cleanup_rejects_invalid_and_unknown_anchors(self) -> None:
        cases = (
            ({}, "INVALID_ANCHOR"),
            (
                {"schema_version": 1, "project_id": "ghost-project"},
                "ANCHOR_CATALOG_MISMATCH",
            ),
        )
        for anchor, reason in cases:
            with self.subTest(reason=reason):
                with tempfile.TemporaryDirectory() as temporary:
                    root = Path(temporary) / "knowledge"
                    atomic_json(
                        root / "sessions/opaque/anchor.json",
                        anchor,
                    )
                    with self.assertRaisesRegex(CleanupRejected, reason):
                        anchor_projects(root, {"known-project": {}})


if __name__ == "__main__":
    unittest.main()

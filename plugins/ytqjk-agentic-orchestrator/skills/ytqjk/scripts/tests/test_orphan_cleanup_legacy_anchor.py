from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import orphan_project_cleanup as cleanup  # noqa: E402
from rag_common import SCHEMA_VERSION, atomic_json  # noqa: E402


class OrphanCleanupLegacyAnchorTest(unittest.TestCase):
    def fixture(self) -> tuple[Path, str]:
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        root = Path(temporary.name) / "knowledge"
        project_id = "orphan-legacy-anchor"
        alias = str(Path(temporary.name) / "missing-project")
        project = root / "projects" / project_id
        project.mkdir(parents=True)
        identity = {
            "id": project_id,
            "root": alias,
            "remote": "",
        }
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
        return root, project_id

    def write_anchor(self, root: Path, value: object) -> Path:
        path = root / "sessions" / "legacy-global" / "anchor.json"
        atomic_json(path, value)
        return path

    def test_archived_global_anchor_is_preserved_during_cleanup(self) -> None:
        root, project_id = self.fixture()
        anchor = self.write_anchor(
            root,
            {
                "schema_version": 1,
                "project_id": "global",
                "archived_at": "2026-08-12T14:20:04+08:00",
                "memory": "preserve this memory",
            },
        )
        original_anchor = anchor.read_bytes()

        result = cleanup.cleanup_orphan_projects(
            root,
            [project_id],
            apply=True,
            yes=True,
            maintenance_window=True,
            batch_id="legacy-global",
        )

        self.assertEqual(result["removed_count"], 1)
        self.assertEqual(anchor.read_bytes(), original_anchor)

    def test_untrusted_non_project_anchors_remain_blocked(self) -> None:
        cases = (
            ({"schema_version": 1, "project_id": "global"},
             "ANCHOR_CATALOG_MISMATCH"),
            ({"schema_version": 1, "project_id": "global",
              "archived_at": ""}, "ANCHOR_CATALOG_MISMATCH"),
            ({"schema_version": 1, "project_id": "global",
              "archived_at": True}, "ANCHOR_CATALOG_MISMATCH"),
            ({"schema_version": 1, "project_id": "unknown",
              "archived_at": "2026-08-12T14:20:04+08:00"},
             "ANCHOR_CATALOG_MISMATCH"),
            ({"schema_version": 2, "project_id": "global",
              "archived_at": "2026-08-12T14:20:04+08:00"},
             "INVALID_ANCHOR"),
        )
        for anchor, reason in cases:
            with self.subTest(anchor=json.dumps(anchor, sort_keys=True)):
                root, project_id = self.fixture()
                self.write_anchor(root, anchor)

                with self.assertRaisesRegex(
                    cleanup.CleanupRejected,
                    reason,
                ):
                    cleanup.cleanup_orphan_projects(root, [project_id])


if __name__ == "__main__":
    unittest.main()

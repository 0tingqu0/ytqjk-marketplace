from __future__ import annotations

import importlib.util
import sys
import tempfile
import unittest
from datetime import datetime, timedelta, timezone
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
SPEC = importlib.util.spec_from_file_location("session_memory", SCRIPTS / "session_memory.py")
assert SPEC and SPEC.loader
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)
QUERY_SPEC = importlib.util.spec_from_file_location("session_query", SCRIPTS / "session_query.py")
assert QUERY_SPEC and QUERY_SPEC.loader
QUERY_MODULE = importlib.util.module_from_spec(QUERY_SPEC)
QUERY_SPEC.loader.exec_module(QUERY_MODULE)
ARCHIVE_SPEC = importlib.util.spec_from_file_location("archive_sync", SCRIPTS / "archive_sync.py")
assert ARCHIVE_SPEC and ARCHIVE_SPEC.loader
ARCHIVE_MODULE = importlib.util.module_from_spec(ARCHIVE_SPEC)
ARCHIVE_SPEC.loader.exec_module(ARCHIVE_MODULE)


class SessionMemoryTest(unittest.TestCase):
    def test_anchor_checkpoint_restore_and_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.write_anchor(root, "thread-1", "project-a")
            MODULE.checkpoint(root, "thread-1", "project-a", "结论：测试通过；证据：commit abc。")
            restored = MODULE.restore(root, "thread-1")
            archived = MODULE.archive(root, "thread-1")

            self.assertIn("测试通过", restored["memory"])
            self.assertTrue(archived["archived_at"])
            self.assertEqual(len(list((root / "personal-experience/candidates").glob("*.md"))), 1)

    def test_sweep_archives_only_inactive_anchored_memory(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            anchor = MODULE.checkpoint(root, "thread-2", "project-a", "来源：测试记录。" * 20)
            anchor["last_activity_at"] = (datetime.now(timezone.utc) - timedelta(days=31)).isoformat()
            MODULE.anchor_path(root, "thread-2").write_text(__import__("json").dumps(anchor), encoding="utf-8")

            archived = MODULE.sweep(root, 30)

            self.assertEqual(archived, [MODULE.session_key("thread-2")])

    def test_knowledge_access_reopens_anchor_without_duplicate_export(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.checkpoint(root, "thread-4", "project-a", "来源：测试记录。" * 20)
            MODULE.archive(root, "thread-4")
            MODULE.write_anchor(root, "thread-4", "project-a")
            MODULE.archive(root, "thread-4")

            self.assertEqual(len(list((root / "personal-experience/candidates").glob("*.md"))), 1)

    def test_repeated_anchor_uses_one_stable_anchor_path(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, created_first = MODULE.ensure_anchor(root, "thread-5", "project-a")
            _, created_second = MODULE.ensure_anchor(root, "thread-5", "project-a")

            self.assertTrue(created_first)
            self.assertFalse(created_second)
            self.assertEqual(len(list((root / "sessions").glob("*/anchor.json"))), 1)

    def test_query_anchor_does_not_run_full_git_identity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            project = Path(temporary) / "project"
            project.mkdir()

            result = QUERY_MODULE.anchor_query(root, project, "thread-6")

            self.assertTrue(result["created"])
            self.assertEqual(result["session_key"], MODULE.session_key("thread-6"))

    def test_memory_rejects_secret(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaises(ValueError):
                MODULE.checkpoint(Path(temporary), "thread-3", "project-a", "token: A1B2C3D4E5F6G7")

    def test_archived_log_exports_memory_for_existing_anchor_only(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "knowledge"
            codex_home = Path(temporary) / "codex"
            archive_dir = codex_home / "archived_sessions"
            archive_dir.mkdir(parents=True)
            session_id = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
            MODULE.write_anchor(root, session_id, "project-a")
            log = archive_dir / f"rollout-2026-01-01T00-00-00-{session_id}.jsonl"
            log.write_text(
                '{"payload":{"type":"agent_message","phase":"final_answer","message":"结论：验证通过。"}}\n',
                encoding="utf-8",
            )

            synced = ARCHIVE_MODULE.sync_archived_sessions(root, codex_home)
            anchor = MODULE.read_anchor(root, session_id)

            self.assertEqual(synced, [MODULE.session_key(session_id)])
            self.assertTrue(anchor["archived_at"])
            self.assertIn("验证通过", anchor["memory"])
            self.assertEqual(len(list((root / "personal-experience/candidates").glob("*.md"))), 1)

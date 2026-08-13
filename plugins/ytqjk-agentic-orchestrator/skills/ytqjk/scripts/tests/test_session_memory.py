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
    def test_anchor_checkpoint_restore_and_confirmed_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.write_anchor(root, "thread-1", "project-a")
            MODULE.checkpoint(root, "thread-1", "project-a", "结论：测试通过；证据：commit abc。")
            restored = MODULE.restore(root, "thread-1")
            MODULE.prepare_archive(root, "thread-1")
            archived = MODULE.finalize_archive(root, "thread-1")

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

    def test_knowledge_access_cannot_reopen_archived_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.checkpoint(root, "thread-4", "project-a", "来源：测试记录。" * 20)
            MODULE.prepare_archive(root, "thread-4")
            MODULE.finalize_archive(root, "thread-4")

            with self.assertRaisesRegex(ValueError, "已归档"):
                MODULE.write_anchor(root, "thread-4", "project-a")
            with self.assertRaisesRegex(ValueError, "已归档"):
                MODULE.restore(root, "thread-4")

            self.assertEqual(len(list((root / "personal-experience/candidates").glob("*.md"))), 1)

    def test_corrupt_anchor_is_not_overwritten(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = MODULE.anchor_path(root, "thread-corrupt")
            path.parent.mkdir(parents=True)
            path.write_text("{broken", encoding="utf-8")

            with self.assertRaisesRegex(ValueError, "已损坏"):
                MODULE.ensure_anchor(root, "thread-corrupt", "project-b")

            self.assertEqual(path.read_text(encoding="utf-8"), "{broken")

    def test_repeated_anchor_uses_one_stable_anchor_path(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            _, created_first = MODULE.ensure_anchor(root, "thread-5", "project-a")
            _, created_second = MODULE.ensure_anchor(root, "thread-5", "project-a")

            self.assertTrue(created_first)
            self.assertFalse(created_second)
            self.assertEqual(len(list((root / "sessions").glob("*/anchor.json"))), 1)

    def test_inspect_distinguishes_legacy_active_and_archived_sessions(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)

            absent = MODULE.inspect_anchor(root, "legacy-thread", "project-a")
            MODULE.write_anchor(root, "legacy-thread", "project-a")
            active = MODULE.inspect_anchor(root, "legacy-thread", "project-a")
            MODULE.checkpoint(
                root, "legacy-thread", "project-a", "来源：流程复用测试。" * 20
            )
            MODULE.prepare_archive(root, "legacy-thread")
            MODULE.finalize_archive(root, "legacy-thread")
            archived = MODULE.inspect_anchor(root, "legacy-thread", "project-a")

            self.assertEqual(absent["state"], "ABSENT")
            self.assertEqual(active["state"], "ACTIVE")
            self.assertEqual(archived["state"], "ARCHIVED")

    def test_unconfirmed_legacy_archive_entrypoint_is_absent(self) -> None:
        self.assertFalse(hasattr(MODULE, "archive"))

    def test_inspect_rejects_cross_project_session(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.write_anchor(root, "thread-inspect", "project-a")

            with self.assertRaisesRegex(ValueError, "禁止作为当前项目会话复用"):
                MODULE.inspect_anchor(root, "thread-inspect", "project-b")

    def test_failed_host_archive_can_retry_from_prepared_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.checkpoint(
                root, "thread-retry", "project-a", "来源：首次归档尝试。" * 20
            )

            prepared = MODULE.prepare_archive(root, "thread-retry")
            inspected = MODULE.inspect_anchor(root, "thread-retry", "project-a")
            self.assertTrue(prepared["archive_prepared_at"])
            self.assertEqual(inspected["state"], "ARCHIVE_PREPARED")
            with self.assertRaisesRegex(ValueError, "等待归档完成"):
                MODULE.restore(root, "thread-retry")
            with self.assertRaisesRegex(ValueError, "等待归档完成"):
                MODULE.write_anchor(root, "thread-retry", "project-a")
            retried = MODULE.checkpoint(
                root, "thread-retry", "project-a", "来源：归档失败后重试。" * 20
            )
            self.assertIsNone(retried["archive_prepared_at"])
            MODULE.prepare_archive(root, "thread-retry")
            archived = MODULE.finalize_archive(root, "thread-retry")
            self.assertTrue(archived["archived_at"])

    def test_finalize_requires_prepared_archive(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.checkpoint(
                root, "thread-unprepared", "project-a", "来源：待归档摘要。" * 20
            )

            with self.assertRaisesRegex(ValueError, "尚未进入待归档状态"):
                MODULE.finalize_archive(root, "thread-unprepared")

    def test_anchor_rejects_cross_project_access(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            MODULE.ensure_anchor(root, "thread-bound", "project-a")

            with self.assertRaisesRegex(ValueError, "禁止访问其他项目子库"):
                MODULE.ensure_anchor(root, "thread-bound", "project-b")

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

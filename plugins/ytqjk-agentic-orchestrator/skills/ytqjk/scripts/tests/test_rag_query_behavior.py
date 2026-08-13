from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import global_session_query  # noqa: E402
import rag_query  # noqa: E402
from project_tracking import identify_project  # noqa: E402
from rag_test_support import make_repo, query, run_rag  # noqa: E402


class RagQueryBehaviorTest(unittest.TestCase):
    def test_dirty_source_is_stale_and_init_preserves_indexed_identity(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo", "索引版本一知识。")
            knowledge = base / "knowledge"
            indexed = run_rag(
                knowledge, "index", repo, "--vector-mode", "off"
            )
            manifest_path = Path(str(indexed["project_dir"])) / "manifest.json"
            before = json.loads(manifest_path.read_text(encoding="utf-8"))
            clean = query(knowledge, repo, "stale-session", "索引版本一")

            (repo / "guide.md").write_text(
                "索引版本一知识。\n尚未重新索引。", encoding="utf-8"
            )
            run_rag(knowledge, "init", repo)
            after_init = json.loads(manifest_path.read_text(encoding="utf-8"))
            dirty = query(knowledge, repo, "stale-session", "索引版本一")

            self.assertFalse(clean["stale"])
            self.assertEqual(
                after_init["indexed_identity"], before["indexed_identity"]
            )
            self.assertTrue(dirty["stale"])

    def test_only_session_bound_query_entrypoint_remains(self) -> None:
        self.assertFalse(hasattr(rag_query, "command_query"))

    def test_project_hit_does_not_open_invalid_global_index(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo", "LOCAL_ONLY_MARKER_4217")
            knowledge = base / "knowledge"
            run_rag(knowledge, "index", repo, "--vector-mode", "off")
            global_cache = knowledge / "global-cache"
            global_cache.mkdir()
            (global_cache / "manifest.json").write_text(
                '{"schema_version":1}', encoding="utf-8"
            )

            result = query(
                knowledge, repo, "local-before-global", "LOCAL_ONLY_MARKER_4217"
            )

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")

    def test_formal_query_builds_project_vector_only_after_lexical_miss(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo", "向量语义来源内容。")
            knowledge = base / "knowledge"
            built = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "on"
            )
            project_dir = Path(str(built["project_dir"]))
            vector_row = {
                "id": "semantic-1",
                "path": "guide.md",
                "line_start": 1,
                "line_end": 1,
                "content": "向量语义来源内容。",
            }

            with mock.patch.object(
                global_session_query,
                "build_vector_cache",
                return_value={"enabled": True, "status": "READY"},
            ) as build, mock.patch.object(
                global_session_query,
                "query_vector_cache",
                return_value=[vector_row],
            ) as vector_query:
                result = global_session_query.query_global(
                    knowledge,
                    repo,
                    "同义表达未命中词法",
                    "semantic-session",
                    identify_project(repo)["id"],
                    5,
                )

            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")
            build.assert_called_once_with(project_dir, knowledge, mock.ANY)
            vector_query.assert_called_once()

    def test_bootstrap_rebuilds_when_index_config_changes(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            first = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "off"
            )
            config_path = knowledge / "config.json"
            config = json.loads(config_path.read_text(encoding="utf-8"))
            config["index"]["chunk_chars"] = 600
            config_path.write_text(json.dumps(config), encoding="utf-8")

            second = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "off"
            )

            self.assertEqual(first["project"]["state"], "REBUILT")
            self.assertEqual(second["project"]["state"], "REBUILT")


if __name__ == "__main__":
    unittest.main()

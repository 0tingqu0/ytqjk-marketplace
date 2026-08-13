from __future__ import annotations

import json
import sqlite3
import sys
import tempfile
import unittest
from contextlib import closing
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import project_source  # noqa: E402
from rag_test_support import make_repo, query, run_rag  # noqa: E402
from project_prefetch import list_prefetch, update_prefetch  # noqa: E402
from project_tracking import identify_project  # noqa: E402


class RagOptimizationTest(unittest.TestCase):
    def test_project_lookup_does_not_diff_or_walk_source(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            ordinary = base / "ordinary"
            ordinary.mkdir()
            original = project_source.run_git

            with mock.patch(
                "project_source.run_git", wraps=original
            ) as run_git_mock, mock.patch("pathlib.Path.rglob") as rglob:
                identify_project(repo)
                identify_project(ordinary)

            commands = [call.args[1:] for call in run_git_mock.call_args_list]
            self.assertFalse(any("diff" in command for command in commands))
            rglob.assert_not_called()

    def test_bootstrap_reuses_clean_indexes_and_preserves_auto_mode(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo", "自动建库只需词法索引。")
            knowledge = base / "knowledge"
            approved = knowledge / "verified"
            approved.mkdir(parents=True)
            (approved / "fact.md").write_text("已验证总库知识。", encoding="utf-8")

            first = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "auto"
            )
            project_database = Path(str(first["project_dir"])) / "lexical.sqlite3"
            global_database = knowledge / "global-cache" / "lexical.sqlite3"
            project_mtime = project_database.stat().st_mtime_ns
            global_mtime = global_database.stat().st_mtime_ns

            second = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "auto"
            )

            project_manifest = json.loads(
                (Path(str(first["project_dir"])) / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
            global_manifest = json.loads(
                (knowledge / "global-cache" / "manifest.json").read_text(
                    encoding="utf-8"
                )
            )
            self.assertEqual(first["vector_mode"], "auto")
            self.assertEqual(project_manifest["vector_mode"], "auto")
            self.assertEqual(global_manifest["vector_mode"], "auto")
            self.assertEqual(second["project"]["state"], "REUSED")
            self.assertEqual(second["global"]["state"], "REUSED")
            self.assertEqual(project_database.stat().st_mtime_ns, project_mtime)
            self.assertEqual(global_database.stat().st_mtime_ns, global_mtime)

    def test_nested_knowledge_and_skipped_tree_are_not_indexed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            workspace = Path(temporary) / "workspace"
            workspace.mkdir()
            knowledge = workspace / ".knowledge"
            candidate = knowledge / "personal-experience" / "candidates"
            candidate.mkdir(parents=True)
            (candidate / "draft.md").write_text(
                "CANDIDATE_ONLY_MARKER_79421", encoding="utf-8"
            )
            dependency = workspace / "node_modules" / "package"
            dependency.mkdir(parents=True)
            (dependency / "index.js").write_text(
                "SKIPPED_TREE_MARKER_31859", encoding="utf-8"
            )
            (workspace / "notes.md").write_text(
                "普通目录安全知识。", encoding="utf-8"
            )

            built = run_rag(
                knowledge, "bootstrap", workspace, "--vector-mode", "off"
            )
            candidate_result = query(
                knowledge, workspace, "nested-session", "CANDIDATE_ONLY_MARKER_79421"
            )
            dependency_result = query(
                knowledge, workspace, "nested-session", "SKIPPED_TREE_MARKER_31859"
            )
            safe_result = query(
                knowledge, workspace, "nested-session", "普通目录安全知识"
            )

            self.assertEqual(built["project"]["stats"]["files"], 1)
            self.assertEqual(candidate_result["results"], [])
            self.assertEqual(dependency_result["results"], [])
            self.assertEqual(safe_result["status"], "PROJECT_CACHE_HIT")
            self.assertTrue(safe_result["stale"])

    def test_project_equal_to_knowledge_root_indexes_nothing(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            knowledge = Path(temporary) / "knowledge"
            knowledge.mkdir()
            (knowledge / "notes.md").write_text(
                "知识根自身不可成为项目来源。", encoding="utf-8"
            )

            built = run_rag(
                knowledge, "bootstrap", knowledge, "--vector-mode", "off"
            )

            self.assertEqual(built["project"]["stats"]["files"], 0)

    def test_global_generation_invalidates_demoted_prefetch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            approved = knowledge / "personal-experience" / "approved"
            candidates = knowledge / "personal-experience" / "candidates"
            approved.mkdir(parents=True)
            candidates.mkdir(parents=True)
            source = approved / "lesson.md"
            source.write_text("撤销批准后不得继续命中。", encoding="utf-8")
            run_rag(knowledge, "index-global", None, "--vector-mode", "off")

            first = query(knowledge, repo, "generation-session", "撤销批准")
            cached = query(knowledge, repo, "generation-session", "撤销批准")
            source.replace(candidates / source.name)
            run_rag(knowledge, "index-global", None, "--vector-mode", "off")
            after_demotion = query(
                knowledge, repo, "generation-session", "撤销批准"
            )

            self.assertEqual(first["status"], "GLOBAL_FALLBACK_HIT")
            self.assertEqual(cached["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(after_demotion["status"], "KNOWLEDGE_MISS")
            self.assertEqual(after_demotion["results"], [])

    def test_absent_global_index_invalidates_old_prefetch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            project = identify_project(repo)
            project_dir = knowledge / "projects" / project["id"]
            update_prefetch(
                project_dir,
                "旧缓存",
                [{
                    "path": "verified/old.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": "已不存在的旧缓存。",
                }],
                generation="OLD_GLOBAL_GENERATION",
            )

            result = query(knowledge, repo, "absent-session", "旧缓存")

            self.assertEqual(result["status"], "KNOWLEDGE_MISS")
            self.assertEqual(list_prefetch(project_dir), [])

    def test_prefetch_rejects_candidate_paths(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project_dir = Path(temporary) / "project"

            rows = update_prefetch(
                project_dir,
                "候选",
                [{
                    "path": "personal-experience/candidates/draft.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": "候选内容不可进入预取缓存。",
                }],
                generation="generation-one",
            )

            self.assertEqual(rows, [])

    def test_prefetch_purges_legacy_candidate_rows(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project_dir = Path(temporary) / "project"
            update_prefetch(
                project_dir,
                "批准",
                [{
                    "path": "verified/fact.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": "批准知识。",
                }],
            )
            database = project_dir / "cache" / "global-knowledge.sqlite3"
            with closing(sqlite3.connect(database)) as connection:
                connection.execute(
                    "INSERT INTO entries VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
                    (
                        "legacy-candidate",
                        "personal-experience/candidates/draft.md",
                        1,
                        1,
                        "旧候选缓存。",
                        "候选",
                        "now",
                        "now",
                        1,
                        16,
                    ),
                )
                connection.commit()

            rows = list_prefetch(project_dir)

            self.assertEqual([row["path"] for row in rows], ["verified/fact.md"])

if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))
RAG = SCRIPTS / "rag_cli.py"

import global_session_query  # noqa: E402
from project_tracking import identify_project  # noqa: E402
from rag_common import Chunk, build_lexical, lexical_query  # noqa: E402


def git(repo: Path, *args: str) -> None:
    subprocess.run(
        ["git", "-C", str(repo), *args], check=True, capture_output=True
    )


def make_repo(path: Path) -> Path:
    path.mkdir()
    git(path, "init")
    git(path, "config", "user.email", "test@example.com")
    git(path, "config", "user.name", "Test")
    (path / "root.md").write_text("ROOT_MARKER_117", encoding="utf-8")
    nested = path / "nested"
    nested.mkdir()
    (nested / "guide.md").write_text("NESTED_MARKER_229", encoding="utf-8")
    git(path, "add", ".")
    git(path, "commit", "-m", "seed")
    return path


def run_rag(
    knowledge: Path,
    command: str,
    project: Path | None = None,
    *args: str,
    expect_success: bool = True,
) -> dict[str, object]:
    invocation = [
        sys.executable,
        str(RAG),
        "--knowledge-root",
        str(knowledge),
        command,
    ]
    if project is not None:
        invocation.extend(["--project-root", str(project)])
    invocation.extend(args)
    result = subprocess.run(
        invocation, check=False, capture_output=True, encoding="utf-8"
    )
    payload = json.loads(result.stdout)
    if expect_success and result.returncode != 0:
        raise AssertionError(result.stderr or result.stdout)
    return payload


def query(
    knowledge: Path, project: Path, session: str, text: str
) -> dict[str, object]:
    return run_rag(
        knowledge,
        "query",
        project,
        "--session-id",
        session,
        "--expected-project-id",
        identify_project(project)["id"],
        text,
    )


class RagConsistencyTest(unittest.TestCase):
    def test_global_query_pages_past_invalid_lexical_hits(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            database = base / "lexical.sqlite3"
            chunks = [
                Chunk(
                    id=f"chunk-{index:02d}",
                    path=f"verified/fact-{index:02d}.md",
                    line_start=1,
                    line_end=1,
                    content="PAGED_VALIDATION_MARKER",
                    source_sha256=f"hash-{index:02d}",
                    indexed_at="2026-08-13T00:00:00+00:00",
                    head="GLOBAL",
                )
                for index in range(25)
            ]
            build_lexical(database, chunks)
            ordered = lexical_query(database, "PAGED_VALIDATION_MARKER", 25)
            valid_id = ordered[20]["id"]

            with mock.patch.object(
                global_session_query,
                "lexical_query",
                wraps=lexical_query,
            ) as paged_query:
                results = global_session_query._query_index(
                    base,
                    database,
                    base / "manifest.json",
                    {"vector_mode": "off", "stats": {"chunks": 25}},
                    base,
                    {"vector_mode": "off"},
                    "PAGED_VALIDATION_MARKER",
                    1,
                    "global-fallback",
                    validator=lambda row: row["id"] == valid_id,
                )

            self.assertEqual([row["id"] for row in results], [valid_id])
            self.assertEqual(paged_query.call_count, 2)

    def test_query_layers_only_current_approved_global_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            approved = knowledge / "personal-experience" / "approved"
            candidates = knowledge / "personal-experience" / "candidates"
            approved.mkdir(parents=True)
            candidates.mkdir(parents=True)
            (approved / "lesson.md").write_text(
                "复审通过后才能归档 worker。\n", encoding="utf-8"
            )
            (candidates / "draft.md").write_text(
                "候选区不可检索的特殊暗号。\n", encoding="utf-8"
            )
            (approved / "secrets.yaml").write_text(
                "client_secret: GLOBAL_FAKE_SECRET_938475\n", encoding="utf-8"
            )
            (approved / "token-guide.md").write_text(
                "Token is a label, not a credential value.\n", encoding="utf-8"
            )

            indexed = run_rag(
                knowledge, "index-global", None, "--vector-mode", "off"
            )
            self.assertEqual(indexed["stats"]["files"], 2)
            run_rag(knowledge, "index", repo, "--vector-mode", "off")
            found = query(knowledge, repo, "approval-session", "复审通过")
            self.assertEqual(found["status"], "GLOBAL_FALLBACK_HIT")
            self.assertEqual(found["results"][0]["scope"], "global-fallback")
            self.assertEqual(
                found["results"][0]["path"],
                "personal-experience/approved/lesson.md",
            )
            hidden = query(knowledge, repo, "approval-session", "特殊暗号")
            self.assertEqual(hidden["results"], [])
            global_secret = query(
                knowledge,
                repo,
                "approval-session",
                "GLOBAL_FAKE_SECRET_938475",
            )
            self.assertEqual(global_secret["results"], [])
            safe_mention = query(
                knowledge, repo, "approval-session", "Token is a label"
            )
            self.assertEqual(
                safe_mention["results"][0]["scope"], "global-fallback"
            )

            (approved / "lesson.md").write_text(
                "全局知识已经变化，必须重新索引。\n", encoding="utf-8"
            )
            stale = run_rag(
                knowledge,
                "query",
                repo,
                "--session-id",
                "approval-session",
                "--expected-project-id",
                identify_project(repo)["id"],
                "复审通过",
            )

            self.assertTrue(stale["ok"])
            self.assertEqual(stale["status"], "KNOWLEDGE_MISS")
            self.assertEqual(stale["results"], [])

    def test_demoted_approved_source_invalidates_matching_prefetch(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            approved = knowledge / "personal-experience" / "approved"
            candidates = knowledge / "personal-experience" / "candidates"
            approved.mkdir(parents=True)
            candidates.mkdir(parents=True)
            source = approved / "lesson.md"
            source.write_text("DEMOTION_MARKER_391", encoding="utf-8")
            run_rag(knowledge, "index-global", None, "--vector-mode", "off")
            first = query(knowledge, repo, "demotion-session", "DEMOTION_MARKER_391")
            source.replace(candidates / source.name)

            after_demotion = run_rag(
                knowledge,
                "query",
                repo,
                "--session-id",
                "demotion-session",
                "--expected-project-id",
                identify_project(repo)["id"],
                "DEMOTION_MARKER_391",
            )

            self.assertEqual(first["status"], "GLOBAL_FALLBACK_HIT")
            self.assertTrue(after_demotion["ok"])
            self.assertEqual(after_demotion["status"], "KNOWLEDGE_MISS")
            self.assertEqual(after_demotion["results"], [])

    def test_bootstrap_from_subdirectory_indexes_repository_root(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"

            built = run_rag(
                knowledge,
                "bootstrap",
                repo / "nested",
                "--vector-mode",
                "off",
            )
            result = query(knowledge, repo, "root-scope-session", "ROOT_MARKER_117")

            self.assertEqual(built["project"]["stats"]["files"], 2)
            self.assertEqual(result["status"], "PROJECT_CACHE_HIT")

    def test_project_id_mismatch_is_rejected_before_anchor(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"

            result = run_rag(
                knowledge,
                "query",
                repo,
                "--session-id",
                "mismatch-session",
                "--expected-project-id",
                "wrong-project",
                "anything",
                expect_success=False,
            )

            self.assertFalse(result["ok"])
            self.assertIn("项目标识与工作目录不匹配", str(result["error"]))
            self.assertEqual(list((knowledge / "sessions").glob("*/anchor.json")), [])

    def test_vector_results_are_bounded_by_requested_limit(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            built = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "on"
            )
            rows = [
                {
                    "id": f"semantic-{index}",
                    "path": "root.md",
                    "line_start": 1,
                    "line_end": 1,
                    "content": f"semantic {index}",
                }
                for index in range(20)
            ]

            with mock.patch.object(
                global_session_query,
                "build_vector_cache",
                return_value={"enabled": True, "status": "READY"},
            ), mock.patch.object(
                global_session_query, "query_vector_cache", return_value=rows
            ):
                result = global_session_query.query_global(
                    knowledge,
                    repo,
                    "semantic miss",
                    "limit-session",
                    identify_project(repo)["id"],
                    3,
                )

            self.assertEqual(result["result_count"], 3)
            self.assertEqual(
                Path(str(built["project_dir"])).name,
                identify_project(repo)["id"],
            )

    def test_global_config_change_requires_reindex(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_repo(base / "repo")
            knowledge = base / "knowledge"
            approved = knowledge / "verified"
            approved.mkdir(parents=True)
            (approved / "fact.md").write_text("CONFIG_MARKER_812", encoding="utf-8")
            run_rag(knowledge, "index-global", None, "--vector-mode", "off")
            config_path = knowledge / "config.json"
            config = json.loads(config_path.read_text(encoding="utf-8"))
            config["index"]["chunk_chars"] = 600
            config_path.write_text(json.dumps(config), encoding="utf-8")

            result = run_rag(
                knowledge,
                "query",
                repo,
                "--session-id",
                "config-session",
                "--expected-project-id",
                identify_project(repo)["id"],
                "CONFIG_MARKER_812",
                expect_success=False,
            )

            self.assertFalse(result["ok"])
            self.assertIn("索引配置已变化", str(result["error"]))


if __name__ == "__main__":
    unittest.main()

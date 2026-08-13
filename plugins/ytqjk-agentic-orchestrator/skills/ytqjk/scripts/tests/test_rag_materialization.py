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
import project_prefetch  # noqa: E402
from project_prefetch import prefetch_stats  # noqa: E402
from project_tracking import identify_project  # noqa: E402


def git(repo: Path, *args: str) -> None:
    subprocess.run(
        ["git", "-C", str(repo), *args], check=True, capture_output=True
    )


def run_rag(
    knowledge: Path, command: str, project: Path, *args: str
) -> dict[str, object]:
    invocation = [
        sys.executable,
        str(RAG),
        "--knowledge-root",
        str(knowledge),
        command,
        "--project-root",
        str(project),
        *args,
    ]
    result = subprocess.run(
        invocation, check=False, capture_output=True, encoding="utf-8"
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr or result.stdout)
    return json.loads(result.stdout)


def make_sparse_repo(path: Path) -> Path:
    path.mkdir()
    git(path, "init")
    git(path, "config", "user.email", "test@example.com")
    git(path, "config", "user.name", "Test")
    for directory, marker in (("visible", "VISIBLE_42"), ("hidden", "HIDDEN_73")):
        target = path / directory
        target.mkdir()
        (target / "guide.md").write_text(marker, encoding="utf-8")
    git(path, "add", ".")
    git(path, "commit", "-m", "seed")
    git(path, "sparse-checkout", "init", "--cone")
    git(path, "sparse-checkout", "set", "visible")
    return path


class RagMaterializationTest(unittest.TestCase):
    def test_sparse_checkout_change_rebuilds_same_head(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_sparse_repo(base / "repo")
            knowledge = base / "knowledge"

            first = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "off"
            )
            git(repo, "sparse-checkout", "set", "visible", "hidden")
            second = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "off"
            )
            queried = run_rag(
                knowledge,
                "query",
                repo,
                "--session-id",
                "sparse-session",
                "--expected-project-id",
                identify_project(repo)["id"],
                "HIDDEN_73",
            )

            self.assertEqual(first["project"]["stats"]["files"], 1)
            self.assertEqual(second["project"]["state"], "REBUILT")
            self.assertEqual(second["project"]["stats"]["files"], 2)
            self.assertEqual(queried["status"], "PROJECT_CACHE_HIT")

    def test_stale_project_does_not_build_vector_cache(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_sparse_repo(base / "repo")
            knowledge = base / "knowledge"
            run_rag(knowledge, "bootstrap", repo, "--vector-mode", "on")
            (repo / "visible" / "guide.md").write_text(
                "DIRTY_SOURCE", encoding="utf-8"
            )

            with mock.patch.object(
                global_session_query, "build_vector_cache"
            ) as build:
                result = global_session_query.query_global(
                    knowledge,
                    repo,
                    "semantic miss",
                    "dirty-vector-session",
                    identify_project(repo)["id"],
                    5,
                )

            self.assertTrue(result["stale"])
            self.assertEqual(result["status"], "KNOWLEDGE_MISS")
            build.assert_not_called()

    def test_lazy_vector_capacity_eviction_skips_deleted_vector(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = make_sparse_repo(base / "repo")
            knowledge = base / "knowledge"
            built = run_rag(
                knowledge, "bootstrap", repo, "--vector-mode", "on"
            )
            project_dir = Path(str(built["project_dir"]))
            baseline = project_prefetch._directory_size(project_dir)

            def build_vector(*_args: object) -> dict[str, object]:
                vectors = project_dir / "vectors"
                vectors.mkdir(exist_ok=True)
                (vectors / "large.bin").write_bytes(b"v" * 65_536)
                return {"enabled": True, "status": "READY", "chunks": 1}

            def enforce(path: Path) -> list[str]:
                return project_prefetch.enforce_project_capacity(
                    path, max_bytes=baseline + 4096
                )

            with mock.patch.object(
                global_session_query,
                "build_vector_cache",
                side_effect=build_vector,
            ), mock.patch.object(
                global_session_query,
                "enforce_project_capacity",
                side_effect=enforce,
            ), mock.patch.object(
                global_session_query, "query_vector_cache"
            ) as vector_query:
                result = global_session_query.query_global(
                    knowledge,
                    repo,
                    "semantic miss",
                    "capacity-vector-session",
                    identify_project(repo)["id"],
                    5,
                )

            manifest = json.loads(
                (project_dir / "manifest.json").read_text(encoding="utf-8")
            )
            vector_query.assert_not_called()
            self.assertFalse((project_dir / "vectors").exists())
            self.assertEqual(manifest["vector"]["status"], "EVICTED_CAPACITY")
            self.assertLessEqual(
                manifest["capacity"]["used_bytes"], baseline + 4096
            )
            self.assertFalse(result["cache"]["capacity_exceeded"])

    def test_prefetch_stats_uses_capacity_snapshot_for_vectors(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            project_dir = Path(temporary) / "project"
            vectors = project_dir / "vectors"
            vectors.mkdir(parents=True)
            (vectors / "index.bin").write_bytes(b"vector-data")
            (project_dir / "manifest.json").write_text(
                json.dumps({"capacity": {"used_bytes": 123_456}}),
                encoding="utf-8",
            )

            with mock.patch(
                "project_prefetch._directory_size",
                side_effect=AssertionError("stats recursively scanned project"),
            ):
                stats = prefetch_stats(project_dir)

            self.assertEqual(stats["project_used_bytes"], 123_456)


if __name__ == "__main__":
    unittest.main()

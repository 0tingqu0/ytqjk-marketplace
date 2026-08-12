from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
RAG = SCRIPTS / "rag_cli.py"

def git(repo: Path, *args: str) -> None:
    subprocess.run(["git", "-C", str(repo), *args], check=True, capture_output=True)


def run_rag(knowledge: Path, command: str, project: Path, *args: str) -> dict:
    extra = ["--session-id", "test-session"] if command == "query" else []
    result = subprocess.run(
        [
            sys.executable,
            str(RAG),
            "--knowledge-root",
            str(knowledge),
            command,
            "--project-root",
            str(project),
            *extra,
            *args,
        ],
        check=False,
        capture_output=True,
        encoding="utf-8",
    )
    if result.returncode != 0:
        raise AssertionError(result.stderr or result.stdout)
    return json.loads(result.stdout)

def index_global(knowledge: Path) -> dict:
    result = subprocess.run(
        [
            sys.executable,
            str(RAG),
            "--knowledge-root",
            str(knowledge),
            "index-global",
            "--vector-mode",
            "off",
        ],
        check=True,
        capture_output=True,
        encoding="utf-8",
    )
    return json.loads(result.stdout)

class RagCliTest(unittest.TestCase):
    def test_lexical_index_excludes_secrets_and_returns_citations(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            git(repo, "init")
            git(repo, "config", "user.email", "test@example.com")
            git(repo, "config", "user.name", "Test")
            (repo / "docs").mkdir()
            (repo / "docs" / "guide.md").write_text(
                "# 指南\n\n多会话总控负责拆分并行任务。\n", encoding="utf-8"
            )
            path_secrets = {
                ".env": "ENV_FAKE_NEVER_INDEX",
                ".envrc": "ENVRC_FAKE_NEVER_INDEX",
                ".npmrc": "NPM_FAKE_NEVER_INDEX",
                ".pypirc": "PYPI_FAKE_NEVER_INDEX",
                ".netrc": "NETRC_FAKE_NEVER_INDEX",
                ".aws/credentials": "AWS_FAKE_NEVER_INDEX",
                "token.json": "TOKEN_FAKE_NEVER_INDEX",
                "prod.tfvars": "TFVARS_FAKE_NEVER_INDEX",
                "terraform.tfstate": "TFSTATE_FAKE_NEVER_INDEX",
                ".docker/config.json": "DOCKER_FAKE_NEVER_INDEX",
                ".kube/config": "KUBE_FAKE_NEVER_INDEX",
                "secrets.yaml": "SECRETS_YAML_FAKE_NEVER_INDEX",
            }
            for relative, marker in path_secrets.items():
                secret_path = repo / relative
                secret_path.parent.mkdir(parents=True, exist_ok=True)
                secret_path.write_text(marker + "\n", encoding="utf-8")
            content_secret = "ghp_" + "A" * 36
            (repo / "docs" / "internal.md").write_text(
                "Accidentally committed token: " + content_secret + "\n",
                encoding="utf-8",
            )
            assignment_secret = "CONFIG_ASSIGNMENT_FAKE_849257"
            (repo / "docs" / "settings.txt").write_text(
                "client_secret: " + assignment_secret + "\n", encoding="utf-8"
            )
            private_key_marker = "PRIVATE_KEY_FAKE_NEVER_INDEX"
            private_key_header = "-----BEGIN OPENSSH " + "PRIVATE KEY-----"
            (repo / "id_custom").write_text(
                private_key_header
                + "\n"
                + private_key_marker
                + "\n-----END OPENSSH PRIVATE KEY-----\n",
                encoding="utf-8",
            )
            (repo / "docs" / "token-guide.md").write_text(
                "Token is a general authentication term; keep real values private.\n",
                encoding="utf-8",
            )
            git(repo, "add", ".")
            git(repo, "commit", "-m", "seed")

            knowledge = base / "knowledge"
            initialized = run_rag(knowledge, "init", repo)
            self.assertTrue(initialized["ok"])
            indexed = run_rag(knowledge, "index", repo, "--vector-mode", "off")
            self.assertEqual(indexed["stats"]["files"], 2)
            found = run_rag(knowledge, "query", repo, "多会话总控")
            self.assertEqual(found["status"], "PROJECT_CACHE_HIT")
            self.assertEqual(found["results"][0]["path"], "docs/guide.md")
            self.assertEqual(found["results"][0]["line_start"], 1)
            blocked_markers = [
                *path_secrets.values(),
                content_secret,
                assignment_secret,
                private_key_marker,
            ]
            for marker in blocked_markers:
                secret = run_rag(knowledge, "query", repo, marker)
                self.assertEqual(secret["results"], [])
            safe_mention = run_rag(knowledge, "query", repo, "general authentication")
            self.assertEqual(safe_mention["results"][0]["path"], "docs/token-guide.md")

            (repo / "docs" / "guide.md").write_text("未重新索引的新内容。\n", encoding="utf-8")
            cached = run_rag(knowledge, "query", repo, "多会话总控")
            self.assertEqual(cached["status"], "PROJECT_CACHE_HIT")

    def test_remote_credentials_are_redacted_before_persistence(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            git(repo, "init")
            credentialed_remote = (
                "https://oauth2:ghp_FAKESECRET@github.com/Acme/Repo.git"
                "?access_token=ALSOFAKE#fragment"
            )
            git(repo, "remote", "add", "origin", credentialed_remote)

            knowledge = base / "knowledge"
            initialized = run_rag(knowledge, "init", repo)
            self.assertEqual(
                initialized["manifest"]["identity"]["remote"],
                "https://github.com/Acme/Repo",
            )
            persisted = json.dumps(initialized, ensure_ascii=False)
            persisted += (knowledge / "catalog.json").read_text(encoding="utf-8")
            for secret in ("ghp_FAKESECRET", "ALSOFAKE", "oauth2"):
                self.assertNotIn(secret, persisted)

    def test_local_remote_path_is_replaced_by_fingerprint(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            git(repo, "init")
            git(repo, "remote", "add", "origin", "file:///C:/Users/Alice/private/repo.git")

            knowledge = base / "knowledge"
            initialized = run_rag(knowledge, "init", repo)
            remote = initialized["manifest"]["identity"]["remote"]
            self.assertTrue(remote.startswith("local://"))
            persisted = json.dumps(initialized, ensure_ascii=False)
            persisted += (knowledge / "catalog.json").read_text(encoding="utf-8")
            for private_part in ("Alice", "private", "C:/Users"):
                self.assertNotIn(private_part, persisted)

    def test_unborn_repository_can_initialize(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "empty"
            repo.mkdir()
            git(repo, "init")
            output = run_rag(base / "knowledge", "init", repo)
            self.assertEqual(output["manifest"]["identity"]["head"], "UNBORN")

    def test_p2604_initialization_uses_stable_project_id(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "p2604_soc"
            repo.mkdir()
            git(repo, "init")

            output = run_rag(base / "knowledge", "init", repo)

            self.assertEqual(Path(output["project_dir"]).name, "p2604_soc")

    def test_old_security_schema_must_be_reindexed_before_query(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            git(repo, "init")
            knowledge = base / "knowledge"
            indexed = run_rag(knowledge, "index", repo, "--vector-mode", "off")
            manifest_path = Path(indexed["project_dir"]) / "manifest.json"
            manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            manifest["schema_version"] = 1
            manifest_path.write_text(json.dumps(manifest), encoding="utf-8")

            command = [
                sys.executable, str(RAG), "--knowledge-root", str(knowledge), "query",
                "--project-root", str(repo), "--session-id", "test-security-session", "anything",
            ]
            project_result = subprocess.run(
                command, check=False, capture_output=True, encoding="utf-8"
            )
            self.assertEqual(project_result.returncode, 1)
            self.assertIn("项目知识索引安全版本已过期", project_result.stdout)

            run_rag(knowledge, "index", repo, "--vector-mode", "off")
            global_index = index_global(knowledge)
            global_manifest_path = Path(global_index["global_dir"]) / "manifest.json"
            global_manifest = json.loads(
                global_manifest_path.read_text(encoding="utf-8")
            )
            global_manifest["schema_version"] = 1
            global_manifest_path.write_text(
                json.dumps(global_manifest), encoding="utf-8"
            )
            global_result = subprocess.run(
                command, check=False, capture_output=True, encoding="utf-8"
            )
            self.assertEqual(global_result.returncode, 1)
            self.assertIn("全局知识索引不可用或已过期", global_result.stdout)

    def test_linked_worktrees_share_project_cache_without_remote(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "main"
            worker = base / "worker"
            repo.mkdir()
            git(repo, "init")
            git(repo, "config", "user.email", "test@example.com")
            git(repo, "config", "user.name", "Test")
            (repo / "README.md").write_text("seed\n", encoding="utf-8")
            git(repo, "add", "README.md")
            git(repo, "commit", "-m", "seed")
            git(repo, "worktree", "add", "-b", "worker", str(worker))

            knowledge = base / "knowledge"
            main = run_rag(knowledge, "init", repo)
            linked = run_rag(knowledge, "init", worker)
            self.assertEqual(main["project_dir"], linked["project_dir"])

    @unittest.skipIf(os.name == "nt", "POSIX paths are case-sensitive")
    def test_posix_repository_paths_keep_distinct_cache_ids(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            upper = base / "Repo"
            lower = base / "repo"
            upper.mkdir()
            lower.mkdir()
            git(upper, "init")
            git(lower, "init")

            knowledge = base / "knowledge"
            upper_result = run_rag(knowledge, "init", upper)
            lower_result = run_rag(knowledge, "init", lower)
            self.assertNotEqual(
                upper_result["project_dir"], lower_result["project_dir"]
            )

    def test_query_layers_only_approved_global_knowledge(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            base = Path(temporary)
            repo = base / "repo"
            repo.mkdir()
            git(repo, "init")
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

            indexed = index_global(knowledge)
            self.assertEqual(indexed["stats"]["files"], 2)
            run_rag(knowledge, "index", repo, "--vector-mode", "off")
            found = run_rag(knowledge, "query", repo, "复审通过")
            self.assertEqual(found["status"], "GLOBAL_FALLBACK_HIT")
            self.assertEqual(found["results"][0]["scope"], "global-fallback")
            self.assertEqual(
                found["results"][0]["path"],
                "personal-experience/approved/lesson.md",
            )
            (approved / "lesson.md").write_text(
                "全局知识已经变化，必须重新索引。\n", encoding="utf-8"
            )
            cached = run_rag(knowledge, "query", repo, "复审通过")
            self.assertEqual(cached["status"], "PROJECT_CACHE_HIT")
            hidden = run_rag(knowledge, "query", repo, "特殊暗号")
            self.assertEqual(hidden["results"], [])
            global_secret = run_rag(
                knowledge, "query", repo, "GLOBAL_FAKE_SECRET_938475"
            )
            self.assertEqual(global_secret["results"], [])
            safe_mention = run_rag(knowledge, "query", repo, "Token is a label")
            self.assertEqual(safe_mention["results"][0]["scope"], "global-fallback")


if __name__ == "__main__":
    unittest.main()

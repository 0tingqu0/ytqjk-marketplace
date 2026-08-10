from __future__ import annotations

import hashlib
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).parents[1] / "handoff_cli.py"


def run(*args: str, cwd: Path | None = None, check: bool = True) -> subprocess.CompletedProcess[bytes]:
    result = subprocess.run(args, cwd=cwd, capture_output=True, check=False)
    if check and result.returncode != 0:
        raise AssertionError((result.stderr or result.stdout).decode("utf-8", errors="replace"))
    return result


def git(repo: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[bytes]:
    return run("git", "-C", str(repo), *args, check=check)


class HandoffCliTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name) / "测试 工作区"
        self.root.mkdir()
        source = self.root / "source"
        run("git", "init", "--initial-branch=main", str(source))
        git(source, "config", "user.email", "test@example.com")
        git(source, "config", "user.name", "Test User")
        (source / "note.txt").write_text("base\n", encoding="utf-8")
        (source / "image.bin").write_bytes(b"\x00base\xff")
        git(source, "add", "--", "note.txt", "image.bin")
        git(source, "commit", "-m", "base")
        self.base = git(source, "rev-parse", "HEAD").stdout.decode("ascii").strip()
        self.worker = self.root / "worker"
        self.integration = self.root / "integration"
        run("git", "clone", "--quiet", str(source), str(self.worker))
        run("git", "clone", "--quiet", str(source), str(self.integration))
        git(self.integration, "config", "user.email", "test@example.com")
        git(self.integration, "config", "user.name", "Test User")

    def tearDown(self) -> None:
        self.temporary.cleanup()

    def cli(self, *args: str, check: bool = True) -> tuple[subprocess.CompletedProcess[bytes], dict[str, object]]:
        result = run(sys.executable, str(SCRIPT), *args, check=False)
        output = json.loads(result.stdout.decode("utf-8"))
        if check and result.returncode != 0:
            self.fail(str(output))
        return result, output

    def export_fixture(self) -> Path:
        (self.worker / "note.txt").write_text("worker change\n", encoding="utf-8")
        (self.worker / "image.bin").write_bytes(b"\x00changed\x01\xff")
        extra = self.worker / "new" / "result.txt"
        extra.parent.mkdir()
        extra.write_text("untracked\n", encoding="utf-8")
        bundle = self.root / "handoff.bundle"
        _, output = self.cli(
            "export",
            "--repo",
            str(self.worker),
            "--bundle",
            str(bundle),
            "--path",
            "note.txt",
            "--path",
            "image.bin",
            "--path",
            "new/result.txt",
        )
        self.assertTrue(output["ok"])
        self.assertEqual(git(self.worker, "rev-parse", "HEAD").stdout.decode().strip(), self.base)
        self.assertEqual(git(self.worker, "diff", "--cached", "--name-only").stdout, b"")
        return bundle

    def test_export_and_apply_stage_exact_binary_and_untracked_changes(self) -> None:
        bundle = self.export_fixture()
        manifest = json.loads((bundle / "manifest.json").read_text(encoding="utf-8"))
        payload = bundle / "untracked" / "new" / "result.txt"
        self.assertEqual(manifest["base_head"], self.base)
        self.assertEqual(manifest["untracked"][0]["sha256"], hashlib.sha256(payload.read_bytes()).hexdigest())

        _, output = self.cli("apply", "--repo", str(self.integration), "--bundle", str(bundle))

        expected = ["image.bin", "new/result.txt", "note.txt"]
        self.assertEqual(output["staged_paths"], expected)
        self.assertEqual(len(str(output["staged_snapshot_sha256"])), 64)
        staged = git(
            self.integration, "diff", "--cached", "--name-only", "--no-renames", "-z"
        ).stdout.decode("utf-8").strip("\0").split("\0")
        self.assertEqual(sorted(staged), expected)
        self.assertEqual((self.integration / "image.bin").read_bytes(), b"\x00changed\x01\xff")
        self.assertEqual((self.integration / "new" / "result.txt").read_text(), "untracked\n")
        self.assertEqual(git(self.integration, "diff", "--name-only").stdout, b"")
        self.assertEqual(git(self.integration, "rev-parse", "HEAD").stdout.decode().strip(), self.base)

    def test_untracked_only_bundle_applies(self) -> None:
        (self.worker / "only.txt").write_text("only untracked\n", encoding="utf-8")
        bundle = self.root / "untracked-only.bundle"
        self.cli(
            "export", "--repo", str(self.worker), "--bundle", str(bundle),
            "--path", "only.txt",
        )
        _, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle)
        )
        self.assertEqual(output["staged_paths"], ["only.txt"])
        self.assertEqual((self.integration / "only.txt").read_text(), "only untracked\n")

    def test_unicode_path_and_spaced_bundle_round_trip(self) -> None:
        target = self.worker / "docs" / "说明 文件.md"
        target.parent.mkdir()
        target.write_text("跨平台交接\n", encoding="utf-8")
        bundle = self.root / "交接 包"
        self.cli(
            "export",
            "--repo",
            str(self.worker),
            "--bundle",
            str(bundle),
            "--path",
            "docs/说明 文件.md",
        )

        manifest = json.loads((bundle / "manifest.json").read_text(encoding="utf-8"))
        self.assertEqual(manifest["allowlist"], ["docs/说明 文件.md"])
        _, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle)
        )
        self.assertEqual(output["staged_paths"], ["docs/说明 文件.md"])
        self.assertEqual(
            (self.integration / "docs" / "说明 文件.md").read_text(encoding="utf-8"),
            "跨平台交接\n",
        )

    def test_export_rejects_staged_changes_without_creating_bundle(self) -> None:
        (self.worker / "note.txt").write_text("staged\n", encoding="utf-8")
        git(self.worker, "add", "--", "note.txt")
        bundle = self.root / "rejected.bundle"
        result, output = self.cli(
            "export",
            "--repo",
            str(self.worker),
            "--bundle",
            str(bundle),
            "--path",
            "note.txt",
            check=False,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("must not stage", str(output["error"]))
        self.assertFalse(bundle.exists())

    def test_export_rejects_intent_to_add_index_entry(self) -> None:
        (self.worker / "intent.txt").write_text("intent\n", encoding="utf-8")
        git(self.worker, "add", "--intent-to-add", "--", "intent.txt")
        result, output = self.cli(
            "export",
            "--repo",
            str(self.worker),
            "--bundle",
            str(self.root / "intent.bundle"),
            "--path",
            "intent.txt",
            check=False,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("must not stage", str(output["error"]))

    def test_conflicting_descendant_fails_during_precheck_and_stays_clean(self) -> None:
        bundle = self.export_fixture()
        (self.integration / "note.txt").write_text("integration change\n", encoding="utf-8")
        git(self.integration, "add", "--", "note.txt")
        git(self.integration, "commit", "-m", "conflict")
        head = git(self.integration, "rev-parse", "HEAD").stdout.decode().strip()

        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle), check=False
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn("does not apply cleanly", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")
        self.assertEqual(git(self.integration, "rev-parse", "HEAD").stdout.decode().strip(), head)

    def test_untracked_target_added_by_descendant_is_rejected_before_patch(self) -> None:
        bundle = self.export_fixture()
        target = self.integration / "new" / "result.txt"
        target.parent.mkdir()
        target.write_text("integration-owned\n", encoding="utf-8")
        git(self.integration, "add", "--", "new/result.txt")
        git(self.integration, "commit", "-m", "claim target")

        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle), check=False
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn("already exists", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")
        self.assertEqual(target.read_text(), "integration-owned\n")

    def test_untracked_parent_file_is_rejected_before_patch(self) -> None:
        bundle = self.export_fixture()
        (self.integration / "new").write_text("tracked parent\n", encoding="utf-8")
        git(self.integration, "add", "--", "new")
        git(self.integration, "commit", "-m", "claim parent")
        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle), check=False
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("unsafe parent", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")

    def test_ignored_untracked_target_is_rejected_before_patch(self) -> None:
        bundle = self.export_fixture()
        (self.integration / ".gitignore").write_text("new/result.txt\n", encoding="utf-8")
        git(self.integration, "add", "--", ".gitignore")
        git(self.integration, "commit", "-m", "ignore worker payload")
        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle), check=False
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("is ignored", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")
        self.assertFalse((self.integration / "new" / "result.txt").exists())
        self.assertEqual((self.integration / "note.txt").read_text(), "base\n")

    def test_required_clean_filter_failure_is_transactional(self) -> None:
        (self.worker / "note.txt").write_text("worker change\n", encoding="utf-8")
        (self.worker / "new.fail").write_text("filtered\n", encoding="utf-8")
        bundle = self.root / "filter.bundle"
        self.cli(
            "export", "--repo", str(self.worker), "--bundle", str(bundle),
            "--path", "note.txt", "--path", "new.fail",
        )
        (self.integration / ".gitattributes").write_text(
            "*.fail filter=broken\n", encoding="utf-8"
        )
        git(self.integration, "config", "filter.broken.clean", "false")
        git(self.integration, "config", "filter.broken.required", "true")
        git(self.integration, "add", "--", ".gitattributes")
        git(self.integration, "commit", "-m", "add required broken filter")

        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle),
            check=False,
        )

        self.assertEqual(result.returncode, 1)
        self.assertIn("filter failed", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")
        self.assertEqual((self.integration / "note.txt").read_text(), "base\n")
        self.assertFalse((self.integration / "new.fail").exists())

    def test_post_preflight_filter_failure_rolls_back_exact_changes(self) -> None:
        (self.worker / "note.txt").write_text("worker change\n", encoding="utf-8")
        (self.worker / "new.once").write_text("filtered\n", encoding="utf-8")
        bundle = self.root / "rollback.bundle"
        self.cli(
            "export", "--repo", str(self.worker), "--bundle", str(bundle),
            "--path", "note.txt", "--path", "new.once",
        )
        (self.integration / ".gitattributes").write_text(
            "*.once filter=once\n", encoding="utf-8"
        )
        git(self.integration, "add", "--", ".gitattributes")
        git(self.integration, "commit", "-m", "add one-shot filter")
        sentinel = (self.root / "filter-ran").as_posix()
        command = (
            f"sh -c 'if test -f \"{sentinel}\"; then exit 1; "
            f"else touch \"{sentinel}\"; cat; fi'"
        )
        git(self.integration, "config", "filter.once.clean", command)
        git(self.integration, "config", "filter.once.required", "true")

        result, output = self.cli(
            "apply", "--repo", str(self.integration), "--bundle", str(bundle),
            check=False,
        )

        self.assertEqual(result.returncode, 1)
        self.assertNotIn("rollback failed", str(output["error"]))
        self.assertEqual(git(self.integration, "status", "--porcelain").stdout, b"")
        self.assertEqual((self.integration / "note.txt").read_text(), "base\n")
        self.assertFalse((self.integration / "new.once").exists())


if __name__ == "__main__":
    unittest.main()

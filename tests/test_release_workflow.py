from __future__ import annotations

import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[1]
TEST_WORKFLOW = ROOT / ".github" / "workflows" / "test.yml"
RELEASE_WORKFLOW = ROOT / ".github" / "workflows" / "release.yml"


class ReleaseWorkflowTest(unittest.TestCase):
    def test_test_workflow_is_reusable_without_duplicate_tag_run(self) -> None:
        source = TEST_WORKFLOW.read_text(encoding="utf-8")

        self.assertIn('branches:\n      - "**"', source)
        self.assertIn("workflow_call:", source)
        self.assertNotIn("tags:", source)

    def test_release_requires_contract_and_complete_test_suite(self) -> None:
        source = RELEASE_WORKFLOW.read_text(encoding="utf-8")

        self.assertIn('tags:\n      - "v*"', source)
        self.assertIn("release versions are inconsistent", source)
        self.assertIn("Release commit is not contained in main", source)
        self.assertIn("uses: actions/setup-python@v5", source)
        self.assertIn("uses: ./.github/workflows/test.yml", source)
        self.assertIn("needs: [contract, tests]", source)

    def test_release_tag_requires_pure_semver(self) -> None:
        source = RELEASE_WORKFLOW.read_text(encoding="utf-8")
        found = re.search(r'\[\[ ! "\$\{tag\}" =~ (.+) \]\]; then', source)

        self.assertIsNotNone(found)
        pattern = re.compile(found.group(1))
        for tag in ("v0.6.7", "v1.0.0", "v10.20.30"):
            self.assertIsNotNone(pattern.fullmatch(tag))
        for tag in ("v01.6.7", "v0.06.7", "v0.6.07", "v0.6.7.1"):
            self.assertIsNone(pattern.fullmatch(tag))

    def test_only_publish_job_can_write_and_release_is_stable(self) -> None:
        source = RELEASE_WORKFLOW.read_text(encoding="utf-8")

        self.assertIn("permissions:\n  contents: read", source)
        self.assertEqual(source.count("contents: write"), 1)
        for argument in (
            "--verify-tag",
            "--generate-notes",
            "--fail-on-no-commits",
            "--latest",
        ):
            self.assertIn(argument, source)
        self.assertNotIn("--draft", source)
        self.assertNotIn("--prerelease", source)


if __name__ == "__main__":
    unittest.main()

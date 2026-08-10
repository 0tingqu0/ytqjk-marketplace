from __future__ import annotations

import os
import sys
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from rag_security import normalize_remote  # noqa: E402


class RemoteSecurityTest(unittest.TestCase):
    def test_network_remote_redacts_credentials_and_preserves_path_case(self) -> None:
        remote = normalize_remote(
            "HTTPS://oauth:secret@Git.Example.com/Group/Repo.GIT?token=x#fragment"
        )
        self.assertEqual(remote, "https://git.example.com/Group/Repo")

    def test_scp_remote_preserves_path_case(self) -> None:
        self.assertEqual(
            normalize_remote("git@Git.Example.com:Org/Repo.git"),
            "ssh://git.example.com/Org/Repo",
        )

    def test_file_remote_does_not_persist_private_directories(self) -> None:
        remote = normalize_remote("file:///home/Alice/private/Repo.git")
        self.assertTrue(remote.startswith("local://"))
        for private_part in ("Alice", "private", "/home"):
            self.assertNotIn(private_part, remote)

    @unittest.skipIf(os.name == "nt", "POSIX paths are case-sensitive")
    def test_posix_local_remote_fingerprint_preserves_case(self) -> None:
        upper = normalize_remote("/tmp/Owner/Repo.git")
        lower = normalize_remote("/tmp/owner/repo.git")
        self.assertNotEqual(upper, lower)


if __name__ == "__main__":
    unittest.main()

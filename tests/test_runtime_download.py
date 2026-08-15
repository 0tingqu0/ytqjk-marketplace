from __future__ import annotations

from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

from runtime_download import download_node_file


class RuntimeDownloadTest(unittest.TestCase):
    def test_windows_prefers_curl_with_https_and_time_limits(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "node.zip"

            def complete(command: list[str], **_: object) -> None:
                destination.write_bytes(b"download")

            with mock.patch(
                "runtime_download.subprocess.run", side_effect=complete
            ) as run:
                download_node_file(
                    "https://nodejs.org/dist/test.zip",
                    destination,
                    system="Windows",
                    which=lambda _: r"C:\Windows\System32\curl.exe",
                )

        command = run.call_args.args[0]
        self.assertIn("--proto", command)
        self.assertIn("=https", command)
        self.assertIn("--connect-timeout", command)
        self.assertIn("--max-time", command)
        self.assertFalse(run.call_args.kwargs["shell"])

    def test_rejects_non_nodejs_source_before_download(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "node.zip"
            with self.assertRaisesRegex(RuntimeError, "not allowed"):
                download_node_file(
                    "https://example.com/node.zip", destination
                )
            self.assertFalse(destination.exists())

    def test_curl_failure_is_reported_without_command_details(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            destination = Path(directory) / "node.zip"
            failure = subprocess.CalledProcessError(
                6, ["curl.exe", "https://nodejs.org/private-detail"]
            )
            with mock.patch(
                "runtime_download.subprocess.run", side_effect=failure
            ):
                with self.assertRaisesRegex(
                    RuntimeError, "^runtime download failed$"
                ):
                    download_node_file(
                        "https://nodejs.org/dist/test.zip",
                        destination,
                        system="Windows",
                        which=lambda _: r"C:\Windows\System32\curl.exe",
                    )
            self.assertFalse(destination.exists())


if __name__ == "__main__":
    unittest.main()

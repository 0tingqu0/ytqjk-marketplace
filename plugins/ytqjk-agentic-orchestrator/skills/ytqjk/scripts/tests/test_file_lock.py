from __future__ import annotations

import concurrent.futures
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from file_lock import exclusive_file_lock  # noqa: E402


HOLDER = """
import sys
import time
from pathlib import Path

sys.path.insert(0, sys.argv[1])
from file_lock import exclusive_file_lock

with exclusive_file_lock(Path(sys.argv[2])):
    print("READY", flush=True)
    time.sleep(float(sys.argv[3]))
"""


class FileLockTest(unittest.TestCase):
    def start_holder(self, path: Path, seconds: float) -> subprocess.Popen[str]:
        process = subprocess.Popen(
            [
                sys.executable,
                "-X",
                "utf8",
                "-c",
                HOLDER,
                str(SCRIPTS),
                str(path),
                str(seconds),
            ],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
        )
        assert process.stdout is not None
        self.assertEqual(process.stdout.readline().strip(), "READY")
        return process

    def test_waits_until_another_process_releases_lock(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "shared.lock"
            holder = self.start_holder(lock, 0.3)
            started = time.monotonic()
            try:
                with exclusive_file_lock(
                    lock, timeout_seconds=2.0, poll_seconds=0.02
                ):
                    elapsed = time.monotonic() - started
            finally:
                holder.wait(timeout=2)
                assert holder.stdout is not None and holder.stderr is not None
                holder.stdout.close()
                holder.stderr.close()

            self.assertGreaterEqual(elapsed, 0.15)
            self.assertEqual(holder.returncode, 0)

    def test_reports_clear_timeout_while_lock_is_held(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "shared.lock"
            holder = self.start_holder(lock, 0.8)
            try:
                with self.assertRaisesRegex(TimeoutError, "等待文件锁超时"):
                    with exclusive_file_lock(
                        lock, timeout_seconds=0.15, poll_seconds=0.02
                    ):
                        self.fail("contended lock unexpectedly acquired")
            finally:
                holder.wait(timeout=2)
                assert holder.stdout is not None and holder.stderr is not None
                holder.stdout.close()
                holder.stderr.close()

            self.assertEqual(holder.returncode, 0)

    def test_concurrent_empty_lock_initialization_is_safe(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            lock = Path(temporary) / "new.lock"

            def acquire() -> None:
                with exclusive_file_lock(
                    lock,
                    timeout_seconds=2.0,
                    poll_seconds=0.005,
                ):
                    time.sleep(0.002)

            with concurrent.futures.ThreadPoolExecutor(8) as pool:
                results = [pool.submit(acquire) for _ in range(32)]
                for result in results:
                    result.result(timeout=5)

            self.assertEqual(lock.read_bytes(), b"\0")

    def test_hardlinked_lock_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            outside = root / "outside"
            lock = root / "shared.lock"
            outside.write_bytes(b"\0")
            os.link(outside, lock)

            with self.assertRaisesRegex(ValueError, "单链接"):
                with exclusive_file_lock(lock):
                    self.fail("hardlinked lock unexpectedly acquired")

            self.assertEqual(outside.read_bytes(), b"\0")


if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import io
import json
import subprocess
import sys
import unittest
from contextlib import redirect_stdout
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import session_query  # noqa: E402


class SessionQueryTest(unittest.TestCase):
    def test_timeout_is_bounded_and_retryable(self) -> None:
        arguments = [
            "session_query.py",
            "--knowledge-root",
            "knowledge",
            "--project-root",
            "project",
            "--session-id",
            "thread-1",
            "--expected-project-id",
            "project-1",
            "question",
        ]
        output = io.StringIO()
        expired = subprocess.TimeoutExpired(["query"], 60)

        with mock.patch.object(sys, "argv", arguments), mock.patch.object(
            session_query.subprocess, "run", side_effect=expired
        ) as run, redirect_stdout(output):
            result = session_query.main()

        payload = json.loads(output.getvalue())
        self.assertEqual(result, 1)
        self.assertEqual(run.call_args.kwargs["timeout"], 60)
        self.assertTrue(payload["retryable"])
        self.assertEqual(payload["timeout_seconds"], 60)


if __name__ == "__main__":
    unittest.main()

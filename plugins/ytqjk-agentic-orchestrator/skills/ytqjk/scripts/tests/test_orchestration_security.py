from __future__ import annotations

import ast
import sqlite3
import sys
import tempfile
import unittest
from contextlib import closing
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from orchestration_identity import OrchestrationControlPlane, RunState  # noqa: E402
from orchestration_key import LocalHmacKey  # noqa: E402
from orchestration_models import LedgerUnavailable, canonical_scope  # noqa: E402
from orchestration_storage import Transaction  # noqa: E402


HASH = "a" * 64
SESSION = "c" * 64
OTHER_SESSION = "d" * 64
STAGED_HASH = "b" * 64


class TrustedSessionAttackTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.database = root / "ledger.sqlite"
        self.key = root / "identity.key"
        self.control = OrchestrationControlPlane(self.database, self.key)
        self.control.initialize()
        self.run_id = self.control.start_run("project-1", HASH, SESSION, SESSION)
        self.control.register_role(
            self.run_id,
            SESSION,
            "worker",
            ["src/read.py"],
            ["src/write.py"],
            True,
            current_session_key=SESSION,
        )
        self.control.register_role(
            self.run_id,
            SESSION,
            "director",
            [],
            [],
            False,
            ["run:lifecycle"],
            SESSION,
        )

    def tearDown(self) -> None:
        self.temp.cleanup()

    def token(self):
        return self.control.issue_attestation(
            self.run_id,
            SESSION,
            "worker",
            ["src/read.py"],
            ["src/write.py"],
            True,
            STAGED_HASH,
            current_session_key=SESSION,
        )

    def test_complete_token_cannot_cross_session_or_instance(self) -> None:
        token = self.token()
        other_instance = OrchestrationControlPlane(self.database, self.key)
        self.assertEqual(
            self.control.gate_mutation(token, OTHER_SESSION).state, "BLOCKED"
        )
        self.assertEqual(
            other_instance.gate_mutation(token, OTHER_SESSION).state, "BLOCKED"
        )
        self.assertEqual(self.control.gate_mutation(token).state, "BLOCKED")

    def test_sensitive_lease_apis_require_current_session(self) -> None:
        token = self.token()
        calls = (
            lambda: self.control.renew_attestation(token),
            lambda: self.control.revoke_lease(self.run_id, SESSION, token.lease_id),
            lambda: self.control.lease_status(token.lease_id),
        )
        for call in calls:
            with self.subTest(call=call), self.assertRaises(LedgerUnavailable):
                call()

    def test_explicit_session_claim_mismatch_is_blocked(self) -> None:
        with self.assertRaises(LedgerUnavailable):
            self.control.issue_attestation(
                self.run_id,
                OTHER_SESSION,
                "worker",
                [],
                [],
                False,
                current_session_key=SESSION,
            )
        token = self.token()
        with self.assertRaises(LedgerUnavailable):
            self.control.revoke_lease(
                self.run_id, OTHER_SESSION, token.lease_id, SESSION
            )

    def test_lifecycle_requires_bound_session_and_capability(self) -> None:
        with self.assertRaises(LedgerUnavailable):
            self.control.transition_run_cas(
                self.run_id, 0, RunState.RUNNING, RunState.PAUSED
            )
        with self.assertRaises(LedgerUnavailable):
            self.control.transition_run_cas(
                self.run_id, 0, RunState.RUNNING, RunState.PAUSED, OTHER_SESSION
            )
        other_run = self.control.start_run("project-1", HASH, SESSION, SESSION)
        with self.assertRaises(LedgerUnavailable):
            self.control.transition_run_cas(
                other_run, 0, RunState.RUNNING, RunState.PAUSED, SESSION
            )


class DirectorLedgerAttackTest(unittest.TestCase):
    def new_run(self) -> tuple[OrchestrationControlPlane, Path, str, tempfile.TemporaryDirectory]:
        temp = tempfile.TemporaryDirectory()
        root = Path(temp.name)
        control = OrchestrationControlPlane(root / "ledger.sqlite", root / "key")
        control.initialize()
        run_id = control.start_run("project-1", HASH, SESSION, SESSION)
        return control, root / "ledger.sqlite", run_id, temp

    def test_db_rejects_director_task_permissions(self) -> None:
        attacks = (
            ("director", '["src/read.py"]', "[]", 0, "[]"),
            ("controller", "[]", '["src/write.py"]', 0, "[]"),
            ("director", "[]", "[]", 1, "[]"),
            ("controller", "[]", "[]", 0, '["test"]'),
            ("director", "[]", "[]", 0, '["approval"]'),
            ("controller", "[]", "[]", 0, '["knowledge:approve"]'),
            ("controller", "[]", "[]", 0, '["git:commit"]'),
        )
        for attack in attacks:
            control, database, run_id, temp = self.new_run()
            try:
                with closing(sqlite3.connect(database)) as conn:
                    with self.assertRaises(sqlite3.IntegrityError):
                        conn.execute(
                            "INSERT INTO role_ledger VALUES (?, ?, ?, ?, ?, ?, ?, 0)",
                            (run_id, SESSION, *attack),
                        )
            finally:
                del control
                temp.cleanup()

    def test_api_blocks_bad_director_row_after_db_defenses_bypassed(self) -> None:
        control, database, run_id, temp = self.new_run()
        try:
            with closing(sqlite3.connect(database)) as conn:
                conn.execute("DROP TRIGGER director_zero_task_power")
                conn.execute("PRAGMA ignore_check_constraints = ON")
                conn.execute(
                    "INSERT INTO role_ledger VALUES "
                    "(?, ?, 'controller', '[]', '[\"src/write.py\"]', 1, '[]', 0)",
                    (run_id, SESSION),
                )
                conn.commit()
            with self.assertRaises(LedgerUnavailable):
                control.issue_attestation(
                    run_id,
                    SESSION,
                    "controller",
                    [],
                    [],
                    False,
                    current_session_key=SESSION,
                )
        finally:
            del control
            temp.cleanup()


class WindowsAclParserAttackTest(unittest.TestCase):
    def assert_acl_rejected(self, line: str) -> None:
        key = LocalHmacKey(Path("identity.key"))
        completed = mock.Mock(returncode=0, stdout=f"identity {line}\n", stderr="")
        with mock.patch("orchestration_key.os.name", "nt"), mock.patch.object(
            key, "_current_sid", return_value=SESSION
        ), mock.patch.object(
            key, "_current_name", return_value="DOMAIN\\USER"
        ), mock.patch(
            "orchestration_key.subprocess.run", return_value=completed
        ):
            with self.assertRaises(LedgerUnavailable):
                key._verify_permissions()

    def test_extra_identities_and_unknown_lines_fail_closed(self) -> None:
        for line in ("Authenticated Users:(R,W)", "Everyone:(R)", "UNKNOWN ACL"):
            with self.subTest(line=line):
                self.assert_acl_rejected(line)


class TransactionClosureTest(unittest.TestCase):
    def test_begin_failure_still_closes(self) -> None:
        conn = mock.Mock()
        conn.execute.side_effect = sqlite3.OperationalError("failure")
        with self.assertRaises(sqlite3.OperationalError):
            Transaction(conn).__enter__()
        conn.close.assert_called_once_with()

    def test_commit_and_rollback_failures_still_close(self) -> None:
        for method, exc_type in (("commit", None), ("rollback", RuntimeError)):
            conn = mock.Mock()
            getattr(conn, method).side_effect = sqlite3.OperationalError("failure")
            transaction = Transaction(conn)
            with self.subTest(method=method), self.assertRaises(sqlite3.OperationalError):
                transaction.__exit__(exc_type, None, None)
            conn.close.assert_called_once_with()

    def test_database_can_be_deleted_after_transactions(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            database = Path(directory) / "ledger.sqlite"
            control = OrchestrationControlPlane(database, Path(directory) / "key")
            control.initialize()
            control.start_run("project-1", HASH, SESSION, SESSION)
            database.unlink()
            self.assertFalse(database.exists())


class IsolationBoundaryTest(unittest.TestCase):
    def test_legacy_sensitive_path_policy_is_preserved_locally(self) -> None:
        sensitive = (
            ".cargo/config",
            ".m2/settings.xml",
            ".venv/auth.txt",
            ".my.cnf",
            ".pgpass",
            "_netrc",
            "settings-security.xml",
            "client.ovpn",
            "state.tfstate.backup",
            "node_modules/package/index.js",
            "credentials.toml",
            "service_account.json",
            "token.yaml",
            ".env.production",
            "keys/client.pem",
        )
        for path in sensitive:
            with self.subTest(path=path), self.assertRaises(ValueError):
                canonical_scope([path])

    def test_normal_source_and_config_paths_remain_allowed(self) -> None:
        normal = (
            "src/auth.py",
            "src/token_parser.py",
            "config/app.yaml",
            "config/settings.xml",
            "pyproject.toml",
            "tests/fixtures/sample.json",
        )
        self.assertEqual(canonical_scope(normal), tuple(sorted(normal)))

    def test_identity_import_graph_excludes_next_wave_modules(self) -> None:
        forbidden = {
            "rag_security",
            "handoff",
            "session_query",
            "session_memory",
        }
        imports: set[str] = set()
        for path in SCRIPTS.glob("orchestration_*.py"):
            tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
            for node in ast.walk(tree):
                if isinstance(node, ast.Import):
                    imports.update(alias.name.split(".", 1)[0] for alias in node.names)
                elif isinstance(node, ast.ImportFrom) and node.module:
                    imports.add(node.module.split(".", 1)[0])
        self.assertTrue(imports.isdisjoint(forbidden), imports & forbidden)


if __name__ == "__main__":
    unittest.main()

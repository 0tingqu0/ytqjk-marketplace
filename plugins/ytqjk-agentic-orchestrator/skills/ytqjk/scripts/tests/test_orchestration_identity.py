from __future__ import annotations

import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from contextlib import closing
from dataclasses import replace
from pathlib import Path
from unittest import mock


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from orchestration_identity import (  # noqa: E402
    CasConflict,
    IdentityError,
    OrchestrationControlPlane,
    RunState,
)
from orchestration_key import LocalHmacKey  # noqa: E402
from orchestration_models import LedgerUnavailable, LeaseState  # noqa: E402
from orchestration_storage import Transaction  # noqa: E402


HASH = "a" * 64
SESSION = "c" * 64
OTHER_SESSION = "d" * 64
STAGED_HASH = "b" * 64


class OrchestrationIdentityTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        root = Path(self.temp.name)
        self.database = root / "ledger.sqlite"
        self.control = OrchestrationControlPlane(self.database, root / "identity.key")
        self.database_id = self.control.initialize()
        self.run_id = self.control.start_run("project-1", HASH, SESSION, SESSION)
        self.control.register_role(
            self.run_id, SESSION, "worker", ["src/read.py"], ["src/write.py"],
            True, current_session_key=SESSION,
        )
        self.control.register_role(
            self.run_id, SESSION, "director", [], [], False,
            ["run:lifecycle"], SESSION,
        )

    def tearDown(self) -> None:
        self.temp.cleanup()

    def token(self, seconds: int = 1800):
        return self.control.issue_attestation(
            self.run_id, SESSION, "worker", ["src/read.py"], ["src/write.py"],
            True, STAGED_HASH, seconds, SESSION,
        )

    def test_attestation_binds_anonymous_session_and_consumes_once(self) -> None:
        token = self.token()
        self.assertEqual(token.database_id, self.database_id)
        self.assertEqual(token.session_key, SESSION)
        self.assertEqual(self.control.gate_mutation(token, SESSION).state, "AUTHORIZED")
        self.assertEqual(self.control.lease_status(token.lease_id, SESSION), LeaseState.CONSUMED)
        self.assertEqual(self.control.gate_mutation(token, SESSION).state, "DENIED")

    def test_cross_session_cannot_register_issue_revoke_or_forge(self) -> None:
        with self.assertRaises(IdentityError):
            self.control.register_role(self.run_id, OTHER_SESSION, "worker", [], [], False)
        with self.assertRaises(IdentityError):
            self.control.issue_attestation(
                self.run_id, OTHER_SESSION, "worker", [], [], False
            )
        token = self.token()
        with self.assertRaises(IdentityError):
            self.control.revoke_lease(
                self.run_id, OTHER_SESSION, token.lease_id, OTHER_SESSION
            )
        self.assertEqual(
            self.control.gate_mutation(
                replace(token, session_key=OTHER_SESSION), OTHER_SESSION
            ).state,
            "DENIED",
        )

    def test_tampered_required_claims_are_denied(self) -> None:
        token = self.token()
        other = self.control.start_run("project-1", HASH, SESSION, SESSION)
        variants = [
            replace(token, run_id=other),
            replace(token, project_id="other"),
            replace(token, objective_hash="c" * 64),
            replace(token, role="git"),
            replace(token, read_scope=("src/other.py",)),
            replace(token, write_scope=("src/other.py",)),
            replace(token, mutation=False),
            replace(token, staged_hash="c" * 64),
            replace(token, database_id="not-this-ledger"),
        ]
        for forged in variants:
            with self.subTest(forged=forged):
                self.assertEqual(self.control.gate_mutation(forged, SESSION).state, "DENIED")

    def test_expiry_persists_event_and_audit_before_denial(self) -> None:
        expired = self.token(1)
        time.sleep(1.1)
        self.assertEqual(self.control.gate_mutation(expired, SESSION).state, "DENIED")
        self.assertEqual(
            self.control.lease_status(expired.lease_id, SESSION), LeaseState.EXPIRED
        )
        with closing(sqlite3.connect(self.database)) as conn:
            state = conn.execute(
                "SELECT state FROM lease_events WHERE lease_id = ? ORDER BY version DESC",
                (expired.lease_id,),
            ).fetchone()[0]
            audit = conn.execute(
                "SELECT COUNT(*) FROM audit_events WHERE kind = 'lease_expired' "
                "AND lease_id = ?",
                (expired.lease_id,),
            ).fetchone()[0]
        self.assertEqual(state, "EXPIRED")
        self.assertEqual(audit, 1)

    def test_concurrent_expiry_is_idempotent(self) -> None:
        token = self.token(1)
        time.sleep(1.1)
        barrier = threading.Barrier(3)
        states: list[str] = []

        def gate() -> None:
            barrier.wait()
            states.append(self.control.gate_mutation(token, SESSION).state)

        first, second = threading.Thread(target=gate), threading.Thread(target=gate)
        first.start()
        second.start()
        barrier.wait()
        first.join()
        second.join()
        self.assertEqual(states, ["DENIED", "DENIED"])
        with closing(sqlite3.connect(self.database)) as conn:
            count = conn.execute(
                "SELECT COUNT(*) FROM audit_events WHERE kind = 'lease_expired' "
                "AND lease_id = ?",
                (token.lease_id,),
            ).fetchone()[0]
        self.assertEqual(count, 1)

    def test_renew_revoke_and_replay_protection(self) -> None:
        original = self.token()
        renewed = self.control.renew_attestation(original, 10, SESSION)
        self.assertEqual(self.control.gate_mutation(original, SESSION).state, "DENIED")
        self.assertEqual(self.control.gate_mutation(renewed, SESSION).state, "AUTHORIZED")
        revoked = self.token()
        self.control.revoke_lease(self.run_id, SESSION, revoked.lease_id, SESSION)
        self.assertEqual(self.control.gate_mutation(revoked, SESSION).state, "DENIED")

    def test_state_machine_rejects_invalid_and_revokes_non_running_leases(self) -> None:
        token = self.token()
        self.assertEqual(
            self.control.transition_run_cas(
                self.run_id, 0, RunState.RUNNING, RunState.PAUSED
                , SESSION
            ),
            1,
        )
        self.assertEqual(
            self.control.lease_status(token.lease_id, SESSION), LeaseState.REVOKED
        )
        self.assertEqual(self.control.gate_mutation(token, SESSION).state, "DENIED")
        self.assertEqual(
            self.control.transition_run_cas(
                self.run_id, 1, RunState.PAUSED, RunState.RUNNING
                , SESSION
            ),
            2,
        )
        terminal = self.control.transition_run_cas(
            self.run_id, 2, RunState.RUNNING, RunState.DONE, SESSION
        )
        with self.assertRaises(ValueError):
            self.control.transition_run_cas(
                self.run_id, terminal, RunState.DONE, RunState.RUNNING, SESSION
            )
        with self.assertRaises(CasConflict):
            self.control.transition_run_cas(
                self.run_id, 0, RunState.RUNNING, RunState.STOPPED, SESSION
            )

    def test_concurrent_run_cas_has_one_winner(self) -> None:
        barrier = threading.Barrier(3)
        results: list[str] = []

        def pause() -> None:
            barrier.wait()
            try:
                self.control.transition_run_cas(
                    self.run_id, 0, RunState.RUNNING, RunState.PAUSED
                    , SESSION
                )
                results.append("PASSED")
            except CasConflict:
                results.append("CONFLICT")

        first, second = threading.Thread(target=pause), threading.Thread(target=pause)
        first.start()
        second.start()
        barrier.wait()
        first.join()
        second.join()
        self.assertEqual(sorted(results), ["CONFLICT", "PASSED"])

    def test_database_triggers_enforce_session_and_state_rules(self) -> None:
        token = self.token()
        with closing(sqlite3.connect(self.database)) as conn:
            with self.assertRaises(sqlite3.IntegrityError):
                conn.execute(
                    "INSERT INTO role_ledger VALUES "
                    "(?, ?, 'worker', '[]', '[]', 0, '[]', 0)",
                    (self.run_id, OTHER_SESSION),
                )
            with self.assertRaises(sqlite3.IntegrityError):
                conn.execute(
                    "INSERT INTO run_events VALUES (?, 1, 'RUNNING', 0)", (self.run_id,)
                )
            with self.assertRaises(sqlite3.IntegrityError):
                conn.execute(
                    "INSERT INTO run_events VALUES (?, 1, 'PAUSED', 0)", (self.run_id,)
                )
        self.assertEqual(
            self.control.lease_status(token.lease_id, SESSION), LeaseState.ACTIVE
        )

    def test_scope_director_and_append_only_protections_remain(self) -> None:
        with self.assertRaises(IdentityError):
            self.control.register_role(
                self.run_id, SESSION, "controller", [], ["a.py"], True,
                current_session_key=SESSION,
            )
        with self.assertRaises(ValueError):
            self.control.issue_attestation(
                self.run_id, SESSION, "worker", [".env"], ["src/write.py"],
                True, STAGED_HASH, current_session_key=SESSION,
            )
        self.token()
        with closing(sqlite3.connect(self.database)) as conn:
            for statement in (
                "UPDATE metadata SET value = 'changed'",
                "DELETE FROM run_events",
                "UPDATE role_ledger SET role = 'git'",
                "DELETE FROM lease_events",
                "DELETE FROM audit_events",
            ):
                with self.subTest(statement=statement), self.assertRaises(sqlite3.IntegrityError):
                    conn.execute(statement)

    def test_ledger_or_gate_unavailable_blocks_mutation(self) -> None:
        token = self.token()
        self.control.key.path.unlink()
        self.assertEqual(self.control.gate_mutation(token, SESSION).state, "BLOCKED")

    def test_unavailable_ledger_blocks_mutation(self) -> None:
        token = self.token()
        original = self.control.ledger._connect
        self.control.ledger._connect = lambda: (_ for _ in ()).throw(sqlite3.OperationalError())
        self.assertEqual(self.control.gate_mutation(token, SESSION).state, "BLOCKED")
        self.control.ledger._connect = original


class WindowsAclTest(unittest.TestCase):
    def test_everyone_read_acl_is_rejected_before_key_read(self) -> None:
        key = LocalHmacKey(Path("identity.key"))
        completed = mock.Mock(returncode=0, stdout="identity Everyone:(R)\n", stderr="")
        with mock.patch("orchestration_key.os.name", "nt"), mock.patch.object(
            key, "_current_sid", return_value=SESSION
        ), mock.patch.object(key, "_current_name", return_value="DOMAIN\\USER"), mock.patch(
            "orchestration_key.subprocess.run", return_value=completed
        ):
            with self.assertRaises(LedgerUnavailable):
                key._verify_permissions()

    @unittest.skipUnless(sys.platform == "win32", "Windows ACL coverage")
    def test_real_everyone_read_acl_blocks_key_gate(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            key = LocalHmacKey(Path(directory) / "identity.key")
            key.read(create=True)
            result = __import__("subprocess").run(
                ["icacls", str(key.path), "/grant", "*S-1-1-0:(R)"],
                capture_output=True,
                check=False,
            )
            self.assertEqual(result.returncode, 0)
            with self.assertRaises(LedgerUnavailable):
                key.read()

    def test_posix_group_permission_is_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "identity.key"
            path.write_bytes(b"x")
            key = LocalHmacKey(path)
            with mock.patch("orchestration_key.os.name", "posix"), mock.patch(
                "orchestration_key.stat.S_IMODE", return_value=0o640
            ):
                with self.assertRaises(LedgerUnavailable):
                    key._verify_permissions()


if __name__ == "__main__":
    unittest.main()

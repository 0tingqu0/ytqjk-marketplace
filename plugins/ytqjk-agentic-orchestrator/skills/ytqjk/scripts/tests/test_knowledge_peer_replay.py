from __future__ import annotations

import json
import multiprocessing
import os
import subprocess
import sys
import time
from pathlib import Path

import pytest


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

import knowledge_peer_replay as replay  # noqa: E402
from knowledge_peer_replay import (  # noqa: E402
    PeerReplayError,
    ReplayGuard,
)


PEER_ID = "peer-local"
NONCE = "N" * 22


def _accept_once(
    root: str,
    start: object,
    results: object,
    timestamp: int,
) -> None:
    start.wait(10)
    try:
        accepted = ReplayGuard(Path(root)).accept(
            PEER_ID,
            NONCE,
            timestamp,
        )
        results.put(accepted)
    except Exception as error:  # pragma: no cover - process transport
        results.put(f"{type(error).__name__}:{error}")


def _write_state(
    path: Path,
    entries: list[dict[str, object]],
) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps({"schema_version": 1, "entries": entries}),
        encoding="utf-8",
    )


def _entry(index: int, timestamp: int) -> dict[str, object]:
    return {
        "peer_id": PEER_ID,
        "nonce": f"{index:022d}",
        "timestamp": timestamp,
    }


def test_replay_survives_restart_and_expires(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    clock = [1_000_000]
    monkeypatch.setattr(replay.time, "time", lambda: clock[0])

    assert ReplayGuard(tmp_path).accept(PEER_ID, NONCE, clock[0])
    assert not ReplayGuard(tmp_path).accept(
        PEER_ID,
        NONCE,
        clock[0],
    )

    clock[0] += 241
    assert ReplayGuard(tmp_path).accept(PEER_ID, NONCE, clock[0])
    saved = json.loads(
        ReplayGuard(tmp_path).path.read_text(encoding="utf-8")
    )
    assert len(saved["entries"]) == 1
    assert saved["entries"][0]["timestamp"] == clock[0]


def test_concurrent_processes_accept_nonce_once(tmp_path: Path) -> None:
    context = multiprocessing.get_context("spawn")
    start = context.Event()
    results = context.Queue()
    timestamp = int(time.time())
    processes = [
        context.Process(
            target=_accept_once,
            args=(str(tmp_path), start, results, timestamp),
        )
        for _ in range(6)
    ]
    for process in processes:
        process.start()
    start.set()
    values = [results.get(timeout=20) for _ in processes]
    for process in processes:
        process.join(timeout=20)
    assert all(not process.is_alive() for process in processes)
    assert values.count(True) == 1
    assert values.count(False) == len(processes) - 1


def test_capacity_never_evicts_live_entry(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    now = 1_000_000
    monkeypatch.setattr(replay.time, "time", lambda: now)
    guard = ReplayGuard(tmp_path)
    entries = [_entry(index, now) for index in range(replay.MAX_ENTRIES)]
    _write_state(guard.path, entries)

    with pytest.raises(
        PeerReplayError,
        match="PEER_REPLAY_CAPACITY_EXHAUSTED",
    ):
        guard.accept(PEER_ID, "Z" * 22, now)
    saved = json.loads(guard.path.read_text(encoding="utf-8"))
    assert saved["entries"] == entries


def test_corrupt_state_fails_closed(tmp_path: Path) -> None:
    guard = ReplayGuard(tmp_path)
    guard.path.parent.mkdir(parents=True)
    guard.path.write_text("{}", encoding="utf-8")

    with pytest.raises(
        PeerReplayError,
        match="PEER_REPLAY_STATE_INVALID",
    ):
        guard.accept(PEER_ID, NONCE, int(time.time()))


def test_invalid_inputs_fail_closed(tmp_path: Path) -> None:
    guard = ReplayGuard(tmp_path)
    now = int(time.time())
    invalid = [
        (None, NONCE, now),
        ("bad peer", NONCE, now),
        (PEER_ID, None, now),
        (PEER_ID, "short", now),
        (PEER_ID, NONCE, True),
        (PEER_ID, NONCE, now - 241),
    ]
    for peer_id, nonce, timestamp in invalid:
        with pytest.raises(
            PeerReplayError,
            match="INVALID_PEER_REPLAY_INPUT",
        ):
            guard.accept(peer_id, nonce, timestamp)


def test_replace_io_failure_fails_closed(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    guard = ReplayGuard(tmp_path)

    def deny_replace(_source: Path, _target: Path) -> None:
        raise PermissionError("denied")

    monkeypatch.setattr(replay.os, "replace", deny_replace)
    with pytest.raises(
        PeerReplayError,
        match="PEER_REPLAY_IO_FAILED",
    ):
        guard.accept(PEER_ID, NONCE, int(time.time()))
    assert not guard.path.exists()


def test_readback_failure_fails_closed(
    tmp_path: Path,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    guard = ReplayGuard(tmp_path)
    original = replay.read_stable_bytes

    def corrupt_readback(path: Path, limit: int):
        snapshot, content = original(path, limit)
        if Path(path) == guard.path:
            return snapshot, b"{}"
        return snapshot, content

    monkeypatch.setattr(replay, "read_stable_bytes", corrupt_readback)
    with pytest.raises(
        PeerReplayError,
        match="PEER_REPLAY_READBACK_FAILED",
    ):
        guard.accept(PEER_ID, NONCE, int(time.time()))


def test_symlink_state_fails_closed(tmp_path: Path) -> None:
    guard = ReplayGuard(tmp_path / "root")
    guard.path.parent.mkdir(parents=True)
    target = tmp_path / "target.json"
    target.write_text("sentinel", encoding="utf-8")
    try:
        guard.path.symlink_to(target)
    except OSError as error:
        pytest.skip(f"symlink unavailable: {error}")

    with pytest.raises(
        PeerReplayError,
        match="UNSAFE_PEER_REPLAY_PATH",
    ):
        guard.accept(PEER_ID, NONCE, int(time.time()))
    assert target.read_text(encoding="utf-8") == "sentinel"


@pytest.mark.skipif(os.name != "nt", reason="Windows junction only")
def test_junction_service_fails_closed(tmp_path: Path) -> None:
    root = tmp_path / "root"
    root.mkdir()
    target = tmp_path / "target"
    target.mkdir()
    service = root / "service"
    created = subprocess.run(
        ["cmd.exe", "/d", "/c", "mklink", "/J", service, target],
        capture_output=True,
        check=False,
        text=True,
    )
    if created.returncode != 0:
        pytest.skip("junction creation unavailable")
    try:
        with pytest.raises(
            PeerReplayError,
            match="UNSAFE_PEER_REPLAY_PATH",
        ):
            ReplayGuard(root).accept(
                PEER_ID,
                NONCE,
                int(time.time()),
            )
    finally:
        os.rmdir(service)

"""Persistent fail-closed replay protection for LAN knowledge peers."""

from __future__ import annotations

import json
import os
import re
import stat
import tempfile
import time
from pathlib import Path

from file_lock import exclusive_file_lock
from path_safety import is_reparse
from stable_file import StableFileError, read_stable_bytes


MAX_ENTRIES = 4096
MAX_STATE_BYTES = 2 * 1024 * 1024
REPLAY_WINDOW_SECONDS = 240
SCHEMA_VERSION = 1
_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")
_NONCE = re.compile(r"^[A-Za-z0-9_-]{22,64}$")


class PeerReplayError(RuntimeError):
    """Replay state cannot be trusted or safely updated."""


class ReplayGuard:
    """Persist accepted peer nonces under ``root/service``."""

    def __init__(self, root: Path) -> None:
        self.root = Path(root).absolute()
        service = self.root / "service"
        self.path = service / "knowledge-peer-replay.json"
        self.lock_path = service / "knowledge-peer-replay.lock"

    def accept(
        self,
        peer_id: str,
        nonce: str,
        timestamp: int,
    ) -> bool:
        now = int(time.time())
        _validate_request(peer_id, nonce, timestamp, now)
        self._prepare_parent()
        try:
            with exclusive_file_lock(self.lock_path):
                _safe_chain(self.path.parent)
                entries = self._load_unlocked(now)
                cutoff = now - REPLAY_WINDOW_SECONDS
                current = [item for item in entries if item[2] >= cutoff]
                changed = len(current) != len(entries)
                duplicate = any(
                    item[:2] == (peer_id, nonce) for item in current
                )
                if duplicate:
                    if changed:
                        self._write_unlocked(current)
                    return False
                if len(current) >= MAX_ENTRIES:
                    raise PeerReplayError(
                        "PEER_REPLAY_CAPACITY_EXHAUSTED"
                    )
                current.append((peer_id, nonce, timestamp))
                self._write_unlocked(current)
                return True
        except PeerReplayError:
            raise
        except (OSError, TimeoutError, ValueError) as error:
            raise PeerReplayError("PEER_REPLAY_LOCK_FAILED") from error

    def _prepare_parent(self) -> None:
        try:
            _safe_chain(self.path.parent)
            self.path.parent.mkdir(parents=True, exist_ok=True)
            _safe_chain(self.path.parent)
            if not self.path.parent.is_dir():
                raise PeerReplayError("UNSAFE_PEER_REPLAY_PATH")
            _safe_file_if_present(self.path)
            _safe_file_if_present(self.lock_path)
        except PeerReplayError:
            raise
        except OSError as error:
            raise PeerReplayError("PEER_REPLAY_IO_FAILED") from error

    def _load_unlocked(
        self,
        now: int,
    ) -> list[tuple[str, str, int]]:
        try:
            self.path.lstat()
        except FileNotFoundError:
            return []
        except OSError as error:
            raise PeerReplayError("PEER_REPLAY_IO_FAILED") from error
        _safe_file_if_present(self.path)
        try:
            _, content = read_stable_bytes(
                self.path,
                MAX_STATE_BYTES,
            )
        except StableFileError as error:
            code = _stable_error_code(error)
            raise PeerReplayError(code) from error
        entries = _decode(content)
        if any(item[2] > now + REPLAY_WINDOW_SECONDS for item in entries):
            raise PeerReplayError("PEER_REPLAY_STATE_INVALID")
        return entries

    def _write_unlocked(
        self,
        entries: list[tuple[str, str, int]],
    ) -> None:
        content = _encode(entries)
        descriptor = -1
        temporary: Path | None = None
        try:
            descriptor, name = tempfile.mkstemp(
                dir=self.path.parent,
                prefix="knowledge-peer-replay.",
                suffix=".tmp",
            )
            temporary = Path(name)
            with os.fdopen(descriptor, "wb") as stream:
                descriptor = -1
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            _safe_chain(self.path.parent)
            _safe_file_if_present(self.path)
            _, staged = read_stable_bytes(
                temporary,
                MAX_STATE_BYTES,
            )
            if staged != content:
                raise PeerReplayError("PEER_REPLAY_READBACK_FAILED")
            os.replace(temporary, self.path)
            temporary = None
        except PeerReplayError:
            raise
        except StableFileError as error:
            code = _stable_error_code(error)
            raise PeerReplayError(code) from error
        except OSError as error:
            raise PeerReplayError("PEER_REPLAY_IO_FAILED") from error
        finally:
            if descriptor >= 0:
                os.close(descriptor)
            if temporary is not None:
                try:
                    temporary.unlink(missing_ok=True)
                except OSError:
                    pass
        self._readback(content, entries)

    def _readback(
        self,
        content: bytes,
        entries: list[tuple[str, str, int]],
    ) -> None:
        try:
            _, saved = read_stable_bytes(self.path, MAX_STATE_BYTES)
            if saved != content or _decode(saved) != entries:
                raise PeerReplayError("PEER_REPLAY_READBACK_FAILED")
        except (OSError, StableFileError, PeerReplayError) as error:
            if str(error) == "PEER_REPLAY_READBACK_FAILED":
                raise
            raise PeerReplayError(
                "PEER_REPLAY_READBACK_FAILED"
            ) from error


def _validate_request(
    peer_id: object,
    nonce: object,
    timestamp: object,
    now: int,
) -> None:
    valid = type(peer_id) is str and _IDENTIFIER.fullmatch(peer_id)
    valid = valid and type(nonce) is str and _NONCE.fullmatch(nonce)
    valid = valid and type(timestamp) is int
    valid = valid and now - REPLAY_WINDOW_SECONDS <= timestamp
    valid = valid and timestamp <= now + REPLAY_WINDOW_SECONDS
    if not valid:
        raise PeerReplayError("INVALID_PEER_REPLAY_INPUT")


def _encode(entries: list[tuple[str, str, int]]) -> bytes:
    value = {
        "schema_version": SCHEMA_VERSION,
        "entries": [
            {
                "peer_id": peer_id,
                "nonce": nonce,
                "timestamp": timestamp,
            }
            for peer_id, nonce, timestamp in entries
        ],
    }
    return json.dumps(
        value,
        ensure_ascii=True,
        allow_nan=False,
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")


def _decode(content: bytes) -> list[tuple[str, str, int]]:
    def unique(pairs: list[tuple[str, object]]) -> dict[str, object]:
        value: dict[str, object] = {}
        for key, item in pairs:
            if key in value:
                raise ValueError("duplicate field")
            value[key] = item
        return value

    try:
        value = json.loads(content.decode("utf-8"), object_pairs_hook=unique)
        if type(value) is not dict or set(value) != {
            "schema_version",
            "entries",
        }:
            raise ValueError("invalid state")
        raw_entries = value["entries"]
        if (
            type(value["schema_version"]) is not int
            or value["schema_version"] != SCHEMA_VERSION
        ):
            raise ValueError("invalid schema")
        if type(raw_entries) is not list or len(raw_entries) > MAX_ENTRIES:
            raise ValueError("invalid entries")
        entries: list[tuple[str, str, int]] = []
        keys: set[tuple[str, str]] = set()
        for item in raw_entries:
            entry = _decode_entry(item)
            if entry[:2] in keys:
                raise ValueError("duplicate replay key")
            keys.add(entry[:2])
            entries.append(entry)
        return entries
    except (UnicodeError, ValueError, TypeError, KeyError) as error:
        raise PeerReplayError("PEER_REPLAY_STATE_INVALID") from error


def _decode_entry(value: object) -> tuple[str, str, int]:
    if type(value) is not dict or set(value) != {
        "peer_id",
        "nonce",
        "timestamp",
    }:
        raise ValueError("invalid replay entry")
    peer_id = value["peer_id"]
    nonce = value["nonce"]
    timestamp = value["timestamp"]
    valid = type(peer_id) is str and _IDENTIFIER.fullmatch(peer_id)
    valid = valid and type(nonce) is str and _NONCE.fullmatch(nonce)
    valid = valid and type(timestamp) is int
    valid = valid and 0 <= timestamp <= 2**63 - 1
    if not valid:
        raise ValueError("invalid replay entry")
    return peer_id, nonce, timestamp


def _safe_chain(path: Path) -> None:
    for candidate in (path.absolute(), *path.absolute().parents):
        if is_reparse(candidate):
            raise PeerReplayError("UNSAFE_PEER_REPLAY_PATH")


def _safe_file_if_present(path: Path) -> None:
    try:
        info = path.lstat()
    except FileNotFoundError:
        return
    except OSError as error:
        raise PeerReplayError("PEER_REPLAY_IO_FAILED") from error
    attributes = getattr(info, "st_file_attributes", 0)
    reparse = getattr(stat, "FILE_ATTRIBUTE_REPARSE_POINT", 0x400)
    if (
        not stat.S_ISREG(info.st_mode)
        or info.st_nlink != 1
        or attributes & reparse
    ):
        raise PeerReplayError("UNSAFE_PEER_REPLAY_PATH")


def _stable_error_code(error: StableFileError) -> str:
    unsafe = ("SINGLE_LINK", "REPARSE", "UNSAFE_DIRECTORY")
    if any(marker in str(error) for marker in unsafe):
        return "UNSAFE_PEER_REPLAY_PATH"
    if "UNAVAILABLE" in str(error):
        return "PEER_REPLAY_IO_FAILED"
    return "PEER_REPLAY_STATE_INVALID"


__all__ = ["PeerReplayError", "ReplayGuard"]

"""Crash-safe local configuration store for LAN knowledge peers."""

from __future__ import annotations

import os
import secrets
import tempfile
from pathlib import Path

from file_lock import exclusive_file_lock
from knowledge_peer_codec import (
    PeerSettings,
    PeerStoreError,
    SCHEMA_VERSION,
    decode_settings,
    encode_settings,
    validate_local,
)
from knowledge_peer_contract import PeerRecord
from path_safety import is_reparse
from stable_file import StableFileError, read_stable_bytes


MAX_CONFIG_BYTES = 1024 * 1024


class PeerConfigStore:
    def __init__(self, root: Path) -> None:
        self.root = Path(root).absolute()
        self.path = self.root / "service" / "knowledge-peers.json"
        self.lock_path = self.path.with_suffix(".lock")

    def load(self, *, create: bool = False) -> PeerSettings:
        with self._locked():
            current = self._load_unlocked()
            if current is None and create:
                current = PeerSettings(
                    0,
                    "peer-" + secrets.token_hex(8),
                    False,
                    "127.0.0.1",
                    8766,
                    False,
                    (),
                )
                self._write_unlocked(current)
            if current is None:
                raise PeerStoreError("PEER_CONFIG_NOT_CONFIGURED")
            return current

    def configure_local(
        self,
        *,
        enabled: bool,
        bind_host: str,
        port: int,
        allow_insecure_lan: bool,
        expected_revision: int,
    ) -> PeerSettings:
        with self._locked():
            current = self._required_unlocked(expected_revision)
            validate_local(enabled, bind_host, port, allow_insecure_lan)
            changed = PeerSettings(
                current.revision + 1,
                current.local_peer_id,
                enabled,
                bind_host,
                port,
                allow_insecure_lan,
                current.peers,
            )
            return self._write_unlocked(changed)

    def upsert(
        self,
        record: PeerRecord,
        *,
        expected_revision: int,
    ) -> PeerSettings:
        if type(record) is not PeerRecord:
            raise PeerStoreError("INVALID_PEER_RECORD")
        with self._locked():
            current = self._required_unlocked(expected_revision)
            peers = {
                item.peer_id: item for item in current.peers
            }
            if record.peer_id == current.local_peer_id:
                raise PeerStoreError("SELF_PEER_FORBIDDEN")
            peers[record.peer_id] = record
            changed = PeerSettings(
                current.revision + 1,
                current.local_peer_id,
                current.enabled,
                current.bind_host,
                current.port,
                current.allow_insecure_lan,
                tuple(peers[key] for key in sorted(peers)),
            )
            return self._write_unlocked(changed)

    def remove(
        self,
        peer_id: str,
        *,
        expected_revision: int,
    ) -> PeerSettings:
        with self._locked():
            current = self._required_unlocked(expected_revision)
            if current.peer(peer_id) is None:
                raise PeerStoreError("UNKNOWN_PEER")
            peers = tuple(
                item for item in current.peers
                if item.peer_id != peer_id
            )
            changed = PeerSettings(
                current.revision + 1,
                current.local_peer_id,
                current.enabled,
                current.bind_host,
                current.port,
                current.allow_insecure_lan,
                peers,
            )
            return self._write_unlocked(changed)

    def _required_unlocked(self, revision: int) -> PeerSettings:
        current = self._load_unlocked()
        if current is None:
            raise PeerStoreError("PEER_CONFIG_NOT_CONFIGURED")
        if type(revision) is not int or revision != current.revision:
            raise PeerStoreError("PEER_REVISION_CONFLICT")
        return current

    def _locked(self):
        self._prepare_parent()
        return exclusive_file_lock(self.lock_path)

    def _prepare_parent(self) -> None:
        _safe_chain(self.path.parent)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        _safe_chain(self.path.parent)
        if not self.path.parent.is_dir():
            raise PeerStoreError("PEER_DIRECTORY_UNSAFE")

    def _load_unlocked(self) -> PeerSettings | None:
        if not self.path.exists():
            return None
        try:
            _, content = read_stable_bytes(self.path, MAX_CONFIG_BYTES)
            return decode_settings(content)
        except (
            OSError,
            StableFileError,
            PeerStoreError,
        ) as error:
            raise PeerStoreError("PEER_CONFIG_INVALID") from error

    def _write_unlocked(self, settings: PeerSettings) -> PeerSettings:
        content = encode_settings(settings)
        try:
            original = self.path.read_bytes() if self.path.exists() else None
        except OSError as error:
            raise PeerStoreError("PEER_CONFIG_WRITE_FAILED") from error
        self._replace_bytes(content)
        try:
            readback = self._load_unlocked()
            if readback != settings:
                raise PeerStoreError("PEER_CONFIG_READBACK_FAILED")
            return readback
        except Exception as error:
            self._rollback(original)
            raise PeerStoreError("PEER_CONFIG_READBACK_FAILED") from error

    def _replace_bytes(self, content: bytes) -> None:
        descriptor, name = tempfile.mkstemp(
            dir=self.path.parent,
            prefix="knowledge-peers.",
            suffix=".tmp",
        )
        temporary = Path(name)
        try:
            with os.fdopen(descriptor, "wb") as stream:
                stream.write(content)
                stream.flush()
                os.fsync(stream.fileno())
            os.replace(temporary, self.path)
        except OSError as error:
            raise PeerStoreError("PEER_CONFIG_WRITE_FAILED") from error
        finally:
            temporary.unlink(missing_ok=True)

    def _rollback(self, original: bytes | None) -> None:
        try:
            if original is None:
                self.path.unlink(missing_ok=True)
            else:
                self._replace_bytes(original)
            restored = self.path.read_bytes() if self.path.exists() else None
            if restored != original:
                raise OSError("peer config rollback mismatch")
        except Exception as error:
            raise PeerStoreError("PEER_CONFIG_ROLLBACK_FAILED") from error

def _safe_chain(path: Path) -> None:
    for candidate in (path.absolute(), *path.absolute().parents):
        if candidate.exists() and is_reparse(candidate):
            raise PeerStoreError("UNSAFE_PEER_CONFIG_PATH")


__all__ = [
    "PeerConfigStore",
    "PeerSettings",
    "PeerStoreError",
]

"""Safe storage and readback for local group knowledge indexes."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import uuid
from pathlib import Path
from typing import Any

from global_store import approved_document_id, is_current_approved_hit
from path_safety import is_direct_directory, is_reparse
from rag_common import (
    SCHEMA_VERSION,
    Chunk,
    config_fingerprint,
    load_json,
    read_chunks,
    utc_now,
)


_IDENTIFIER = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


class GroupMaterializationError(RuntimeError):
    def __init__(self, status: int, code: str) -> None:
        super().__init__(code)
        self.status = status
        self.code = code


class GroupIndexStorage:
    def __init__(self, knowledge_root: Path) -> None:
        self.root = Path(knowledge_root).absolute()

    def locations(self, node_id: str) -> tuple[Path, Path]:
        if _IDENTIFIER.fullmatch(node_id) is None:
            raise GroupMaterializationError(400, "INVALID_NODE_ID")
        require_safe_directory(self.root)
        libraries = self.root / "libraries"
        libraries.mkdir(exist_ok=True)
        require_safe_directory(libraries, self.root)
        staging = libraries / ".staging"
        staging.mkdir(exist_ok=True)
        require_safe_directory(staging, libraries)
        active = libraries / node_id
        if active.exists() and (
            is_reparse(active)
            or not is_direct_directory(active, libraries)
        ):
            raise GroupMaterializationError(
                503, "UNSAFE_GROUP_INDEX_PATH"
            )
        return active, staging

    def status(self, node_id: str) -> dict[str, object]:
        try:
            if _IDENTIFIER.fullmatch(node_id) is None:
                return {"status": "CORRUPT"}
            require_safe_directory(self.root)
            libraries = self.root / "libraries"
            if not libraries.exists():
                return {"status": "NOT_CONFIGURED"}
            require_safe_directory(libraries, self.root)
            active = libraries / node_id
            if not active.exists():
                return {"status": "NOT_CONFIGURED"}
            if not is_direct_directory(active, libraries):
                return {"status": "CORRUPT"}
            manifest = self.readback(active, verify_sources=False)
            ready = self.sources_current(active)
            return {
                "status": "READY" if ready else "STALE",
                "generation": manifest["generation"],
                "documents": len(manifest["documents"]),
                "chunks": manifest["stats"]["chunks"],
                "indexed_at": manifest["indexed_at"],
            }
        except Exception:
            return {"status": "CORRUPT"}

    def readback(
        self,
        directory: Path,
        verify_sources: bool,
    ) -> dict[str, Any]:
        manifest = load_json(directory / "manifest.json", {})
        if (
            type(manifest) is not dict
            or manifest.get("schema_version") != SCHEMA_VERSION
            or type(manifest.get("documents")) is not list
        ):
            raise GroupMaterializationError(
                503, "GROUP_INDEX_MANIFEST_INVALID"
            )
        stats = manifest.get("stats")
        if type(stats) is not dict:
            raise GroupMaterializationError(
                503, "GROUP_INDEX_MANIFEST_INVALID"
            )
        chunks = read_chunks(directory / "lexical.sqlite3")
        digest = membership_digest(chunks)
        if (
            manifest.get("membership_digest") != digest
            or manifest.get("generation") != digest
            or manifest.get("source_fingerprint") != digest
            or stats.get("chunks") != len(chunks)
        ):
            raise GroupMaterializationError(
                503, "GROUP_INDEX_MEMBERSHIP_MISMATCH"
            )
        if manifest.get("documents") != documents(chunks):
            raise GroupMaterializationError(
                503, "GROUP_INDEX_PROVENANCE_MISMATCH"
            )
        if verify_sources and not all(
            is_current_approved_hit(self.root, chunk_row(chunk))
            for chunk in chunks
        ):
            raise GroupMaterializationError(
                409, "SOURCE_CHANGED_DURING_BUILD"
            )
        return manifest

    def sources_current(self, directory: Path) -> bool:
        try:
            return all(
                is_current_approved_hit(self.root, chunk_row(chunk))
                for chunk in read_chunks(
                    directory / "lexical.sqlite3"
                )
            )
        except Exception:
            return False

    def switch(
        self,
        active: Path,
        stage: Path,
        staging: Path,
    ) -> None:
        token = uuid.uuid4().hex
        backup = staging / f"backup-{active.name}-{token}"
        had_active = active.exists()
        try:
            if had_active:
                os.replace(active, backup)
            os.replace(stage, active)
            self.readback(active, verify_sources=True)
            if backup.exists():
                remove_tree(backup, staging)
        except BaseException:
            if active.exists():
                remove_tree(active, active.parent)
            if backup.exists():
                os.replace(backup, active)
            raise


def build_manifest(
    chunks: list[Chunk],
    source_stats: dict[str, int],
    generation: str,
    config: dict[str, Any],
) -> dict[str, object]:
    provenance = documents(chunks)
    return {
        "schema_version": SCHEMA_VERSION,
        "generation": generation,
        "source_fingerprint": generation,
        "membership_digest": generation,
        "config_fingerprint": config_fingerprint(config),
        "vector_mode": "off",
        "vector": {
            "enabled": False,
            "status": "DISABLED",
            "error": None,
        },
        "documents": provenance,
        "stats": {
            "files": len(provenance),
            "skipped": source_stats.get("skipped", 0),
            "text_bytes": sum(
                len(chunk.content.encode("utf-8"))
                for chunk in chunks
            ),
            "chunks": len(chunks),
        },
        "indexed_at": utc_now(),
    }


def documents(chunks: list[Chunk]) -> list[dict[str, str]]:
    found = {
        chunk.path: {
            "document_id": approved_document_id(chunk.path),
            "path": chunk.path,
            "source_sha256": chunk.source_sha256,
        }
        for chunk in chunks
    }
    return [found[path] for path in sorted(found)]


def membership_digest(chunks: list[Chunk]) -> str:
    rows = [
        {
            "content": chunk.content,
            "id": chunk.id,
            "line_end": chunk.line_end,
            "line_start": chunk.line_start,
            "path": chunk.path,
            "source_sha256": chunk.source_sha256,
        }
        for chunk in sorted(chunks, key=lambda item: item.id)
    ]
    payload = json.dumps(
        rows,
        ensure_ascii=False,
        sort_keys=True,
        separators=(",", ":"),
    ).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()


def chunk_row(chunk: Chunk) -> dict[str, object]:
    return {
        "path": chunk.path,
        "line_start": chunk.line_start,
        "line_end": chunk.line_end,
        "content": chunk.content,
        "source_sha256": chunk.source_sha256,
    }


def require_safe_directory(
    path: Path,
    parent: Path | None = None,
) -> None:
    if not path.is_dir() or is_reparse(path):
        raise GroupMaterializationError(
            503, "UNSAFE_GROUP_INDEX_PATH"
        )
    if parent is not None and not is_direct_directory(path, parent):
        raise GroupMaterializationError(
            503, "UNSAFE_GROUP_INDEX_PATH"
        )


def remove_tree(path: Path, parent: Path) -> None:
    if not path.exists():
        return
    if is_reparse(path) or path.resolve().parent != parent.resolve():
        raise GroupMaterializationError(
            503, "UNSAFE_GROUP_INDEX_PATH"
        )
    shutil.rmtree(path)


__all__ = [
    "GroupIndexStorage",
    "GroupMaterializationError",
    "build_manifest",
    "membership_digest",
    "remove_tree",
]

"""Build governed local indexes for knowledge-tree group nodes."""

from __future__ import annotations

import argparse
import json
import re
import uuid
from collections.abc import Callable
from pathlib import Path

from file_lock import exclusive_file_lock
from global_store import approved_document_id, scan_global
from knowledge_group_index import (
    GroupIndexStorage,
    GroupMaterializationError,
    build_manifest,
    membership_digest,
    remove_tree,
)
from knowledge_tree_store import KnowledgeTreeStore
from platform_paths import default_knowledge_root
from rag_common import (
    DEFAULT_CONFIG,
    Chunk,
    atomic_json,
    build_lexical,
    load_json,
)
from rag_locks import project_id_lock


_SHA256 = re.compile(r"^[0-9a-f]{64}$")
_NODE_ID = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$")


class GroupLibraryService:
    def __init__(
        self,
        knowledge_root: Path,
        store: KnowledgeTreeStore | None = None,
        before_switch: Callable[[], None] | None = None,
    ) -> None:
        self.root = Path(knowledge_root).absolute()
        self.store = store or KnowledgeTreeStore(
            self.root / "tree.json"
        )
        self.storage = GroupIndexStorage(self.root)
        self.before_switch = before_switch

    def status(self, node_id: str) -> dict[str, object]:
        if (
            type(node_id) is not str
            or _NODE_ID.fullmatch(node_id) is None
        ):
            return {"status": "CORRUPT"}
        current = self.storage.status(node_id)
        if current.get("status") == "NOT_CONFIGURED":
            return current
        lock = project_id_lock(
            self.root, f"library-{node_id}"
        )
        with exclusive_file_lock(lock):
            return self.storage.status(node_id)

    def rebuild(
        self,
        node_id: str,
        expected_revision: int,
        document_ids: list[str] | None = None,
    ) -> dict[str, object]:
        selected = self._validate_selection(document_ids)
        with self.store.read_transaction() as tree:
            if tree.revision != expected_revision:
                raise GroupMaterializationError(
                    409, "REVISION_CONFLICT"
                )
            node = next(
                (item for item in tree.nodes if item.node_id == node_id),
                None,
            )
            if node is None:
                raise GroupMaterializationError(404, "UNKNOWN_NODE")
            if node.kind != "group":
                raise GroupMaterializationError(
                    409, "GROUP_NODE_REQUIRED"
                )
            lock = project_id_lock(
                self.root, f"library-{node_id}"
            )
            with exclusive_file_lock(lock):
                return self._rebuild_locked(
                    node_id, tree.revision, selected
                )

    def _rebuild_locked(
        self,
        node_id: str,
        revision: int,
        selected: set[str] | None,
    ) -> dict[str, object]:
        active, staging = self.storage.locations(node_id)
        config = load_json(self.root / "config.json", DEFAULT_CONFIG)
        chunks, source_stats = scan_global(self.root, config)
        chunks = self._select_chunks(chunks, selected)
        generation = membership_digest(chunks)
        current = self.storage.status(node_id)
        if (
            current.get("status") == "READY"
            and current.get("generation") == generation
        ):
            return self._receipt(
                node_id, revision, "REUSED", current
            )
        stage = staging / f"{node_id}-{uuid.uuid4().hex}"
        try:
            stage.mkdir()
            build_lexical(stage / "lexical.sqlite3", chunks)
            manifest = build_manifest(
                chunks, source_stats, generation, config
            )
            atomic_json(stage / "manifest.json", manifest)
            self.storage.readback(stage, verify_sources=True)
            if self.before_switch is not None:
                self.before_switch()
            self.storage.readback(stage, verify_sources=True)
            self.storage.switch(active, stage, staging)
            result = self.storage.status(node_id)
            if result.get("status") != "READY":
                raise GroupMaterializationError(
                    503, "ACTIVE_INDEX_READBACK_FAILED"
                )
            return self._receipt(
                node_id, revision, "REBUILT", result
            )
        except GroupMaterializationError:
            raise
        except Exception as error:
            raise GroupMaterializationError(
                503, "GROUP_INDEX_BUILD_FAILED"
            ) from error
        finally:
            if stage.exists():
                remove_tree(stage, staging)

    @staticmethod
    def _validate_selection(
        values: list[str] | None,
    ) -> set[str] | None:
        if values is None or values == []:
            return None
        if type(values) is not list or any(
            type(value) is not str
            or _SHA256.fullmatch(value) is None
            for value in values
        ):
            raise GroupMaterializationError(
                400, "INVALID_DOCUMENT_IDS"
            )
        if len(values) != len(set(values)):
            raise GroupMaterializationError(
                400, "DUPLICATE_DOCUMENT_ID"
            )
        return set(values)

    @staticmethod
    def _select_chunks(
        chunks: list[Chunk],
        selected: set[str] | None,
    ) -> list[Chunk]:
        known = {
            approved_document_id(chunk.path) for chunk in chunks
        }
        if selected is not None and not selected <= known:
            raise GroupMaterializationError(
                400, "UNKNOWN_DOCUMENT_ID"
            )
        if selected is None:
            return chunks
        return [
            chunk for chunk in chunks
            if approved_document_id(chunk.path) in selected
        ]

    @staticmethod
    def _receipt(
        node_id: str,
        revision: int,
        status: str,
        index: dict[str, object],
    ) -> dict[str, object]:
        return {
            "ok": True,
            "status": status,
            "node_id": node_id,
            "tree_revision": revision,
            "generation": index.get("generation"),
            "documents": index.get("documents", 0),
            "chunks": index.get("chunks", 0),
            "indexed_at": index.get("indexed_at"),
            "source_scope": "approved-verified-only",
        }


def main() -> int:
    parser = argparse.ArgumentParser(
        description="Rebuild a governed local group knowledge index."
    )
    parser.add_argument(
        "--knowledge-root",
        type=Path,
        default=default_knowledge_root(),
    )
    parser.add_argument("--node-id", required=True)
    parser.add_argument(
        "--expected-revision",
        type=int,
        required=True,
    )
    parser.add_argument("--document-id", action="append")
    args = parser.parse_args()
    try:
        result = GroupLibraryService(args.knowledge_root).rebuild(
            args.node_id,
            args.expected_revision,
            args.document_id,
        )
    except GroupMaterializationError as error:
        result = {
            "ok": False,
            "status": error.status,
            "error": error.code,
        }
        print(json.dumps(result, ensure_ascii=False))
        return 1
    print(json.dumps(result, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())


__all__ = [
    "GroupLibraryService",
    "GroupMaterializationError",
]

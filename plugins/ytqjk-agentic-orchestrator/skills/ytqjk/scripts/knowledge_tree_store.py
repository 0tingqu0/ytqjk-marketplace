from __future__ import annotations

import json
import os
import tempfile
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator, Mapping

from file_lock import exclusive_file_lock
from knowledge_tree import KnowledgeTree, LibraryNode, MAX_REVISION
from knowledge_tree import RevisionConflict
from knowledge_tree_codec import (
    SCHEMA_VERSION,
    TreeStoreError,
    _decode_json,
    _digest,
    _tree_body,
    _tree_from_payload,
)
from path_safety import is_reparse
from stable_file import StableFileError, read_stable_bytes


LEGACY_PERSONAL_ROOT_TITLE = "总知识库"
PERSONAL_ROOT_TITLE = "个人总库"


class KnowledgeTreeStore:
    def __init__(self, path: Path) -> None:
        self.path = Path(path).absolute()
        self.lock_path = self.path.with_name(self.path.name + ".lock")

    def load(self) -> KnowledgeTree:
        with self.read_transaction() as tree:
            return tree

    @contextmanager
    def read_transaction(self) -> Iterator[KnowledgeTree]:
        with self._locked():
            tree = self._load_unlocked()
            if tree is None:
                raise FileNotFoundError("KNOWLEDGE_TREE_NOT_INITIALIZED")
            yield tree

    def save(
        self, tree: KnowledgeTree, *, expected_revision: int,
    ) -> KnowledgeTree:
        if type(expected_revision) is not int:
            raise RevisionConflict("INVALID_EXPECTED_REVISION")
        with self._locked():
            current = self._load_unlocked()
            current_revision = -1 if current is None else current.revision
            if current_revision != expected_revision:
                raise RevisionConflict("REVISION_CONFLICT")
            if type(tree) is not KnowledgeTree:
                raise TreeStoreError("INVALID_TREE_TYPE")
            if tree.revision != expected_revision + 1:
                raise RevisionConflict("INVALID_NEXT_REVISION")
            return self._write_unlocked(tree)

    def bootstrap(
        self, catalog: Path | Mapping[str, object],
    ) -> KnowledgeTree:
        projects = self._catalog_projects(catalog)
        with self._locked():
            current = self._load_unlocked()
            nodes = [] if current is None else list(current.nodes)
            edges = [] if current is None else list(current.edges)
            by_id = {node.node_id: node for node in nodes}
            changed = current is None
            if "global" not in by_id:
                node = LibraryNode(
                    "global", PERSONAL_ROOT_TITLE, "global"
                )
                nodes.append(node)
                by_id[node.node_id] = node
                changed = True
            elif by_id["global"].kind != "global":
                raise TreeStoreError("GLOBAL_NODE_CONFLICT")
            elif by_id["global"].title == LEGACY_PERSONAL_ROOT_TITLE:
                node = LibraryNode(
                    "global", PERSONAL_ROOT_TITLE, "global"
                )
                nodes = [
                    node if item.node_id == "global" else item
                    for item in nodes
                ]
                by_id[node.node_id] = node
                changed = True
            for node in projects:
                existing = by_id.get(node.node_id)
                if existing is None:
                    nodes.append(node)
                    by_id[node.node_id] = node
                    edges.append(("global", node.node_id))
                    changed = True
                elif existing.kind != "project":
                    raise TreeStoreError("CATALOG_NODE_CONFLICT")
                elif existing.title != node.title:
                    nodes = [
                        node if item.node_id == node.node_id else item
                        for item in nodes
                    ]
                    by_id[node.node_id] = node
                    changed = True
            revision = 0 if current is None else current.revision
            if current is not None and changed:
                if revision == MAX_REVISION:
                    raise RevisionConflict("REVISION_EXHAUSTED")
                revision += 1
            tree = KnowledgeTree(nodes, edges, revision=revision)
            if changed:
                return self._write_unlocked(tree)
            return tree

    def _catalog_projects(
        self, catalog: Path | Mapping[str, object],
    ) -> tuple[LibraryNode, ...]:
        if isinstance(catalog, Path):
            try:
                _, content = read_stable_bytes(catalog, 16 * 1024 * 1024)
                value = _decode_json(content)
            except StableFileError as error:
                raise TreeStoreError("CATALOG_UNAVAILABLE") from error
        elif type(catalog) is dict:
            value = catalog
        else:
            raise TreeStoreError("INVALID_CATALOG")
        if type(value) is not dict or type(value.get("projects")) is not dict:
            raise TreeStoreError("INVALID_CATALOG")
        projects = value["projects"]
        if any(type(project_id) is not str for project_id in projects):
            raise TreeStoreError("INVALID_CATALOG_PROJECT")
        result: list[LibraryNode] = []
        for project_id, record in sorted(projects.items()):
            if type(record) is not dict:
                raise TreeStoreError("INVALID_CATALOG_PROJECT")
            title = record.get("name", project_id)
            try:
                node = LibraryNode(project_id, title, "project")
            except (TypeError, ValueError) as error:
                raise TreeStoreError("INVALID_CATALOG_PROJECT") from error
            if node.node_id == "global":
                raise TreeStoreError("CATALOG_NODE_CONFLICT")
            result.append(node)
        return tuple(result)

    @contextmanager
    def _locked(self) -> Iterator[None]:
        self._prepare_parent()
        self._require_safe_target(self.lock_path)
        with exclusive_file_lock(self.lock_path):
            self._require_safe_target(self.lock_path)
            self._require_safe_target(self.path)
            yield

    def _prepare_parent(self) -> None:
        self._require_no_reparse(self.path.parent)
        try:
            self.path.parent.mkdir(parents=True, exist_ok=True)
        except OSError as error:
            raise TreeStoreError("TREE_DIRECTORY_UNAVAILABLE") from error
        self._require_no_reparse(self.path.parent)
        if not self.path.parent.is_dir():
            raise TreeStoreError("TREE_DIRECTORY_UNSAFE")

    def _require_safe_existing(self, path: Path) -> None:
        self._require_no_reparse(path)
        if not path.is_file() or is_reparse(path):
            raise TreeStoreError("UNSAFE_REPARSE_PATH")

    def _require_safe_target(self, path: Path) -> None:
        self._require_no_reparse(path)
        if path.exists() and (not path.is_file() or is_reparse(path)):
            raise TreeStoreError("UNSAFE_REPARSE_PATH")

    @staticmethod
    def _require_no_reparse(path: Path) -> None:
        absolute = path.absolute()
        for candidate in (absolute, *absolute.parents):
            if candidate.exists() and is_reparse(candidate):
                raise TreeStoreError("UNSAFE_REPARSE_PATH")

    def _load_unlocked(self) -> KnowledgeTree | None:
        if not self.path.exists():
            return None
        try:
            _, content = read_stable_bytes(
                self.path,
                16 * 1024 * 1024,
            )
            return _tree_from_payload(_decode_json(content))
        except StableFileError as error:
            raise TreeStoreError("TREE_UNAVAILABLE") from error

    def _require_readback(self) -> KnowledgeTree:
        tree = self._load_unlocked()
        if tree is None:
            raise TreeStoreError("TREE_READBACK_FAILED")
        return tree

    def _write_unlocked(self, tree: KnowledgeTree) -> KnowledgeTree:
        body = _tree_body(tree)
        payload = dict(body)
        payload["digest"] = _digest(body)
        content = json.dumps(
            payload, ensure_ascii=False, allow_nan=False,
            indent=2, sort_keys=True,
        ).encode("utf-8") + b"\n"
        try:
            original = self.path.read_bytes() if self.path.exists() else None
        except OSError as error:
            raise TreeStoreError("TREE_UNAVAILABLE") from error
        self._replace_bytes(content)
        try:
            self._require_safe_existing(self.path)
            readback = self._require_readback()
            if _tree_body(readback) != body:
                raise TreeStoreError("TREE_READBACK_MISMATCH")
            return readback
        except Exception as error:
            self._rollback(original)
            raise TreeStoreError("TREE_READBACK_FAILED") from error

    def _replace_bytes(self, content: bytes) -> None:
        descriptor, name = tempfile.mkstemp(
            dir=self.path.parent, prefix=self.path.name + ".", suffix=".tmp",
        )
        temporary = Path(name)
        try:
            with os.fdopen(descriptor, "wb") as handle:
                handle.write(content)
                handle.flush()
                os.fsync(handle.fileno())
            os.replace(temporary, self.path)
        except OSError as error:
            raise TreeStoreError("TREE_WRITE_FAILED") from error
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
                raise OSError("rollback content mismatch")
            if restored is not None:
                _tree_from_payload(_decode_json(restored))
        except Exception as error:
            raise TreeStoreError("TREE_ROLLBACK_FAILED") from error

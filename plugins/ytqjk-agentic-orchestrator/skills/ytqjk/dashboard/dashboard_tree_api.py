"""HTTP-neutral, preview-gated knowledge-tree dashboard API."""

from __future__ import annotations

import json
import secrets
import threading
from dataclasses import dataclass

from dashboard_tree_operations import (
    DashboardTreeApiError,
    affected_nodes,
    apply,
    plan,
    tree_error,
    validate_action,
    validate_arguments,
    validate_commit,
)
from dashboard_tree_serialization import (
    canonical_digest,
    tree_payload,
)
from knowledge_tree import (
    KnowledgeTree,
    PreviewMismatch,
    RevisionConflict,
    TreeError,
)
from knowledge_group_materializer import (
    GroupLibraryService,
    GroupMaterializationError,
)
from knowledge_tree_store import KnowledgeTreeStore, TreeStoreError


_MAX_PREVIEWS = 256


@dataclass(frozen=True)
class _IssuedPreview:
    action: str
    expected_revision: int
    base_digest: str
    request_json: str


class DashboardTreeApi:
    def __init__(
        self,
        store: KnowledgeTreeStore,
        group_service: GroupLibraryService | None = None,
    ) -> None:
        if not isinstance(store, KnowledgeTreeStore):
            raise TypeError("INVALID_TREE_STORE")
        self.store = store
        self.group_service = group_service or GroupLibraryService(
            store.path.parent, store
        )
        self._issued: dict[str, _IssuedPreview] = {}
        self._lock = threading.Lock()

    def snapshot(self) -> dict[str, object]:
        tree = self._load()
        return {"ok": True, "tree": self._response_tree(tree)}

    def preview(
        self,
        action: str,
        payload: object,
    ) -> dict[str, object]:
        action = validate_action(action)
        arguments = validate_arguments(action, payload)
        tree = self._load()
        try:
            summary = plan(tree, action, arguments)
            affected = affected_nodes(tree, action, arguments)
        except (TreeError, RevisionConflict) as error:
            raise tree_error(error) from error
        base = tree_payload(tree)["digest"]
        request_json = _canonical_json(arguments)
        digest = canonical_digest({
            "action": action,
            "base": base,
            "nonce": secrets.token_hex(16),
            "request": request_json,
        })
        record = _IssuedPreview(
            action, tree.revision, str(base), request_json,
        )
        with self._lock:
            if len(self._issued) >= _MAX_PREVIEWS:
                self._issued.pop(next(iter(self._issued)))
            self._issued[digest] = record
        return {
            "ok": True,
            "preview": {
                "action": action,
                "expected_revision": tree.revision,
                "digest": digest,
                "summary": summary,
                "affected_nodes": affected,
            },
        }

    def commit(
        self,
        action: str,
        payload: object,
    ) -> dict[str, object]:
        action = validate_action(action)
        digest, revision = validate_commit(payload)
        with self._lock:
            record = self._issued.pop(digest, None)
        if record is None:
            raise DashboardTreeApiError(409, "PREVIEW_NOT_FOUND")
        if record.action != action or record.expected_revision != revision:
            raise DashboardTreeApiError(409, "PREVIEW_MISMATCH")
        tree = self._load()
        current_digest = tree_payload(tree)["digest"]
        if tree.revision != revision:
            raise DashboardTreeApiError(409, "REVISION_CONFLICT")
        if current_digest != record.base_digest:
            raise DashboardTreeApiError(409, "TOPOLOGY_CHANGED")
        arguments = json.loads(record.request_json)
        if action == "rebuild_index":
            return self._materialize(tree, arguments, revision)
        try:
            changed = apply(tree, action, arguments)
            saved = self.store.save(changed, expected_revision=revision)
        except (TreeError, RevisionConflict, PreviewMismatch) as error:
            raise tree_error(error) from error
        except TreeStoreError as error:
            raise DashboardTreeApiError(503, str(error)) from error
        return {
            "ok": True,
            "action": action,
            "revision": saved.revision,
            "tree": self._response_tree(saved),
        }

    def _materialize(
        self,
        tree: KnowledgeTree,
        arguments: dict[str, object],
        revision: int,
    ) -> dict[str, object]:
        documents = arguments["document_ids"]
        if type(documents) is not list:
            raise DashboardTreeApiError(
                400, "INVALID_DOCUMENT_IDS"
            )
        try:
            receipt = self.group_service.rebuild(
                str(arguments["node_id"]),
                revision,
                documents,
            )
        except GroupMaterializationError as error:
            raise DashboardTreeApiError(
                error.status, error.code
            ) from error
        current = self._load()
        return {
            "ok": True,
            "action": "rebuild_index",
            "revision": current.revision,
            "tree": self._response_tree(current),
            "materialization": receipt,
        }

    def _response_tree(
        self,
        tree: KnowledgeTree,
    ) -> dict[str, object]:
        payload = tree_payload(tree)
        nodes = payload["nodes"]
        if type(nodes) is list:
            for node in nodes:
                if type(node) is dict and node.get("type") == "group":
                    node["index"] = self.group_service.status(
                        str(node["id"])
                    )
        return payload

    def _load(self) -> KnowledgeTree:
        try:
            return self.store.load()
        except FileNotFoundError as error:
            raise DashboardTreeApiError(
                503, "TREE_NOT_CONFIGURED",
            ) from error
        except TreeStoreError as error:
            raise DashboardTreeApiError(503, str(error)) from error

def _canonical_json(value: object) -> str:
    return json.dumps(
        value, ensure_ascii=False, allow_nan=False,
        separators=(",", ":"), sort_keys=True,
    )


__all__ = ["DashboardTreeApi", "DashboardTreeApiError"]

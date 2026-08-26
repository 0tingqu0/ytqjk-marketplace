from __future__ import annotations

import hashlib
import hmac
import json
import math

from knowledge_tree import KnowledgeTree, LibraryNode, MAX_REVISION


SCHEMA_VERSION = 1


class TreeStoreError(ValueError): ...


def _reject_constant(_: str) -> None:
    raise TreeStoreError("INVALID_JSON_NUMBER")


def _json_number(value: str) -> int | float:
    is_int = "." not in value and "e" not in value.casefold()
    if is_int and len(value.lstrip("-")) > 19:
        raise TreeStoreError("JSON_INTEGER_OUT_OF_RANGE")
    number = int(value) if is_int else float(value)
    if abs(number) > MAX_REVISION if is_int else not math.isfinite(number):
        raise TreeStoreError("INVALID_JSON_NUMBER")
    return number


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result: dict[str, object] = {}
    for key, value in pairs:
        if type(key) is not str or key in result:
            raise TreeStoreError("DUPLICATE_JSON_KEY")
        result[key] = value
    return result


def _decode_json(content: bytes) -> object:
    try:
        text = content.decode("utf-8", errors="strict")
    except UnicodeDecodeError as error:
        raise TreeStoreError("JSON_NOT_UTF8") from error
    if text.startswith("\ufeff"):
        raise TreeStoreError("UTF8_BOM_FORBIDDEN")
    try:
        return json.loads(
            text, object_pairs_hook=_unique_object,
            parse_constant=_reject_constant, parse_float=_json_number,
            parse_int=_json_number,
        )
    except TreeStoreError:
        raise
    except (json.JSONDecodeError, ValueError) as error:
        raise TreeStoreError("INVALID_JSON") from error


def _canonical(value: object) -> bytes:
    return json.dumps(value, ensure_ascii=False, allow_nan=False,
                      separators=(",", ":"), sort_keys=True).encode("utf-8")


def _digest(value: object) -> str:
    return hashlib.sha256(_canonical(value)).hexdigest()


def _node_payload(node: LibraryNode) -> dict[str, object]:
    return dict(
        capability=node.capability, kind=node.kind, mount_id=node.mount_id,
        node_id=node.node_id, title=node.title,
    )


def _tree_body(tree: KnowledgeTree) -> dict[str, object]:
    if type(tree) is not KnowledgeTree:
        raise TreeStoreError("INVALID_TREE_TYPE")
    return dict(
        edges=[list(edge) for edge in tree.edges],
        nodes=[_node_payload(node) for node in tree.nodes],
        revision=tree.revision, schema_version=SCHEMA_VERSION,
    )


def _node_from_payload(value: object) -> LibraryNode:
    if type(value) is not dict:
        raise TreeStoreError("INVALID_NODE_RECORD")
    required = {"capability", "kind", "mount_id", "node_id", "title"}
    if set(value) != required:
        raise TreeStoreError("INVALID_NODE_RECORD")
    try:
        keys = ("node_id", "title", "kind", "mount_id", "capability")
        return LibraryNode(*(value[key] for key in keys))
    except (TypeError, ValueError) as error:
        raise TreeStoreError("INVALID_NODE_RECORD") from error


def _tree_from_payload(value: object) -> KnowledgeTree:
    if type(value) is not dict:
        raise TreeStoreError("INVALID_TREE_DOCUMENT")
    required = {"digest", "edges", "nodes", "revision", "schema_version"}
    schema = value.get("schema_version")
    valid_schema = type(schema) is int and schema == SCHEMA_VERSION
    if set(value) != required or not valid_schema:
        raise TreeStoreError("INVALID_TREE_DOCUMENT")
    digest = value["digest"]
    valid_digest = type(digest) is str and len(digest) == 64
    if not valid_digest or set(digest) - set("0123456789abcdef"):
        raise TreeStoreError("INVALID_TREE_DIGEST")
    body = {key: value[key] for key in required if key != "digest"}
    if not hmac.compare_digest(digest, _digest(body)):
        raise TreeStoreError("TREE_DIGEST_MISMATCH")
    if type(value["nodes"]) is not list or type(value["edges"]) is not list:
        raise TreeStoreError("INVALID_TREE_DOCUMENT")
    nodes = tuple(_node_from_payload(node) for node in value["nodes"])
    edges: list[tuple[str, str]] = []
    for edge in value["edges"]:
        valid = type(edge) is list and len(edge) == 2
        valid = valid and all(type(item) is str for item in edge)
        if not valid:
            raise TreeStoreError("INVALID_EDGE_RECORD")
        edges.append((edge[0], edge[1]))
    try:
        return KnowledgeTree(nodes, edges, revision=value["revision"])
    except (TypeError, ValueError) as error:
        raise TreeStoreError("INVALID_TREE_DOCUMENT") from error

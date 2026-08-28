"""Build a bounded document/entity graph with auditable edge evidence."""

from __future__ import annotations

import hashlib
import math
import re
from collections import Counter, defaultdict
from pathlib import Path
from typing import Iterable

from knowledge_graph_extract import (
    canonical_label,
    extract_knowledge,
    semantic_tokens,
)
from knowledge_graph_sources import GraphSource, vector_index_available


RELATION_LABELS = {
    "mentions": "提及", "similar_to": "相似",
}


def stable_id(prefix: str, value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]
    return f"{prefix}:{digest}"


def document_id(scope: str, path: str) -> str:
    return stable_id("doc", f"{scope}\0{path}")


def entity_id(label: str) -> str:
    return stable_id("entity", canonical_label(label).casefold())


def _title(path: str, content: str) -> str:
    match = re.search(r"^\s{0,3}#{1,6}\s+(.+?)\s*$", content, re.MULTILINE)
    if match:
        return canonical_label(match.group(1))
    return Path(path).stem[:80] or "知识文档"


def group_documents(sources: Iterable[GraphSource]) -> list[dict[str, object]]:
    grouped: dict[str, dict[str, object]] = {}
    for source in sources:
        row = grouped.setdefault(source.document_key, {
            "id": document_id(source.scope, source.path),
            "scope": source.scope,
            "project_id": source.project_id,
            "path": source.path,
            "indexed_at": source.indexed_at,
            "parts": [],
            "line_start": source.line_start,
            "line_end": source.line_end,
        })
        row["parts"].append(source.content)
        row["line_start"] = min(int(row["line_start"]), source.line_start)
        row["line_end"] = max(int(row["line_end"]), source.line_end)
    documents = []
    for row in grouped.values():
        content = "\n".join(str(part) for part in row.pop("parts"))
        documents.append({
            **row,
            "title": _title(str(row["path"]), content),
            "content": content,
            "tokens": semantic_tokens(content),
        })
    return sorted(
        documents,
        key=lambda row: (str(row["scope"]), str(row["path"])),
    )


def _evidence(source: GraphSource, line: int, excerpt: str) -> dict[str, object]:
    return {
        "path": source.path,
        "scope": source.scope,
        "line_start": line,
        "line_end": line,
        "excerpt": excerpt[:240],
    }


def _cosine(left: Counter[str], right: Counter[str]) -> float:
    shared = set(left) & set(right)
    numerator = sum(left[token] * right[token] for token in shared)
    left_norm = math.sqrt(sum(value * value for value in left.values()))
    right_norm = math.sqrt(sum(value * value for value in right.values()))
    if not left_norm or not right_norm:
        return 0.0
    return numerator / (left_norm * right_norm)


def _source_extractions(
    sources: Iterable[GraphSource],
) -> tuple[dict[str, dict[str, object]], list[dict[str, object]]]:
    entities: dict[str, dict[str, object]] = {}
    relations: list[dict[str, object]] = []
    for source in sources:
        doc_id = document_id(source.scope, source.path)
        extracted = extract_knowledge(source.content, source.line_start - 1)
        for item in extracted["entities"]:
            node_id = entity_id(str(item["label"]))
            row = entities.setdefault(node_id, {
                "id": node_id, "label": item["label"],
                "kind": item["kind"], "mentions": 0,
                "documents": set(), "evidence": [],
            })
            row["mentions"] = int(row["mentions"]) + 1
            row["documents"].add(doc_id)
            if len(row["evidence"]) < 3:
                row["evidence"].append(_evidence(
                    source, int(item["line"]),
                    source.content.splitlines()[
                        max(0, int(item["line"]) - source.line_start)
                    ],
                ))
        for item in extracted["relations"]:
            relations.append({
                **item,
                "source": entity_id(str(item["source"])),
                "target": entity_id(str(item["target"])),
                "evidence": _evidence(
                    source, int(item["line"]), str(item["excerpt"]),
                ),
            })
    return entities, relations


def _selected_documents(
    documents: list[dict[str, object]],
    entities: dict[str, dict[str, object]],
    limit: int,
) -> list[dict[str, object]]:
    counts = Counter(
        document
        for entity in entities.values()
        for document in entity["documents"]
    )
    ranked = sorted(
        documents,
        key=lambda row: (
            -counts[str(row["id"])],
            0 if row["scope"] == "global" else 1,
            str(row["path"]),
        ),
    )
    return ranked[:min(36, max(4, limit // 3))]


def _entity_nodes(
    entities: dict[str, dict[str, object]],
    selected_docs: set[str],
    limit: int,
) -> list[dict[str, object]]:
    candidates = []
    for row in entities.values():
        documents = set(row["documents"]) & selected_docs
        if not documents:
            continue
        candidates.append({
            **row,
            "documents": sorted(documents),
            "document_count": len(documents),
        })
    ranked = sorted(
        candidates,
        key=lambda row: (
            {"topic": 0, "concept": 1, "term": 2}.get(
                str(row["kind"]), 3,
            ),
            -int(row["document_count"]),
            -int(row["mentions"]),
            str(row["label"]).casefold(),
        ),
    )
    return ranked[:max(0, limit)]


def _aggregate_edges(
    selected_documents: list[dict[str, object]],
    selected_entities: list[dict[str, object]],
    relations: list[dict[str, object]],
) -> list[dict[str, object]]:
    selected_doc_ids = {str(row["id"]) for row in selected_documents}
    selected_entity_ids = {str(row["id"]) for row in selected_entities}
    edges: list[dict[str, object]] = []
    for entity in selected_entities:
        for doc_id in entity["documents"]:
            evidence = [
                item for item in entity["evidence"]
                if document_id(str(item["scope"]), str(item["path"])) == doc_id
            ][:2]
            edges.append(_edge(
                doc_id, str(entity["id"]), "mentions", "提及",
                min(1.0, 0.62 + int(entity["mentions"]) * 0.04),
                evidence,
            ))
    grouped: dict[tuple[str, str, str], list[dict[str, object]]] = defaultdict(list)
    for relation in relations:
        key = (
            str(relation["source"]), str(relation["target"]),
            str(relation["type"]),
        )
        if key[0] in selected_entity_ids and key[1] in selected_entity_ids:
            grouped[key].append(relation)
    for (source, target, relation_type), rows in grouped.items():
        edges.append(_edge(
            source, target, relation_type, str(rows[0]["label"]),
            max(float(row["confidence"]) for row in rows),
            [row["evidence"] for row in rows[:3]],
            weight=len(rows),
        ))
    edges.extend(_similar_edges(selected_documents, selected_doc_ids))
    return edges


def _edge(
    source: str, target: str, edge_type: str, label: str,
    confidence: float, evidence: list[dict[str, object]],
    weight: int = 1,
) -> dict[str, object]:
    return {
        "id": stable_id("edge", f"{source}\0{target}\0{edge_type}"),
        "source": source, "target": target, "type": edge_type,
        "label": label, "confidence": round(confidence, 3),
        "weight": weight, "evidence": evidence,
    }


def _similar_edges(
    documents: list[dict[str, object]], selected: set[str],
) -> list[dict[str, object]]:
    del selected
    candidates: list[tuple[float, dict[str, object], dict[str, object]]] = []
    for index, left in enumerate(documents):
        for right in documents[index + 1:]:
            score = _cosine(left["tokens"], right["tokens"])
            if score >= 0.12:
                candidates.append((score, left, right))
    counts: Counter[str] = Counter()
    edges = []
    for score, left, right in sorted(candidates, key=lambda row: -row[0]):
        if counts[str(left["id"])] >= 2 or counts[str(right["id"])] >= 2:
            continue
        counts[str(left["id"])] += 1
        counts[str(right["id"])] += 1
        edges.append(_edge(
            str(left["id"]), str(right["id"]), "similar_to", "相似",
            score, [],
        ))
    return edges


def build_graph(
    root: Path, sources: list[GraphSource], limit: int,
) -> dict[str, object]:
    bounded = max(20, min(limit, 160))
    documents = group_documents(sources)
    entities, relations = _source_extractions(sources)
    selected_documents = _selected_documents(documents, entities, bounded)
    selected_doc_ids = {str(row["id"]) for row in selected_documents}
    selected_entities = _entity_nodes(
        entities, selected_doc_ids, bounded - len(selected_documents),
    )
    document_nodes = [{
        "id": row["id"], "label": row["title"], "type": "document",
        "kind": "document", "path": row["path"], "scope": row["scope"],
        "project_id": row["project_id"],
        "snippet": str(row["content"])[:240],
        "line_start": row["line_start"], "line_end": row["line_end"],
    } for row in selected_documents]
    entity_nodes = [{
        "id": row["id"], "label": row["label"], "type": "entity",
        "kind": row["kind"], "mentions": row["mentions"],
        "document_count": row["document_count"],
        "evidence": row["evidence"],
    } for row in selected_entities]
    edges = _aggregate_edges(selected_documents, selected_entities, relations)
    nodes = document_nodes + entity_nodes
    return {
        "schema": 1, "nodes": nodes, "edges": edges,
        "stats": {
            "documents": len(document_nodes), "entities": len(entity_nodes),
            "relations": len(edges), "source_chunks": len(sources),
        },
        "capabilities": {
            "entity_extraction": "rules-v1",
            "semantic_search": (
                "hybrid-vector" if vector_index_available(root)
                else "concept-hybrid"
            ),
            "embedding": vector_index_available(root),
            "recommendations": True, "path_exploration": True,
        },
        "warnings": [] if sources else ["NO_KNOWLEDGE_SOURCES"],
    }


__all__ = [
    "build_graph", "document_id", "entity_id", "group_documents",
]

"""Public knowledge graph, search, recommendation, and path services."""

from __future__ import annotations

import copy
import hashlib
import math
from collections import Counter, defaultdict, deque
from datetime import datetime, timezone
from functools import lru_cache
from pathlib import Path

from knowledge_graph_builder import build_graph, document_id, group_documents
from knowledge_graph_extract import semantic_tokens
from knowledge_graph_sources import GraphSource, load_graph_sources
from rag_common import DEFAULT_CONFIG, load_json
from rag_query import query_vector_cache


def _signature(root: Path) -> tuple[tuple[str, int, int], ...]:
    paths: list[Path] = []
    for relative in (
        "verified", "personal-experience/approved",
        "error-experience/approved",
    ):
        directory = root / relative
        if directory.is_dir():
            paths.extend(directory.rglob("*.md"))
    paths.append(root / "global-cache" / "manifest.json")
    paths.append(root / "global-cache" / "lexical.sqlite3")
    projects = root / "projects"
    if projects.is_dir():
        paths.extend(projects.glob("*/manifest.json"))
        paths.extend(projects.glob("*/lexical.sqlite3"))
    rows = []
    for path in sorted(set(paths)):
        try:
            stat = path.stat()
            rows.append((str(path), stat.st_mtime_ns, stat.st_size))
        except OSError:
            continue
    return tuple(rows)


def _revision(signature: tuple[tuple[str, int, int], ...]) -> str:
    digest = hashlib.sha256()
    for path, modified, size in signature:
        digest.update(path.encode("utf-8"))
        digest.update(f"\0{modified}\0{size}\0".encode("ascii"))
    return digest.hexdigest()


@lru_cache(maxsize=8)
def _cached_sources(
    root_text: str, signature: tuple[tuple[str, int, int], ...],
) -> tuple[GraphSource, ...]:
    del signature
    return tuple(load_graph_sources(Path(root_text)))


def _sources(root: Path) -> tuple[GraphSource, ...]:
    resolved = root.resolve()
    return _cached_sources(str(resolved), _signature(resolved))


@lru_cache(maxsize=16)
def _cached_graph(
    root_text: str, signature: tuple[tuple[str, int, int], ...], limit: int,
) -> dict[str, object]:
    sources = _cached_sources(root_text, signature)
    return build_graph(Path(root_text), list(sources), limit)


def build_knowledge_graph(root: Path, limit: int = 100) -> dict[str, object]:
    bounded = max(20, min(int(limit), 160))
    resolved = root.resolve()
    signature = _signature(resolved)
    graph = _cached_graph(str(resolved), signature, bounded)
    return {
        "generated_at": datetime.now(timezone.utc).isoformat(),
        "revision": _revision(signature),
        "graph": copy.deepcopy(graph),
    }


def knowledge_graph_revision(root: Path) -> str:
    return _revision(_signature(root.resolve()))


def _cosine(left: Counter[str], right: Counter[str]) -> float:
    shared = set(left) & set(right)
    numerator = sum(left[token] * right[token] for token in shared)
    left_norm = math.sqrt(sum(value * value for value in left.values()))
    right_norm = math.sqrt(sum(value * value for value in right.values()))
    if not left_norm or not right_norm:
        return 0.0
    return numerator / (left_norm * right_norm)


def _vector_scores(
    root: Path, query: str, limit: int,
) -> dict[str, float]:
    config = load_json(root / "config.json", DEFAULT_CONFIG)
    candidates = [(root / "global-cache", "global")]
    projects = root / "projects"
    if projects.is_dir():
        candidates.extend(
            (path, f"project:{path.name}")
            for path in sorted(projects.iterdir()) if path.is_dir()
        )
    scores: dict[str, float] = {}
    for directory, fallback_scope in candidates:
        manifest = load_json(directory / "manifest.json", {})
        vector = manifest.get("vector", {})
        if not isinstance(vector, dict) or vector.get("enabled") is not True:
            continue
        identity = manifest.get("identity", {})
        project_id = (
            str(identity.get("id", directory.name))
            if isinstance(identity, dict) else directory.name
        )
        scope = "global" if directory.name == "global-cache" else f"project:{project_id}"
        try:
            rows = query_vector_cache(
                directory, query, config, root, min(20, max(limit * 2, 8)),
            )
        except (OSError, RuntimeError, ValueError):
            continue
        for row in rows:
            node_id = document_id(scope or fallback_scope, str(row["path"]))
            scores[node_id] = max(
                scores.get(node_id, 0.0), float(row.get("vector_score", 0.0)),
            )
    return scores


def _snippet(content: str, terms: set[str]) -> str:
    lines = [line.strip() for line in content.splitlines() if line.strip()]
    for line in lines:
        tokens = semantic_tokens(line)
        if terms & set(tokens):
            return line[:260]
    return (lines[0] if lines else "")[:260]


def semantic_search(
    root: Path, query: str, limit: int = 8,
) -> dict[str, object]:
    normalized = query.strip()
    if not normalized:
        raise ValueError("EMPTY_QUERY")
    if len(normalized) > 2_000:
        raise ValueError("QUERY_TOO_LONG")
    bounded = max(1, min(int(limit), 20))
    query_tokens = semantic_tokens(normalized)
    terms = set(query_tokens)
    vector_scores = _vector_scores(root.resolve(), normalized, bounded)
    results = []
    for document in group_documents(_sources(root)):
        concept = _cosine(query_tokens, document["tokens"])
        matched_weight = sum(
            count for token, count in query_tokens.items()
            if token in document["tokens"]
        )
        coverage = matched_weight / max(1, sum(query_tokens.values()))
        title_tokens = semantic_tokens(str(document["title"]))
        title_score = _cosine(query_tokens, title_tokens)
        title_phrase = 1.0 if any(
            len(token) >= 2 and token in normalized.casefold()
            for token in title_tokens
        ) else 0.0
        exact = 1.0 if normalized.casefold() in str(document["content"]).casefold() else 0.0
        base = min(
            1.0,
            coverage * 0.42 + concept * 0.14 + title_score * 0.16
            + title_phrase * 0.28 + exact * 0.16,
        )
        vector = vector_scores.get(str(document["id"]), 0.0)
        score = base * (0.72 if vector else 1.0) + vector * 0.28
        if score <= 0:
            continue
        matched = sorted(
            terms & set(document["tokens"]),
            key=lambda term: (-len(term), term),
        )[:8]
        results.append({
            "node_id": document["id"], "title": document["title"],
            "path": document["path"], "scope": document["scope"],
            "project_id": document["project_id"],
            "line_start": document["line_start"],
            "line_end": document["line_end"],
            "snippet": _snippet(str(document["content"]), terms),
            "score": round(score, 4), "matched_terms": matched,
        })
    results.sort(key=lambda row: (-float(row["score"]), str(row["path"])))
    return {
        "query": normalized,
        "mode": "hybrid-vector" if vector_scores else "concept-hybrid",
        "results": results[:bounded],
        "suggestions": [] if results else ["尝试实体名称", "缩短检索词"],
    }


def _graph_index(root: Path) -> tuple[dict[str, object], dict[str, list[dict[str, object]]]]:
    graph = build_knowledge_graph(root, 160)["graph"]
    adjacency: dict[str, list[dict[str, object]]] = defaultdict(list)
    for edge in graph["edges"]:
        adjacency[str(edge["source"])].append(edge)
        adjacency[str(edge["target"])].append(edge)
    return graph, adjacency


def recommend_knowledge(
    root: Path, node_id: str, limit: int = 6,
) -> dict[str, object]:
    graph, adjacency = _graph_index(root)
    nodes = {str(node["id"]): node for node in graph["nodes"]}
    if node_id not in nodes:
        return {"node_id": node_id, "results": []}
    scores: dict[str, float] = defaultdict(float)
    reasons: dict[str, set[str]] = defaultdict(set)
    for edge in adjacency[node_id]:
        neighbor = str(edge["target"] if edge["source"] == node_id else edge["source"])
        scores[neighbor] = max(scores[neighbor], float(edge["confidence"]))
        reasons[neighbor].add(str(edge["label"]))
        for second in adjacency[neighbor]:
            candidate = str(
                second["target"] if second["source"] == neighbor else second["source"]
            )
            if candidate == node_id:
                continue
            score = float(edge["confidence"]) * float(second["confidence"]) * 0.78
            scores[candidate] = max(scores[candidate], score)
            reasons[candidate].add(f"经由 {nodes.get(neighbor, {}).get('label', '关联实体')}")
    ranked = sorted(
        (identifier for identifier in scores if identifier in nodes),
        key=lambda identifier: (-scores[identifier], str(nodes[identifier]["label"])),
    )[:max(1, min(int(limit), 20))]
    return {
        "node_id": node_id,
        "results": [{
            **nodes[identifier], "score": round(scores[identifier], 4),
            "reasons": sorted(reasons[identifier])[:3],
        } for identifier in ranked],
    }


def explore_path(
    root: Path, source: str, target: str, max_depth: int = 5,
) -> dict[str, object]:
    if not 1 <= int(max_depth) <= 6:
        raise ValueError("INVALID_MAX_DEPTH")
    graph, adjacency = _graph_index(root)
    nodes = {str(node["id"]): node for node in graph["nodes"]}
    if source not in nodes or target not in nodes:
        return {"found": False, "reason": "UNKNOWN_NODE", "nodes": [], "edges": []}
    queue = deque([(source, [])])
    visited = {source}
    found: list[tuple[str, dict[str, object]]] | None = None
    while queue:
        current, path = queue.popleft()
        if len(path) >= max_depth:
            continue
        for edge in adjacency[current]:
            neighbor = str(edge["target"] if edge["source"] == current else edge["source"])
            if neighbor in visited:
                continue
            next_path = path + [(neighbor, edge)]
            if neighbor == target:
                found = next_path
                queue.clear()
                break
            visited.add(neighbor)
            queue.append((neighbor, next_path))
    if found is None:
        return {"found": False, "reason": "NO_PATH", "nodes": [], "edges": []}
    ordered_ids = [source] + [identifier for identifier, _ in found]
    return {
        "found": True,
        "nodes": [nodes[identifier] for identifier in ordered_ids],
        "edges": [edge for _, edge in found],
        "hops": len(found),
    }


__all__ = [
    "build_knowledge_graph", "explore_path", "knowledge_graph_revision",
    "recommend_knowledge", "semantic_search",
]

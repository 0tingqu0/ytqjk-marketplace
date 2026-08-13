"""Dependency-free BM25 search over caller-supplied snapshot chunks."""

from __future__ import annotations

import math
import re
from collections import Counter
from dataclasses import dataclass

from .retrieval_contracts import (
    RetrievalContext,
    SnapshotChunk,
    validate_limit,
    validate_query,
    validate_snapshot,
)


TOKEN = re.compile(r"\w+", re.UNICODE)


@dataclass(frozen=True, slots=True)
class LexicalMatch:
    """One child-chunk BM25 match."""

    chunk_id: str
    score: float


def lexical_search(
    query: str,
    chunks: tuple[SnapshotChunk, ...],
    limit: int,
    *,
    context: RetrievalContext,
) -> tuple[LexicalMatch, ...]:
    """Rank matching child chunks using deterministic BM25."""
    terms = _tokens(validate_query(query))
    corpus = validate_snapshot(chunks, context)
    count = validate_limit(limit)
    documents = [_tokens(item.content) for item in corpus]
    average_length = sum(map(len, documents)) / len(documents)
    frequencies = Counter(term for tokens in documents for term in set(tokens))
    matches: list[LexicalMatch] = []
    for item, tokens in zip(corpus, documents, strict=True):
        term_counts = Counter(tokens)
        score = sum(
            _term_score(
                term_counts[term],
                len(tokens),
                average_length,
                len(corpus),
                frequencies[term],
            )
            for term in set(terms)
        )
        if score > 0:
            matches.append(LexicalMatch(item.chunk_id, score))
    matches.sort(key=lambda match: (-match.score, match.chunk_id))
    parent_by_chunk = {item.chunk_id: item.parent_id for item in corpus}
    parents: dict[str, LexicalMatch] = {}
    for match in matches:
        parents.setdefault(parent_by_chunk[match.chunk_id], match)
    return tuple(parents.values())[:count]


def _tokens(text: str) -> tuple[str, ...]:
    return tuple(TOKEN.findall(text.casefold()))


def _term_score(
    frequency: int,
    length: int,
    average: float,
    total: int,
    containing: int,
) -> float:
    if frequency == 0:
        return 0.0
    k1, b = 1.5, 0.75
    inverse = math.log(1 + (total - containing + 0.5) / (containing + 0.5))
    denominator = frequency + k1 * (1 - b + b * length / max(average, 1.0))
    return inverse * frequency * (k1 + 1) / denominator

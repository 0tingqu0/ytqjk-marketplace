from __future__ import annotations

import sys
import warnings
from pathlib import Path
from typing import Any, Iterable

from rag_common import Chunk


TABLE = "project_chunks"
ANN_THRESHOLD = 50_000


def _imports() -> tuple[Any, Any, Any]:
    try:
        import lancedb
        from fastembed import TextEmbedding
        from lancedb.index import HnswSq
    except ImportError as exc:
        raise RuntimeError(
            "Vector runtime missing. Run bootstrap_runtime.py in D:\\knowledge first."
        ) from exc
    return lancedb, TextEmbedding, HnswSq


def _embedder(TextEmbedding: Any, model_name: str, model_cache: Path, local: bool = False) -> Any:
    with warnings.catch_warnings():
        warnings.filterwarnings(
            "ignore",
            message=r"The model .* now uses mean pooling instead of CLS embedding.*",
            category=UserWarning,
        )
        return TextEmbedding(
            model_name=model_name,
            cache_dir=str(model_cache),
            local_files_only=local,
        )


def build_vectors(
    vector_path: Path,
    chunks: Iterable[Chunk],
    model_name: str,
    dimensions: int,
    model_cache: Path,
) -> int:
    lancedb, TextEmbedding, HnswSq = _imports()
    chunk_list = list(chunks)
    if not chunk_list:
        raise RuntimeError("No chunks available for vector indexing.")
    model_cache.mkdir(parents=True, exist_ok=True)
    print(
        f"MODEL_DOWNLOAD_MAY_OCCUR model={model_name} cache={model_cache}",
        file=sys.stderr,
        flush=True,
    )
    embedder = _embedder(TextEmbedding, model_name, model_cache)
    vectors = list(embedder.passage_embed([chunk.content for chunk in chunk_list]))
    records = []
    for chunk, vector in zip(chunk_list, vectors, strict=True):
        values = vector.tolist()
        if len(values) != dimensions:
            raise RuntimeError(
                f"Embedding dimension mismatch: expected {dimensions}, got {len(values)}"
            )
        records.append(
            {
                "id": chunk.id,
                "path": chunk.path,
                "line_start": chunk.line_start,
                "line_end": chunk.line_end,
                "content": chunk.content,
                "head": chunk.head,
                "indexed_at": chunk.indexed_at,
                "vector": values,
            }
        )
    vector_path.mkdir(parents=True, exist_ok=True)
    database = lancedb.connect(str(vector_path))
    table = database.create_table(TABLE, data=records, mode="overwrite")
    if len(records) >= ANN_THRESHOLD:
        table.create_index(
            "vector",
            config=HnswSq(distance_type="cosine"),
        )
    return len(records)


def query_vectors(
    vector_path: Path,
    query: str,
    model_name: str,
    model_cache: Path,
    limit: int,
) -> list[dict[str, Any]]:
    lancedb, TextEmbedding, _ = _imports()
    embedder = _embedder(TextEmbedding, model_name, model_cache, local=True)
    vector = list(embedder.query_embed([query]))[0]
    database = lancedb.connect(str(vector_path))
    if TABLE not in set(database.list_tables().tables):
        return []
    table = database.open_table(TABLE)
    rows = (
        table.search(vector.tolist())
        .distance_type("cosine")
        .limit(limit)
        .to_list()
    )
    for row in rows:
        distance = float(row.pop("_distance", 1.0))
        row.pop("vector", None)
        row["vector_score"] = 1.0 - distance
    return rows

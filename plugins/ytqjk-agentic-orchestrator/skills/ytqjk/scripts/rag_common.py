from __future__ import annotations

import hashlib
import json
import os
import re
import sqlite3
import subprocess
from dataclasses import asdict, dataclass
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from typing import Any, Iterable

from rag_security import (
    contains_high_confidence_secret,
    is_sensitive_path,
    normalize_remote,
)


SCHEMA_VERSION = 2
DEFAULT_CONFIG: dict[str, Any] = {
    "schema_version": SCHEMA_VERSION,
    "vector_mode": "auto",
    "auto": {
        "text_bytes": 10 * 1024 * 1024,
        "chunks": 2000,
    },
    "index": {
        "chunk_chars": 1200,
        "overlap_chars": 200,
        "max_file_bytes": 1024 * 1024,
    },
    "embedding": {
        "model": "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2",
        "dimensions": 384,
        "estimated_size_gb": 0.22,
    },
}
@dataclass(frozen=True)
class Chunk:
    id: str
    path: str
    line_start: int
    line_end: int
    content: str
    source_sha256: str
    indexed_at: str
    head: str


def utc_now() -> str:
    return datetime.now(timezone.utc).isoformat()


def atomic_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(
        json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8"
    )
    os.replace(temporary, path)


def load_json(path: Path, default: Any) -> Any:
    if not path.exists():
        return default
    return json.loads(path.read_text(encoding="utf-8"))


def run_git(project: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(
        ["git", "-C", str(project), *args],
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="surrogateescape",
        check=False,
    )
    if check and result.returncode != 0:
        raise RuntimeError(result.stderr.strip() or result.stdout.strip())
    return result.stdout


def project_identity(project: Path) -> dict[str, str]:
    root = Path(run_git(project, "rev-parse", "--show-toplevel").strip()).resolve()
    raw_remote = run_git(root, "remote", "get-url", "origin", check=False).strip()
    common_value = Path(run_git(root, "rev-parse", "--git-common-dir").strip())
    common = (
        common_value if common_value.is_absolute() else root / common_value
    ).resolve()
    canonical_root = (
        common.parent
        if os.path.normcase(common.name) == os.path.normcase(".git")
        else common
    )
    normalized_remote = normalize_remote(raw_remote)
    identity = normalized_remote or os.path.normcase(str(canonical_root))
    short_hash = hashlib.sha256(identity.encode("utf-8")).hexdigest()[:12]
    name_source = PurePosixPath(normalized_remote).name if normalized_remote else canonical_root.name
    safe_name = re.sub(r"[^a-zA-Z0-9._-]+", "-", name_source).strip("-_") or "project"
    head = run_git(root, "rev-parse", "--verify", "HEAD", check=False).strip() or "UNBORN"
    diff_args = ("diff", "--binary", "--full-index", "--no-ext-diff", "--no-renames")
    fingerprint_source = run_git(root, *diff_args, "HEAD", "--") if head != "UNBORN" else (
        run_git(root, *diff_args, "--cached", "--") + run_git(root, *diff_args, "--")
    )
    return {
        "id": f"{safe_name}--{short_hash}",
        "name": safe_name,
        "remote": normalized_remote,
        "root": str(root),
        "head": head,
        "source_fingerprint": hashlib.sha256(
            fingerprint_source.encode("utf-8", errors="surrogateescape")
        ).hexdigest(),
        "dirty": str(bool(run_git(root, "status", "--short").strip())).lower(),
    }


def ensure_layout(knowledge_root: Path, project: Path) -> tuple[Path, dict[str, Any]]:
    knowledge_root.mkdir(parents=True, exist_ok=True)
    for relative in (
        "models",
        "verified",
        "personal-experience/candidates",
        "personal-experience/approved",
        "error-experience/candidates",
        "error-experience/approved",
        "projects",
    ):
        (knowledge_root / relative).mkdir(parents=True, exist_ok=True)
    config_path = knowledge_root / "config.json"
    if not config_path.exists():
        atomic_json(config_path, DEFAULT_CONFIG)
    identity = project_identity(project)
    project_dir = knowledge_root / "projects" / identity["id"]
    for relative in ("cache", "handoffs", "errors", "vectors"):
        (project_dir / relative).mkdir(parents=True, exist_ok=True)

    catalog_path = knowledge_root / "catalog.json"
    catalog = load_json(catalog_path, {"schema_version": SCHEMA_VERSION, "projects": {}})
    catalog["schema_version"] = SCHEMA_VERSION
    existing = catalog["projects"].get(identity["id"], {})
    aliases = sorted(set(existing.get("path_aliases", []) + [identity["root"]]))
    catalog["projects"][identity["id"]] = {
        "name": identity["name"],
        "remote": identity["remote"],
        "path_aliases": aliases,
        "last_accessed": utc_now(),
    }
    atomic_json(catalog_path, catalog)
    return project_dir, identity


def tracked_paths(project: Path) -> list[str]:
    output = subprocess.run(
        ["git", "-C", str(project), "ls-files", "-z"],
        capture_output=True,
        check=True,
    ).stdout
    return [part.decode("utf-8", errors="strict") for part in output.split(b"\0") if part]


def is_indexable(relative: str, full_path: Path, max_bytes: int) -> bool:
    if is_sensitive_path(relative):
        return False
    if not full_path.is_file() or full_path.is_symlink():
        return False
    return full_path.stat().st_size <= max_bytes


def read_text(full_path: Path) -> str | None:
    data = full_path.read_bytes()
    if b"\0" in data[:8192]:
        return None
    for encoding in ("utf-8", "utf-8-sig"):
        try:
            return data.decode(encoding)
        except UnicodeDecodeError:
            continue
    return None


def split_chunks(
    relative: str,
    text: str,
    head: str,
    chunk_chars: int,
    overlap_chars: int,
) -> list[Chunk]:
    lines = text.splitlines()
    indexed_at = utc_now()
    source_hash = hashlib.sha256(text.encode("utf-8")).hexdigest()
    chunks: list[Chunk] = []
    start = 0
    while start < len(lines):
        end = start
        length = 0
        while end < len(lines) and (length < chunk_chars or end == start):
            length += len(lines[end]) + 1
            end += 1
        content = "\n".join(lines[start:end]).strip()
        if content:
            raw_id = f"{relative}:{start + 1}:{end}:{source_hash}"
            chunks.append(
                Chunk(
                    id=hashlib.sha256(raw_id.encode("utf-8")).hexdigest(),
                    path=relative,
                    line_start=start + 1,
                    line_end=end,
                    content=content,
                    source_sha256=source_hash,
                    indexed_at=indexed_at,
                    head=head,
                )
            )
        if end >= len(lines):
            break
        rewind = 0
        next_start = end
        while next_start > start and rewind < overlap_chars:
            next_start -= 1
            rewind += len(lines[next_start]) + 1
        start = max(start + 1, next_start)
    return chunks


def scan_project(project: Path, config: dict[str, Any], head: str) -> tuple[list[Chunk], dict[str, int]]:
    index_config = config["index"]
    chunks: list[Chunk] = []
    text_bytes = 0
    files = 0
    skipped = 0
    for relative in tracked_paths(project):
        full_path = project / Path(relative)
        if not is_indexable(relative, full_path, int(index_config["max_file_bytes"])):
            skipped += 1
            continue
        text = read_text(full_path)
        if text is None:
            skipped += 1
            continue
        if contains_high_confidence_secret(text):
            skipped += 1
            continue
        files += 1
        text_bytes += len(text.encode("utf-8"))
        chunks.extend(
            split_chunks(
                relative,
                text,
                head,
                int(index_config["chunk_chars"]),
                int(index_config["overlap_chars"]),
            )
        )
    return chunks, {"files": files, "skipped": skipped, "text_bytes": text_bytes, "chunks": len(chunks)}


def build_lexical(database: Path, chunks: Iterable[Chunk]) -> None:
    temporary = database.with_suffix(".tmp.sqlite3")
    if temporary.exists():
        temporary.unlink()
    connection = sqlite3.connect(temporary)
    try:
        connection.execute(
            "CREATE TABLE chunks (id TEXT PRIMARY KEY, path TEXT, line_start INTEGER, "
            "line_end INTEGER, content TEXT, source_sha256 TEXT, indexed_at TEXT, head TEXT)"
        )
        try:
            connection.execute(
                "CREATE VIRTUAL TABLE chunks_fts USING fts5(id UNINDEXED, path UNINDEXED, content, tokenize='trigram')"
            )
        except sqlite3.OperationalError:
            connection.execute(
                "CREATE VIRTUAL TABLE chunks_fts USING fts5(id UNINDEXED, path UNINDEXED, content, tokenize='unicode61')"
            )
        rows = [tuple(asdict(chunk).values()) for chunk in chunks]
        connection.executemany("INSERT INTO chunks VALUES (?, ?, ?, ?, ?, ?, ?, ?)", rows)
        connection.executemany(
            "INSERT INTO chunks_fts(id, path, content) VALUES (?, ?, ?)",
            [(row[0], row[1], row[4]) for row in rows],
        )
        connection.commit()
    finally:
        connection.close()
    os.replace(temporary, database)


def lexical_query(database: Path, query: str, limit: int) -> list[dict[str, Any]]:
    connection = sqlite3.connect(database)
    connection.row_factory = sqlite3.Row
    try:
        phrase = '"' + query.replace('"', '""') + '"'
        try:
            rows = connection.execute(
                "SELECT c.*, bm25(chunks_fts) AS score FROM chunks_fts "
                "JOIN chunks c ON c.id = chunks_fts.id WHERE chunks_fts MATCH ? "
                "ORDER BY score LIMIT ?",
                (phrase, limit),
            ).fetchall()
            if not rows:
                rows = connection.execute(
                    "SELECT *, 0.0 AS score FROM chunks WHERE content LIKE ? LIMIT ?",
                    (f"%{query}%", limit),
                ).fetchall()
        except sqlite3.OperationalError:
            rows = connection.execute(
                "SELECT *, 0.0 AS score FROM chunks WHERE content LIKE ? LIMIT ?",
                (f"%{query}%", limit),
            ).fetchall()
        return [dict(row) for row in rows]
    finally:
        connection.close()


def read_chunks(database: Path) -> list[Chunk]:
    connection = sqlite3.connect(database)
    connection.row_factory = sqlite3.Row
    try:
        return [Chunk(**dict(row)) for row in connection.execute("SELECT * FROM chunks")]
    finally:
        connection.close()

"""Private SQLite primitives for YTQJK Knowledge."""

from __future__ import annotations

import sqlite3
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator


@contextmanager
def connection(database: Path) -> Iterator[sqlite3.Connection]:
    """Open then always close one SQLite connection."""
    current = sqlite3.connect(database, timeout=15, isolation_level=None)
    current.row_factory = sqlite3.Row
    current.execute("PRAGMA foreign_keys = ON")
    try:
        yield current
    finally:
        current.close()


@contextmanager
def transaction(database: Path) -> Iterator[sqlite3.Connection]:
    """Run an immediate transaction and close its connection."""
    with connection(database) as current:
        current.execute("BEGIN IMMEDIATE")
        try:
            yield current
            current.commit()
        except BaseException:
            current.rollback()
            raise


def read_value(database: Path, statement: str) -> int:
    """Read one integer from a short fixed service query."""
    with connection(database) as current:
        return int(current.execute(statement).fetchone()[0])


def read_row(
    database: Path, statement: str, params: tuple[Any, ...]
) -> dict[str, Any]:
    """Read one row or raise when it does not exist."""
    with connection(database) as current:
        row = current.execute(statement, params).fetchone()
    if row is None:
        raise KeyError("record not found")
    return dict(row)


def read_rows(
    database: Path, statement: str, params: tuple[Any, ...]
) -> list[dict[str, Any]]:
    """Read rows from a fixed service query."""
    with connection(database) as current:
        return [dict(row) for row in current.execute(statement, params).fetchall()]

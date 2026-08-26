from __future__ import annotations

import hashlib
import sys
from pathlib import Path


TESTS = Path(__file__).resolve().parent
SCRIPTS = TESTS.parent
sys.path[:0] = [str(TESTS), str(SCRIPTS)]

from knowledge_tree_runtime import QueryNode, query_node  # noqa: E402
from rag_common import DEFAULT_CONFIG  # noqa: E402
from test_knowledge_tree_query import _node_index  # noqa: E402


def test_ancestor_toctou_is_revalidated_before_return(
    tmp_path: Path,
) -> None:
    knowledge = tmp_path / "knowledge"
    cache = knowledge / "libraries" / "team"
    _node_index(cache, "RACE_MARKER")
    source = knowledge / "verified" / "fact.md"
    source_hash = hashlib.sha256(b"RACE_MARKER").hexdigest()
    row = {
        "path": "verified/fact.md",
        "line_start": 1,
        "line_end": 1,
        "content": "RACE_MARKER",
        "source_sha256": source_hash,
    }

    def racing_query(*_args: object, **kwargs: object) -> list[dict]:
        validator = kwargs["validator"]
        assert callable(validator)
        assert validator(row)
        source.write_text("DRIFTED", encoding="utf-8")
        return [dict(row)]

    node = QueryNode(
        "team",
        "group",
        cache,
        knowledge / ".locks" / "team.lock",
        "tree-group-fallback",
    )
    outcome = query_node(
        node,
        False,
        knowledge,
        tmp_path,
        DEFAULT_CONFIG,
        "RACE_MARKER",
        5,
        query_index=racing_query,
        project_stale=lambda *_args: False,
    )

    assert outcome["results"] == []

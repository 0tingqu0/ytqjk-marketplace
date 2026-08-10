from __future__ import annotations

import io
import os
import sys
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from rag_common import Chunk  # noqa: E402
from platform_paths import default_knowledge_root  # noqa: E402
from vector_store import build_vectors, query_vectors  # noqa: E402


MODEL = "sentence-transformers/paraphrase-multilingual-MiniLM-L12-v2"


@unittest.skipUnless(
    os.environ.get("YTQJK_VECTOR_INTEGRATION") == "1", "vector integration"
)
class VectorStoreTest(unittest.TestCase):
    def test_local_multilingual_round_trip(self) -> None:
        chunk = Chunk(
            id="chunk-1",
            path="docs/guide.md",
            line_start=1,
            line_end=1,
            content="总控负责拆分并行任务并监督复审。",
            source_sha256="source",
            indexed_at="2026-08-10T00:00:00+00:00",
            head="test-head",
        )
        with tempfile.TemporaryDirectory() as temporary:
            database = Path(temporary) / "vectors"
            model_cache = Path(
                os.environ.get(
                    "YTQJK_MODEL_CACHE", str(default_knowledge_root() / "models")
                )
            )
            stdout = io.StringIO()
            with redirect_stdout(stdout):
                count = build_vectors(database, [chunk], MODEL, 384, model_cache)
            self.assertEqual(stdout.getvalue(), "")
            self.assertEqual(count, 1)
            results = query_vectors(database, "如何拆分任务", MODEL, model_cache, 5)
            self.assertEqual(results[0]["id"], "chunk-1")


if __name__ == "__main__":
    unittest.main()

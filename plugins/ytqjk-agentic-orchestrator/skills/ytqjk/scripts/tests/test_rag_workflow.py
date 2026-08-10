from __future__ import annotations

import sys
import unittest
from pathlib import Path


SCRIPTS = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(SCRIPTS))

from rag_common import DEFAULT_CONFIG  # noqa: E402
from rag_query import vector_enabled  # noqa: E402


class RagWorkflowTest(unittest.TestCase):
    def test_auto_vector_mode_requires_a_large_corpus(self) -> None:
        small = {"text_bytes": 1024, "chunks": 10}
        self.assertFalse(vector_enabled("auto", small, DEFAULT_CONFIG))

        byte_threshold = DEFAULT_CONFIG["auto"]["text_bytes"]
        chunk_threshold = DEFAULT_CONFIG["auto"]["chunks"]
        self.assertTrue(
            vector_enabled(
                "auto", {"text_bytes": byte_threshold, "chunks": 10}, DEFAULT_CONFIG
            )
        )
        self.assertTrue(
            vector_enabled(
                "auto", {"text_bytes": 1024, "chunks": chunk_threshold}, DEFAULT_CONFIG
            )
        )

    def test_explicit_vector_modes_override_corpus_size(self) -> None:
        small = {"text_bytes": 0, "chunks": 0}
        self.assertTrue(vector_enabled("on", small, DEFAULT_CONFIG))
        self.assertFalse(vector_enabled("off", small, DEFAULT_CONFIG))


if __name__ == "__main__":
    unittest.main()

from __future__ import annotations

import concurrent.futures
import hashlib
import sys
import uuid
from pathlib import Path


DASHBOARD = Path(__file__).resolve().parents[2] / "dashboard"
SCRIPTS = DASHBOARD.parent / "scripts"
sys.path.insert(0, str(SCRIPTS))
sys.path.insert(0, str(DASHBOARD))

from structured_candidate_writer import (  # noqa: E402
    StructuredCandidateWriteError,
    write_structured_candidate,
)


SOURCE = b"same-source"
CANDIDATE_ID = "a" * 64


def candidate() -> dict[str, object]:
    return {
        "candidate_id": CANDIDATE_ID,
        "source_digest": hashlib.sha256(SOURCE).hexdigest(),
        "state": "CANDIDATE",
        "metadata": {
            "title": "Concurrent candidate",
            "summary": "Only one bundle may win.",
        },
        "chunks": [{
            "id": "chunk-1",
            "text": "stable searchable text",
            "confidence": 0.9,
            "locator": {"page_number": 1},
        }],
    }


def test_same_candidate_concurrent_intakes_leave_one_bundle(
    tmp_path: Path,
) -> None:
    intake_ids = (str(uuid.uuid4()), str(uuid.uuid4()))

    def run(intake_id: str) -> tuple[str, object]:
        try:
            result = write_structured_candidate(
                tmp_path,
                intake_id,
                candidate(),
                SOURCE,
                "same.png",
            )
            return "SUCCEEDED", result
        except StructuredCandidateWriteError as error:
            return error.code, error

    with concurrent.futures.ThreadPoolExecutor(2) as pool:
        results = tuple(pool.map(run, intake_ids))

    assert sorted(status for status, _ in results) == [
        "DUPLICATE_CANDIDATE",
        "SUCCEEDED",
    ]
    winner = next(value for status, value in results
                  if status == "SUCCEEDED")
    chunk_paths = winner.value["chunk_paths"]
    assert len(chunk_paths) == 1
    chunk = tmp_path / chunk_paths[0]
    assert chunk.is_file()
    intake_id = chunk.parent.name
    document = tmp_path / winner.value["candidate_path"]
    assert f"intake_id: {intake_id}" in document.read_text(
        encoding="utf-8"
    )
    chunk_root = chunk.parent.parent
    directories = [item for item in chunk_root.iterdir()
                   if item.is_dir()]
    assert directories == [chunk.parent]

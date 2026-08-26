from __future__ import annotations

import hashlib
import json
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.document_acceptance import (  # noqa: E402
    AcceptanceValidationError,
    METRIC_DEFINITIONS,
    MetricAlgorithms,
    assess_document_acceptance,
    load_evidence,
)


FACETS = [
    "native", "scan", "mixed", "table", "image", "corrupt",
    "clear", "noisy", "tilted",
]
SAMPLE_FACETS = {
    "native": ["native", "table", "image"],
    "clear": ["scan", "clear", "tilted"],
    "noisy": ["scan", "noisy"],
    "mixed": ["mixed"],
    "corrupt": ["corrupt"],
}


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def write_json(path: Path, value: object) -> None:
    path.write_text(
        json.dumps(value, sort_keys=True, separators=(",", ":")),
        encoding="utf-8",
    )


def page(
    number: int, route: str, facets: list[str], text: str,
    truth: bool, *, rich: bool = False, review: bool = False,
) -> dict[str, object]:
    value = {
        "number": number, "route": route, "text": text,
        "reading_order": ["b1", "b2"] if rich else [],
        "table": ({
            "structure": "<table></table>",
            "cells": [{"id": "c1", "text": "value"}],
        } if rich else None),
        "boxes": ([{"id": "b1", "xyxy": [0, 0, 10, 10]}]
                  if rich else []),
    }
    if truth:
        value.update({
            "facets": facets, "image_class": "diagram" if rich else None,
            "review_required": review,
        })
    else:
        value.update({
            "image_top3": ["diagram"] if rich else [],
            "confidence": 0.1 if review else 0.95, "reviewed": review,
        })
    return value


def sample_pages(name: str, truth: bool) -> list[dict[str, object]]:
    specs = {
        "native": [("NATIVE_TEXT", ["native", "table", "image"], True)],
        "clear": [("OCR", ["scan", "clear", "tilted"], False)],
        "noisy": [("OCR", ["scan", "noisy"], False)],
        "mixed": [
            ("NATIVE_TEXT", ["native"], False),
            ("OCR", ["scan"], False),
        ],
        "corrupt": [],
    }
    return [
        page(
            index, route, facets, name, truth, rich=rich,
            review=name == "native",
        )
        for index, (route, facets, rich) in enumerate(specs[name], 1)
    ]


def create_bundle(
    root: Path, *, sidecar_path: str | None = None,
) -> dict[str, Path]:
    manifest_path = root / "manifest.json"
    run_path = root / "run.json"
    paths = {"manifest": manifest_path, "run": run_path}
    manifest = {"schema_version": 2, "required_facets": FACETS, "samples": []}
    run_samples = []
    for name, facets in SAMPLE_FACETS.items():
        source = root / f"source-{name}.pdf"
        truth_path = root / f"truth-{name}.json"
        result_path = root / f"result-{name}.json"
        source.write_bytes(f"%PDF-1.7\n{name}".encode())
        source_hash = digest(source)
        primary = name == "native"
        corrupt = name == "corrupt"
        truth = {
            "schema_version": 1, "sample_id": name,
            "source_sha256": source_hash, "pages": sample_pages(name, True),
            "retry_ids": ["retry-1"] if primary else [],
            "conflict_ids": ["conflict-1"] if primary else [],
            "retrieval_queries": ([{
                "id": "q1", "relevant_document_ids": ["doc-1"],
            }] if primary else []),
            "expected_status": "REJECTED" if corrupt else "ACCEPTED",
            "expected_error_category": (
                "CORRUPT_DOCUMENT" if corrupt else None
            ),
        }
        write_json(truth_path, truth)
        truth_hash = digest(truth_path)
        result = {
            "schema_version": 1, "sample_id": name,
            "source_sha256": source_hash, "sidecar_sha256": truth_hash,
            "pages": sample_pages(name, False),
            "retry_events": ([{"id": "retry-1", "succeeded": True}]
                             if primary else []),
            "conflict_events": ([{"id": "conflict-1", "reviewed": True}]
                                if primary else []),
            "retrieval_results": ([{
                "id": "q1", "top5": [{
                    "document_id": "doc-1", "governance": "approved",
                }],
            }] if primary else []),
            "warm_seconds": [10, 20] if primary else [5],
            "status": "REJECTED" if corrupt else "ACCEPTED",
            "error": ({
                "category": "CORRUPT_DOCUMENT", "ref": "f" * 64,
            } if corrupt else None),
        }
        write_json(result_path, result)
        relative_sidecar = (
            sidecar_path if primary and sidecar_path else truth_path.name
        )
        manifest["samples"].append({
            "id": name, "license": "owned", "facets": facets,
            "pages": len(truth["pages"]), "source": source.name,
            "source_sha256": source_hash, "sidecar": relative_sidecar,
            "sidecar_sha256": truth_hash,
        })
        run_samples.append({
            "id": name, "source_sha256": source_hash,
            "sidecar_sha256": truth_hash, "result": result_path.name,
            "result_sha256": digest(result_path),
        })
        paths[f"source-{name}"] = source
        paths[f"truth-{name}"] = truth_path
        paths[f"result-{name}"] = result_path
    write_json(manifest_path, manifest)
    run = {
        "schema_version": 1,
        "golden_manifest_sha256": digest(manifest_path),
        "samples": run_samples,
    }
    write_json(run_path, run)
    paths.update({
        "source": paths["source-native"], "truth": paths["truth-native"],
        "result": paths["result-native"],
    })
    return paths


def algorithms() -> MetricAlgorithms:
    return MetricAlgorithms(
        "test-teds-v1",
        lambda expected, actual: float(expected == actual),
    )


def rebind_truth(paths: dict[str, Path], truth: object) -> None:
    write_json(paths["truth"], truth)
    truth_hash = digest(paths["truth"])
    manifest = json.loads(paths["manifest"].read_text(encoding="utf-8"))
    manifest["samples"][0]["sidecar_sha256"] = truth_hash
    write_json(paths["manifest"], manifest)
    result = json.loads(paths["result"].read_text(encoding="utf-8"))
    result["sidecar_sha256"] = truth_hash
    write_json(paths["result"], result)
    run = json.loads(paths["run"].read_text(encoding="utf-8"))
    run["golden_manifest_sha256"] = digest(paths["manifest"])
    run["samples"][0]["sidecar_sha256"] = truth_hash
    run["samples"][0]["result_sha256"] = digest(paths["result"])
    write_json(paths["run"], run)


def test_aggregate_metric_map_is_not_an_acceptance_input() -> None:
    report = assess_document_acceptance({}, {})  # type: ignore[arg-type]
    assert report.status == "BLOCK"
    assert report.metrics is None
    assert "file path" in report.blocked[0]


def test_metric_definitions_include_units_and_computation_semantics() -> None:
    assert "ratio" in METRIC_DEFINITIONS["native_cer"]
    assert "5th percentile" in METRIC_DEFINITIONS["image_bbox_iou_p05"]
    assert "truth" in METRIC_DEFINITIONS["review_trigger_recall"]
    assert "category" in METRIC_DEFINITIONS["corrupt_rejection_accuracy"]
    assert "seconds" in METRIC_DEFINITIONS["warm_p95_seconds"]


def test_bound_evidence_computes_complete_pass(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path)
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "PASS"
    assert report.metrics is not None
    assert report.manifest_sha256 == digest(paths["manifest"])
    assert report.run_sha256 == digest(paths["run"])
    assert report.covered_facets == frozenset(FACETS)
    with pytest.raises(TypeError):
        report.metrics["native_cer"] = 1  # type: ignore[index]


def test_missing_teds_adapter_blocks_without_claiming_score(
    tmp_path: Path,
) -> None:
    paths = create_bundle(tmp_path)
    report = assess_document_acceptance(paths["manifest"], paths["run"])
    assert report.status == "BLOCK"
    assert report.metrics is None
    assert "NOT_CONFIGURED: TEDS adapter" in report.blocked[0]


@pytest.mark.parametrize("target", ["source", "truth", "result", "manifest"])
def test_each_bound_digest_is_rechecked(tmp_path: Path, target: str) -> None:
    paths = create_bundle(tmp_path)
    with paths[target].open("ab") as stream:
        stream.write(b" ")
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert "digest mismatch" in report.blocked[0]


def test_required_facet_coverage_cannot_be_weakened(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path)
    manifest = json.loads(paths["manifest"].read_text(encoding="utf-8"))
    manifest["required_facets"] = FACETS[:-1]
    write_json(paths["manifest"], manifest)
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert "cannot be weakened" in report.blocked[0]


def test_sidecar_path_must_be_relative(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path, sidecar_path="../truth.json")
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert "path must be relative" in report.blocked[0]


def test_huge_json_number_is_uniform_validation_block(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path)
    paths["manifest"].write_text(
        '{"schema_version":999999999999999999999999}', encoding="utf-8"
    )
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert "JSON number is out of range" in report.blocked[0]
    with pytest.raises(AcceptanceValidationError, match="out of range"):
        load_evidence(paths["manifest"], paths["run"])


def test_route_is_a_fixed_enum(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path)
    truth = json.loads(paths["truth"].read_text(encoding="utf-8"))
    truth["pages"][0]["route"] = "native"
    rebind_truth(paths, truth)
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert "route is invalid" in report.blocked[0]


@pytest.mark.parametrize(
    ("name", "facets", "message"),
    [
        ("native", ["native", "scan", "table", "image"],
         "exactly one route"),
        ("corrupt", ["corrupt", "image"], "cannot cover other facets"),
    ],
)
def test_sample_facets_cannot_fake_coverage(
    tmp_path: Path, name: str, facets: list[str], message: str,
) -> None:
    paths = create_bundle(tmp_path)
    manifest = json.loads(paths["manifest"].read_text(encoding="utf-8"))
    sample = next(item for item in manifest["samples"] if item["id"] == name)
    sample["facets"] = facets
    write_json(paths["manifest"], manifest)
    report = assess_document_acceptance(
        paths["manifest"], paths["run"], algorithms=algorithms()
    )
    assert report.status == "BLOCK"
    assert message in report.blocked[0]


def test_teds_adapter_crash_fails_closed(tmp_path: Path) -> None:
    paths = create_bundle(tmp_path)

    def broken_teds(_: str, __: str) -> float:
        raise RuntimeError(r"token=secret C:\Users\private\truth.json")

    report = assess_document_acceptance(
        paths["manifest"], paths["run"],
        algorithms=MetricAlgorithms("broken-v1", broken_teds),
    )
    assert report.status == "BLOCK"
    assert report.metrics is None
    assert report.blocked[0].startswith("FAIL_CLOSED: unexpected_type_ref=")
    assert "secret" not in report.blocked[0]
    assert "Users" not in report.blocked[0]

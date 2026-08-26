"""Strict UTF-8 loading and digest binding for acceptance evidence."""

from __future__ import annotations

import hashlib
import json
import math
import re
from pathlib import Path, PurePosixPath
from typing import Mapping, NamedTuple

from .acceptance_metrics import EvidenceSample
from .acceptance_report import (
    FACETS,
    MAX_NUMBER,
    ROUTES,
    AcceptanceValidationError,
    bounded_array as _array,
    bounded_fields as _fields,
    bounded_ids as _ids,
    bounded_integer as _integer,
    bounded_number as _number,
    bounded_text as _text,
    require as _require,
    unique as _unique,
    validate_boxes as _boxes,
    validate_coverage as _coverage_contract,
    validate_events as _events,
    validate_page_contract as _page_facets,
    validate_queries as _queries,
    validate_sample_contract as _sample_facets,
    validate_table as _table,
)


SHA256 = re.compile(r"[0-9a-f]{64}\Z")
MANIFEST_FIELDS = {"schema_version", "required_facets", "samples"}
SAMPLE_FIELDS = {
    "id", "license", "facets", "pages", "source", "source_sha256",
    "sidecar", "sidecar_sha256",
}
RUN_FIELDS = {"schema_version", "golden_manifest_sha256", "samples"}
RUN_SAMPLE_FIELDS = {
    "id", "source_sha256", "sidecar_sha256", "result", "result_sha256",
}
ERROR_CATEGORIES = frozenset({
    "CORRUPT_DOCUMENT", "PROCESSING_FAILED", "SECURITY_REJECTED",
    "UNSUPPORTED_DOCUMENT",
})


class _GoldenSample(NamedTuple):
    sample_id: str
    facets: frozenset[str]
    pages: int
    source_sha256: str
    sidecar_sha256: str
    truth: Mapping[str, object]


class EvidenceBundle(NamedTuple):
    samples: tuple[EvidenceSample, ...]
    manifest_sha256: str
    run_sha256: str
    covered_facets: frozenset[str]


def load_evidence(
    manifest_path: str | Path, run_path: str | Path
) -> EvidenceBundle:
    manifest, manifest_digest, manifest_root = _json_file(manifest_path)
    _fields(manifest, MANIFEST_FIELDS, "manifest")
    _version(manifest["schema_version"], 2, "manifest")
    required = frozenset(_ids(manifest["required_facets"], "required facets"))
    _require(required == FACETS, "required facets cannot be weakened")
    records = [
        _golden(item, manifest_root)
        for item in _array(manifest["samples"], "samples", required=True)
    ]
    _unique((item.sample_id for item in records), "sample ids")
    _unique((item.source_sha256 for item in records), "source digests")
    covered = _coverage_contract([item.facets for item in records])
    run, run_digest, run_root = _json_file(run_path)
    _fields(run, RUN_FIELDS, "run")
    _version(run["schema_version"], 1, "run")
    _require(
        run["golden_manifest_sha256"] == manifest_digest,
        "run manifest digest mismatch",
    )
    entries = _array(run["samples"], "run.samples", required=True)
    for entry in entries:
        _fields(entry, RUN_SAMPLE_FIELDS, "run sample")
    _unique((entry["id"] for entry in entries), "run sample ids")
    by_id = {entry["id"]: entry for entry in entries}
    _require(
        set(by_id) == {item.sample_id for item in records},
        "run sample set mismatch",
    )
    samples = tuple(
        _result(item, by_id[item.sample_id], run_root) for item in records
    )
    for sample in samples:
        _sidecar(sample, sample.truth, truth=True)
        _sidecar(sample, sample.result, truth=False)
    return EvidenceBundle(samples, manifest_digest, run_digest, covered)


def _golden(value: object, root: Path) -> _GoldenSample:
    _fields(value, SAMPLE_FIELDS, "sample")
    sample_id = _text(value["id"], "sample.id")
    licenses = ("owned", "publicly_licensed")
    _require(value["license"] in licenses, "sample license is invalid")
    facets = frozenset(_ids(value["facets"], "sample facets"))
    _require(bool(facets) and facets <= FACETS, "sample facets are invalid")
    pages = _integer(value["pages"], "sample.pages", 0)
    _sample_facets(facets, pages)
    source_digest = _digest(value["source_sha256"], "source_sha256")
    source = _relative_file(root, value["source"], "source")
    _require(_file_digest(source) == source_digest, "source digest mismatch")
    sidecar_digest = _digest(value["sidecar_sha256"], "sidecar_sha256")
    sidecar = _relative_file(root, value["sidecar"], "sidecar")
    truth, actual, _ = _json_file(sidecar)
    _require(actual == sidecar_digest, "sidecar digest mismatch")
    return _GoldenSample(
        sample_id, facets, pages, source_digest, sidecar_digest, truth
    )


def _result(
    golden: _GoldenSample, entry: Mapping[str, object], root: Path
) -> EvidenceSample:
    bindings = (
        ("id", golden.sample_id),
        ("source_sha256", golden.source_sha256),
        ("sidecar_sha256", golden.sidecar_sha256),
    )
    _require(
        not any(entry[name] != expected for name, expected in bindings),
        "result evidence binding mismatch",
    )
    expected = _digest(entry["result_sha256"], "result_sha256")
    path = _relative_file(root, entry["result"], "result")
    result, actual, _ = _json_file(path)
    _require(actual == expected, "result digest mismatch")
    return EvidenceSample(
        golden.sample_id, golden.facets, golden.pages,
        golden.source_sha256, golden.sidecar_sha256, golden.truth, result,
    )


def _sidecar(
    sample: EvidenceSample, value: Mapping[str, object], *, truth: bool
) -> None:
    fields = {
        "schema_version", "sample_id", "source_sha256", "pages",
        "retry_ids", "conflict_ids", "retrieval_queries",
        "expected_status", "expected_error_category",
    } if truth else {
        "schema_version", "sample_id", "source_sha256", "sidecar_sha256",
        "pages", "retry_events", "conflict_events", "retrieval_results",
        "warm_seconds", "status", "error",
    }
    _fields(value, fields, "sidecar")
    _version(value["schema_version"], 1, "sidecar")
    _require(value["sample_id"] == sample.sample_id,
             "sidecar sample id mismatch")
    _require(value["source_sha256"] == sample.source_sha256,
             "sidecar source digest mismatch")
    _require(truth or value["sidecar_sha256"] == sample.sidecar_sha256,
             "result truth digest mismatch")
    _outcome(sample, value, truth)
    pages = _array(value["pages"], "pages")
    for page in pages:
        _page(page, sample, truth)
    numbers = {page["number"] for page in pages}
    expected = set(range(1, sample.pages + 1))
    complete = len(pages) == sample.pages and numbers == expected
    _require(not truth or complete, "truth pages are incomplete")
    if truth and "mixed" in sample.facets:
        routes = {page["route"] for page in pages}
        mixed = "MIXED" in routes or {"NATIVE_TEXT", "OCR"} <= routes
        _require(mixed, "mixed sample lacks mixed routing evidence")
    if truth:
        _ids(value["retry_ids"], "retry ids")
        _ids(value["conflict_ids"], "conflict ids")
        _queries(value["retrieval_queries"], truth=True)
    else:
        _events(value["retry_events"], "succeeded")
        _events(value["conflict_events"], "reviewed")
        _queries(value["retrieval_results"], truth=False)
        for number in _array(value["warm_seconds"], "warm seconds"):
            _number(number, "warm seconds", low=0)


def _page(value: object, sample: EvidenceSample, truth: bool) -> None:
    common = {"number", "route", "text", "reading_order", "table", "boxes"}
    fields = common | ({
        "facets", "image_class", "review_required",
    } if truth else {
        "image_top3", "confidence", "reviewed",
    })
    _fields(value, fields, "page")
    _integer(value["number"], "page.number", 1)
    route = _text(value["route"], "route")
    _require(route in ROUTES, "route is invalid")
    _text(value["text"], "text", empty=True)
    _ids(value["reading_order"], "reading order")
    _table(value["table"])
    _boxes(value["boxes"])
    if truth:
        facets = frozenset(_ids(value["facets"], "page facets"))
        _page_facets(sample.facets, facets, route)
        if value["image_class"] is not None:
            _text(value["image_class"], "image class")
        _require(isinstance(value["review_required"], bool),
                 "review_required must be boolean")
    else:
        _require(len(_ids(value["image_top3"], "image top3")) <= 3,
                 "image top3 exceeds three")
        _number(value["confidence"], "confidence", low=0, high=1)
        _require(isinstance(value["reviewed"], bool),
                 "reviewed must be boolean")


def _outcome(
    sample: EvidenceSample, value: Mapping[str, object], truth: bool
) -> None:
    if truth:
        status = value["expected_status"]
        category = value["expected_error_category"]
        corrupt = "corrupt" in sample.facets
        expected = "REJECTED" if corrupt else "ACCEPTED"
        _require(status == expected, "truth expected status is invalid")
        valid_category = (
            category in ERROR_CATEGORIES if corrupt else category is None
        )
        _require(valid_category, "truth expected error category is invalid")
        return
    status, error = value["status"], value["error"]
    _require(status in ("ACCEPTED", "REJECTED"), "result status is invalid")
    if status == "ACCEPTED":
        _require(error is None, "accepted result cannot contain error")
        return
    _fields(error, {"category", "ref"}, "result error")
    _require(error["category"] in ERROR_CATEGORIES,
             "result error category is invalid")
    _digest(error["ref"], "result error ref")


def _json_file(value: object) -> tuple[dict, str, Path]:
    path = _input_path(value)
    try:
        if path.stat().st_size > 16 * 1024 * 1024:
            raise AcceptanceValidationError("JSON file is too large")
        raw = path.read_bytes()
        parsed = json.loads(
            raw.decode("utf-8"), object_pairs_hook=_unique_object,
            parse_int=_parse_int, parse_float=_parse_float,
            parse_constant=_reject_constant,
        )
    except AcceptanceValidationError:
        raise
    except (OSError, UnicodeError, json.JSONDecodeError, RecursionError) as err:
        raise AcceptanceValidationError("invalid UTF-8 JSON evidence") from err
    _require(isinstance(parsed, dict), "JSON root must be object")
    return parsed, hashlib.sha256(raw).hexdigest(), path.parent.resolve()


def _relative_file(root: Path, value: object, label: str) -> Path:
    text = _text(value, label)
    relative = PurePosixPath(text)
    safe = not relative.is_absolute() and ".." not in relative.parts
    _require(safe and "\\" not in text, f"{label} path must be relative")
    try:
        path = (root / Path(*relative.parts)).resolve(strict=True)
    except OSError as error:
        raise AcceptanceValidationError(
            f"{label} evidence is missing"
        ) from error
    _require(path.is_file() and path.is_relative_to(root.resolve()),
             f"{label} path escapes evidence root")
    return path


def _file_digest(path: Path) -> str:
    digest = hashlib.sha256()
    try:
        with path.open("rb") as stream:
            for chunk in iter(lambda: stream.read(1024 * 1024), b""):
                digest.update(chunk)
    except OSError as error:
        raise AcceptanceValidationError("cannot hash evidence") from error
    return digest.hexdigest()


def _version(value: object, expected: int, label: str) -> None:
    valid = not isinstance(value, bool) and isinstance(value, int)
    _require(valid and value == expected, f"{label} version is invalid")


def _digest(value: object, label: str) -> str:
    _require(isinstance(value, str) and bool(SHA256.fullmatch(value)),
             f"{label} is invalid")
    return value


def _input_path(value: object) -> Path:
    _require(isinstance(value, (str, Path)), "evidence input must be file path")
    return Path(value).resolve()


def _unique_object(pairs: list[tuple[str, object]]) -> dict[str, object]:
    result = {}
    for key, value in pairs:
        if key in result:
            raise AcceptanceValidationError(f"duplicate JSON key: {key}")
        result[key] = value
    return result


def _parse_int(value: str) -> int:
    _require(len(value.lstrip("-")) <= 10, "JSON number is out of range")
    number = int(value)
    _require(abs(number) <= MAX_NUMBER, "JSON number is out of range")
    return number


def _parse_float(value: str) -> float:
    number = float(value)
    _require(math.isfinite(number) and abs(number) <= MAX_NUMBER,
             "JSON number is out of range")
    return number


def _reject_constant(value: str) -> None:
    raise AcceptanceValidationError(f"invalid JSON number: {value}")

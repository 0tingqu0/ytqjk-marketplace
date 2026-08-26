"""Fail-closed canonical serialization for extraction contracts."""

from __future__ import annotations

import hashlib
import json
from dataclasses import fields, is_dataclass
from enum import Enum

from .intake_extraction_contracts import (
    BlockKind,
    BoundingBox,
    CoordinateUnit,
    ExtractedBlock,
    ExtractedPage,
    ExtractedTable,
    ExtractionResult,
    ExtractionMode,
    ImageClassification,
    RecognitionEvidence,
    RecognitionRound,
    TableCell,
    QualityStatus,
)


_CONTRACT_TYPES = (
    BoundingBox, RecognitionEvidence, ImageClassification, RecognitionRound,
    TableCell, ExtractedTable, ExtractedBlock, ExtractedPage, ExtractionResult,
)
_ENUM_TYPES = (BlockKind, CoordinateUnit, ExtractionMode, QualityStatus)
_SCALAR_TYPES = (str, int, float, bool, type(None))


def _rebuild_value(value: object) -> object:
    if is_dataclass(value) and not isinstance(value, type):
        if type(value) not in _CONTRACT_TYPES:
            raise TypeError("nested contract type is invalid")
        return _rebuild(value)
    if type(value) is tuple:
        return tuple(_rebuild_value(item) for item in value)
    if type(value) in _ENUM_TYPES or type(value) in _SCALAR_TYPES:
        return value
    raise TypeError("nested canonical type is invalid")


def _rebuild(value: object) -> object:
    items = fields(value)
    names = {item.name for item in items}
    if set(vars(value)) != names:
        raise ValueError("contract has unexpected fields")
    values = {item.name: _rebuild_value(getattr(value, item.name))
              for item in items}
    return type(value)(**values)


def _canonical_value(value: object) -> object:
    if type(value) in _ENUM_TYPES:
        return value.value
    if type(value) in _CONTRACT_TYPES:
        return {
            item.name: _canonical_value(getattr(value, item.name))
            for item in fields(value)
        }
    if type(value) is tuple:
        return [_canonical_value(item) for item in value]
    if type(value) is float and value.is_integer():
        return int(value)
    if type(value) in _SCALAR_TYPES:
        return value
    raise TypeError("unsupported canonical value")


def canonical_json(value: object) -> str:
    """Revalidate and encode one explicit extraction contract."""
    if type(value) not in _CONTRACT_TYPES:
        raise TypeError("canonical JSON requires an extraction contract")
    try:
        rebuilt = _rebuild(value)
    except (
        AttributeError,
        KeyError,
        OverflowError,
        TypeError,
        ValueError,
    ) as error:
        raise ValueError("invalid extraction contract") from error
    encoded = _canonical_value(rebuilt)
    return json.dumps(encoded, ensure_ascii=False, allow_nan=False,
                      separators=(",", ":"), sort_keys=True)


def canonical_digest(value: object) -> str:
    """Return SHA-256 of normalized canonical UTF-8 JSON."""
    payload = canonical_json(value).encode("utf-8")
    return hashlib.sha256(payload).hexdigest()

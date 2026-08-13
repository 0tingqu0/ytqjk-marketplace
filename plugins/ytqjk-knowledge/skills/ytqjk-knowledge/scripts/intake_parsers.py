"""Deterministic parser registry with explicit optional capabilities."""

from __future__ import annotations

import csv
import hashlib
import io
import json
from collections.abc import Callable

from .intake_contracts import (
    CONTROLLED_SCANNER_ID,
    CapabilityState,
    InspectedSource,
    ParsedChunk,
    ParsedDocument,
    ParserCapability,
    ScannerPort,
    ScanResult,
    ScanState,
)
from .intake_security import LocalScanner


Parser = Callable[[str], str]


class ParserRegistry:
    """Registry for deterministic parsers and declared adapters."""

    def __init__(
        self, *, chunk_chars: int = 2000, scanner: ScannerPort | None = None
    ) -> None:
        if chunk_chars <= 0:
            raise ValueError("chunk size must be positive")
        self._chunk_chars = chunk_chars
        self._scanner = scanner or LocalScanner()
        self._parsers: dict[
            str, tuple[ParserCapability, Parser, frozenset[str]]
        ] = {}
        self._capabilities: dict[str, ParserCapability] = {}

    def register(
        self, extension: str, media_kind: str, parser: Parser, *,
        adapter: str = "builtin", media_types: frozenset[str] = frozenset()
    ) -> None:
        """Register configured parser for extension and media types."""
        normalized = _extension(extension)
        capability = ParserCapability(
            normalized, media_kind, CapabilityState.CONFIGURED, adapter
        )
        self._parsers[normalized] = (capability, parser, media_types)
        self._capabilities[normalized] = capability

    def declare(self, extension: str, media_kind: str, adapter: str) -> None:
        """Declare unavailable parser adapter capability."""
        normalized = _extension(extension)
        if normalized not in self._parsers:
            self._capabilities[normalized] = ParserCapability(
                normalized, media_kind, CapabilityState.NOT_CONFIGURED, adapter
            )

    def capability(self, extension: str) -> ParserCapability:
        """Return declared capability or reject unknown extension."""
        normalized = _extension(extension)
        capability = self._capabilities.get(normalized)
        if capability is None:
            raise ValueError(f"unsupported parser extension: {normalized}")
        return capability

    def parse(self, source: InspectedSource) -> ParsedDocument:
        """Parse inspected source into deterministic scanned chunks."""
        capability = self.capability(source.extension)
        if capability.state is CapabilityState.NOT_CONFIGURED:
            raise ValueError(
                f"parser capability NOT_CONFIGURED: {source.extension}"
            )
        _, parser, media_types = self._parsers[source.extension]
        if (
            source.media_type
            and media_types
            and source.media_type not in media_types
        ):
            raise ValueError("extension and media type conflict")
        text = source.content.decode("utf-8", errors="replace")
        replacement_count = text.count("\ufffd")
        try:
            parsed = parser(text)
        except Exception as error:
            reference = _type_reference(error)
            raise ValueError(f"PARSER_FAILED:ref={reference}") from error
        if not isinstance(parsed, str):
            reference = _type_reference(parsed)
            raise ValueError(f"PARSER_FAILED:ref={reference}")
        encoded = parsed.encode("utf-8")
        try:
            scanner_ready = self._scanner.ready()
        except Exception as error:
            raise ValueError("parsed output scanner FAILED") from error
        if not scanner_ready:
            raise ValueError("parsed output scanner FAILED")
        try:
            scan = self._scanner.scan(encoded, "parsed")
        except Exception as error:
            raise ValueError("parsed output scanner FAILED") from error
        if not isinstance(scan, ScanResult):
            raise ValueError("parsed output scanner FAILED")
        digest = hashlib.sha256(encoded).hexdigest()
        if (
            scan.state is not ScanState.CLEAN
            or scan.sha256 != digest
            or scan.size_bytes != len(encoded)
            or scan.scanner != CONTROLLED_SCANNER_ID
        ):
            raise ValueError("parsed output scanner FAILED")
        document_id = hashlib.sha256(
            f"document:{source.sha256}:{digest}".encode("utf-8")
        ).hexdigest()
        chunks = tuple(
            _chunk(
                document_id,
                ordinal,
                parsed[offset : offset + self._chunk_chars],
            )
            for ordinal, offset in enumerate(
                range(0, len(parsed), self._chunk_chars), 1
            )
        )
        return ParsedDocument(
            document_id=document_id,
            source=source,
            text=parsed,
            content_sha256=digest,
            output_scan=scan,
            encoding="utf-8",
            decode_errors="replace",
            replacement_count=replacement_count,
            chunks=chunks,
        )


def default_registry(
    *, chunk_chars: int = 2000, scanner: ScannerPort | None = None
) -> ParserRegistry:
    """Build registry containing safe parsers and optional adapters."""
    registry = ParserRegistry(chunk_chars=chunk_chars, scanner=scanner)
    for suffix in (".txt", ".md", ".markdown"):
        registry.register(
            suffix, "text", _identity,
            media_types=frozenset({"text/plain", "text/markdown"}),
        )
    registry.register(
        ".json",
        "structured-text",
        _json,
        media_types=frozenset({"application/json"}),
    )
    registry.register(
        ".csv", "tabular-text", lambda text: _delimited(text, ","),
        media_types=frozenset({"text/csv", "application/vnd.ms-excel"}),
    )
    registry.register(
        ".tsv",
        "tabular-text",
        lambda text: _delimited(text, "\t"),
        media_types=frozenset({"text/tab-separated-values"}),
    )
    for suffix, kind, adapter in (
        (".pdf", "document", "pdf"),
        (".docx", "office", "office"),
        (".xlsx", "office", "office"),
        (".png", "image", "ocr"),
        (".jpg", "image", "ocr"),
        (".wav", "audio", "asr"),
        (".mp3", "audio", "asr"),
    ):
        registry.declare(suffix, kind, adapter)
    return registry


def _identity(text: str) -> str:
    return text


def _json(text: str) -> str:
    def pairs(values: list[tuple[str, object]]) -> dict[str, object]:
        result: dict[str, object] = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result

    def reject_constant(value: str) -> object:
        raise ValueError(f"non-standard JSON constant: {value}")

    value = json.loads(
        text, object_pairs_hook=pairs, parse_constant=reject_constant
    )
    return json.dumps(value, ensure_ascii=False, sort_keys=True, indent=2)


def _delimited(text: str, delimiter: str) -> str:
    rows = list(
        csv.reader(
            io.StringIO(text, newline=""),
            delimiter=delimiter,
            strict=True,
        )
    )
    output = io.StringIO(newline="")
    csv.writer(output, delimiter=delimiter, lineterminator="\n").writerows(rows)
    return output.getvalue()


def _extension(value: str) -> str:
    normalized = value.strip().casefold()
    if not normalized:
        raise ValueError("parser extension is required")
    return normalized if normalized.startswith(".") else f".{normalized}"


def _chunk(parent_id: str, ordinal: int, text: str) -> ParsedChunk:
    digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
    chunk_id = hashlib.sha256(
        f"{parent_id}:{ordinal}:{digest}".encode("utf-8")
    ).hexdigest()
    return ParsedChunk(chunk_id, parent_id, ordinal, text, digest)


def _type_reference(value: object) -> str:
    encoded = type(value).__name__.encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()[:16]

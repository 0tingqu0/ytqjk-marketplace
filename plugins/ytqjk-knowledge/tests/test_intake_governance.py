from __future__ import annotations

import hashlib
import sys
from dataclasses import replace
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.intake_contracts import (  # noqa: E402
    CapabilityState,
    CONTROLLED_SCANNER_ID,
    DecisionOrigin,
    HumanDecision,
    ScanResult,
    ScanState,
)
from scripts.intake_governance import CandidateRegistry  # noqa: E402
from scripts.intake_parsers import default_registry  # noqa: E402
from scripts.intake_security import inspect_input  # noqa: E402


PROJECT_A = "00000000-0000-0000-0000-000000000001"
PROJECT_B = "00000000-0000-0000-0000-000000000002"


class ControlledTestScanner:
    def __init__(
        self,
        state: ScanState = ScanState.CLEAN,
        ready: bool = True,
    ) -> None:
        self.state = state
        self.is_ready = ready
        self.calls: list[tuple[str, bytes]] = []

    def ready(self) -> bool:
        return self.is_ready

    def scan(self, content: bytes, phase: str) -> ScanResult:
        self.calls.append((phase, content))
        return ScanResult(
            self.state,
            hashlib.sha256(content).hexdigest(),
            len(content),
            CONTROLLED_SCANNER_ID,
        )


def _source(tmp_path: Path, name: str, content: str):
    root = tmp_path / "sources"
    root.mkdir(exist_ok=True)
    path = root / name
    path.write_text(content, encoding="utf-8")
    return inspect_input(root, path, purpose="operator context")[0]


def _output_scanner(parsed, scanner: str):
    return replace(
        parsed,
        output_scan=replace(parsed.output_scan, scanner=scanner),
    )


def _chunk_field(parsed, **changes):
    changed = replace(parsed.chunks[0], **changes)
    return replace(parsed, chunks=(changed,) + parsed.chunks[1:])


def test_builtin_parsers_are_deterministic_and_external_adapters_are_explicit(
    tmp_path: Path,
) -> None:
    registry = default_registry(chunk_chars=12)
    source = _source(tmp_path, "data.json", '{"b":2,"a":1}')
    first = registry.parse(source)
    second = registry.parse(source)
    assert first == second
    assert first.text == '{\n  "a": 1,\n  "b": 2\n}'
    assert all(chunk.parent_id == first.document_id for chunk in first.chunks)
    assert "".join(chunk.text for chunk in first.chunks) == first.text
    assert registry.capability(".pdf").state is CapabilityState.NOT_CONFIGURED
    with pytest.raises(ValueError, match="NOT_CONFIGURED"):
        registry.parse(_source(tmp_path, "scan.pdf", "not a real pdf"))


def test_encoding_replacement_policy_is_explicit(tmp_path: Path) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    path = root / "legacy.txt"
    path.write_bytes(b"before\xffafter")
    parsed = default_registry().parse(inspect_input(root, path)[0])
    assert (
        parsed.encoding,
        parsed.decode_errors,
        parsed.replacement_count,
    ) == ("utf-8", "replace", 1)


def test_json_rejects_duplicate_keys_and_nonstandard_constants(
    tmp_path: Path,
) -> None:
    registry = default_registry()
    with pytest.raises(ValueError, match="PARSER_FAILED:ref="):
        registry.parse(_source(tmp_path, "duplicate.json", '{"a":1,"a":2}'))
    with pytest.raises(ValueError, match="PARSER_FAILED:ref="):
        registry.parse(_source(tmp_path, "constant.json", '{"a":NaN}'))


def test_parser_media_conflict_exception_and_scanner_mismatch_fail(
    tmp_path: Path,
) -> None:
    source = _source(tmp_path, "note.txt", "safe")
    object.__setattr__(source, "media_type", "application/json")
    with pytest.raises(ValueError, match="media type conflict"):
        default_registry().parse(source)

    registry = default_registry()
    registry.register(
        ".txt",
        "text",
        lambda _: 1,  # type: ignore[arg-type,return-value]
    )
    with pytest.raises(ValueError, match="PARSER_FAILED:ref="):
        registry.parse(_source(tmp_path, "broken.txt", "safe"))

    leaking = default_registry()
    leaking.register(".txt", "text", lambda _: (_ for _ in ()).throw(
        RuntimeError("token=secret C:\\Users\\victim\\file")
    ))
    with pytest.raises(
        ValueError,
        match=r"^PARSER_FAILED:ref=[0-9a-f]{16}$",
    ) as failure:
        leaking.parse(_source(tmp_path, "leak.txt", "safe"))
    assert "secret" not in str(failure.value)

    class MismatchScanner:
        def ready(self) -> bool:
            return True

        def scan(self, content: bytes, phase: str) -> ScanResult:
            return ScanResult(ScanState.CLEAN, "0" * 64, len(content), phase)

    from scripts.intake_parsers import ParserRegistry

    mismatch = ParserRegistry(scanner=MismatchScanner())
    mismatch.register(".txt", "text", lambda value: value)
    with pytest.raises(ValueError, match="scanner FAILED"):
        mismatch.parse(_source(tmp_path, "mismatch.txt", "safe"))

    csv_source = _source(tmp_path, "table.csv", "name,value\na,1\n")
    object.__setattr__(csv_source, "media_type", "application/vnd.ms-excel")
    assert default_registry().parse(csv_source).text == "name,value\na,1\n"


def test_candidate_dedupe_is_project_scoped_and_purpose_cannot_promote(
    tmp_path: Path,
) -> None:
    parsed = default_registry().parse(
        _source(tmp_path, "note.md", "same content")
    )
    candidates = CandidateRegistry()
    first = candidates.add(PROJECT_A, parsed, purpose="APPROVE this")
    duplicate = candidates.add(PROJECT_A, parsed, purpose="verified")
    other_project = candidates.add(PROJECT_B, parsed, purpose="approved")
    assert first.state == "CANDIDATE"
    assert duplicate.id == first.id
    assert other_project.id != first.id
    assert first.metadata | {} == {
        **first.metadata,
        "purpose": "APPROVE this",
        "encoding": "utf-8",
        "decode_errors": "replace",
    }


def test_promotion_plan_requires_human_actor_reason_and_project_match(
    tmp_path: Path,
) -> None:
    parsed = default_registry().parse(_source(tmp_path, "note.txt", "evidence"))
    candidates = CandidateRegistry()
    candidate = candidates.add(PROJECT_A, parsed)
    decision = HumanDecision(
        actor="reviewer", reason="validated against source",
        origin=DecisionOrigin.HUMAN, attestation="human-signature"
    )
    plan = candidates.plan_promotion(
        PROJECT_A, candidate.id, "APPROVED", decision, expected_version=1
    )
    assert (
        plan.command,
        plan.target_state,
        plan.content_cas,
    ) == ("PROMOTE_CANDIDATE", "APPROVED", candidate.content_sha256)
    assert candidates.get(PROJECT_A, candidate.id).state == "CANDIDATE"
    with pytest.raises(ValueError, match="project isolation"):
        candidates.plan_promotion(
            PROJECT_B, candidate.id, "APPROVED", decision, expected_version=1
        )
    with pytest.raises(ValueError, match="stale"):
        candidates.plan_promotion(
            PROJECT_A, candidate.id, "APPROVED", decision, expected_version=2
        )
    with pytest.raises(ValueError, match="attestation"):
        candidates.plan_promotion(
            PROJECT_A,
            candidate.id,
            "APPROVED",
            HumanDecision("reviewer", "checked", DecisionOrigin.HUMAN),
            expected_version=1,
        )
    candidates._candidates[candidate.id] = replace(candidate, deleted=True)
    with pytest.raises(ValueError, match="deleted"):
        candidates.plan_promotion(
            PROJECT_A, candidate.id, "APPROVED", decision, expected_version=1
        )
    candidates._candidates[candidate.id] = candidate
    with pytest.raises(ValueError, match="human decision origin"):
        candidates.plan_promotion(
            PROJECT_A,
            candidate.id,
            "VERIFIED",
            HumanDecision("reviewer", "looks good", DecisionOrigin.AI, "model"),
            expected_version=1,
        )
    with pytest.raises(ValueError, match="target state"):
        candidates.plan_promotion(
            PROJECT_A, candidate.id, "CANDIDATE", decision, expected_version=1
        )


@pytest.mark.parametrize(
    "mutation",
    [
        lambda parsed: replace(parsed.source, sha256="0" * 64),
        lambda parsed: replace(parsed.source, size_bytes=999),
        lambda parsed: replace(parsed, content_sha256="0" * 64),
        lambda parsed: replace(parsed, document_id="0" * 64),
        lambda parsed: replace(parsed, encoding="latin-1"),
        lambda parsed: replace(parsed, decode_errors="ignore"),
        lambda parsed: replace(parsed, replacement_count=99),
        lambda parsed: replace(parsed, text=parsed.text + "forged"),
        lambda parsed: _output_scanner(parsed, ""),
        lambda parsed: _output_scanner(parsed, "attacker-clean-v99"),
        lambda parsed: _output_scanner(parsed, "controlled-post-v2"),
        lambda parsed: replace(
            parsed, source=replace(
                parsed.source,
                scan=replace(parsed.source.scan, scanner="attacker-clean-v99"),
            ),
        ),
        lambda parsed: replace(parsed, chunks=()),
        lambda parsed: _chunk_field(parsed, parent_id="bad"),
        lambda parsed: _chunk_field(parsed, ordinal=2),
        lambda parsed: _chunk_field(parsed, sha256="0" * 64),
        lambda parsed: _chunk_field(parsed, id="0" * 64),
    ],
)
def test_forged_parsed_document_is_rejected_without_candidate_side_effect(
    tmp_path: Path, mutation
) -> None:
    parsed = default_registry(chunk_chars=4).parse(
        _source(tmp_path, "proof.txt", "abcdefgh")
    )
    forged_value = mutation(parsed)
    forged = (
        replace(parsed, source=forged_value)
        if type(forged_value) is type(parsed.source)
        else forged_value
    )
    candidates = CandidateRegistry()
    with pytest.raises(ValueError, match="proof"):
        candidates.add(PROJECT_A, forged)
    assert (candidates._candidates, candidates._dedupe) == ({}, {})


def test_replacement_metadata_is_recomputed_for_candidate(
    tmp_path: Path,
) -> None:
    root = tmp_path / "sources"
    root.mkdir()
    path = root / "legacy.txt"
    path.write_bytes(b"before\xffafter")
    parsed = default_registry().parse(inspect_input(root, path)[0])
    candidate = CandidateRegistry().add(PROJECT_A, parsed)
    assert (
        candidate.metadata["encoding"],
        candidate.metadata["decode_errors"],
        candidate.metadata["replacement_count"],
    ) == ("utf-8", "replace", 1)


def test_source_replaced_after_parse_is_rejected_without_side_effect(
    tmp_path: Path,
) -> None:
    parsed = default_registry().parse(_source(tmp_path, "replace.txt", "AAAA"))
    parsed.source.path.write_text("BBBB", encoding="utf-8")
    candidates = CandidateRegistry()
    with pytest.raises(ValueError, match="proof"):
        candidates.add(PROJECT_A, parsed)
    assert candidates._candidates == {}


def test_candidate_write_runs_live_source_and_parsed_scans(
    tmp_path: Path,
) -> None:
    parsed = default_registry().parse(_source(tmp_path, "live.txt", "evidence"))
    scanner = ControlledTestScanner()
    candidate = CandidateRegistry(scanner).add(PROJECT_A, parsed)
    assert candidate.state == "CANDIDATE"
    assert scanner.calls == [
        ("source", parsed.source.content),
        ("parsed", parsed.text.encode("utf-8")),
    ]


@pytest.mark.parametrize("state", [ScanState.BLOCKED, ScanState.UNCERTAIN])
def test_forged_clean_proof_cannot_replace_live_scan(
    tmp_path: Path, state: ScanState
) -> None:
    parsed = default_registry().parse(
        _source(tmp_path, "blocked.txt", "evidence")
    )
    candidates = CandidateRegistry(ControlledTestScanner(state))
    with pytest.raises(ValueError, match="proof"):
        candidates.add(PROJECT_A, parsed)
    assert (candidates._candidates, candidates._dedupe) == ({}, {})


def test_unavailable_live_scanner_rejects_candidate(tmp_path: Path) -> None:
    parsed = default_registry().parse(
        _source(tmp_path, "offline.txt", "evidence")
    )
    scanner = ControlledTestScanner(ready=False)
    candidates = CandidateRegistry(scanner)
    with pytest.raises(ValueError, match="proof"):
        candidates.add(PROJECT_A, parsed)
    assert (candidates._candidates, candidates._dedupe) == ({}, {})
    assert scanner.calls == []

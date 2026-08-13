from __future__ import annotations

import copy
import sys
from pathlib import Path

import pytest


SKILL_ROOT = Path(__file__).parents[1] / "skills" / "ytqjk-knowledge"
sys.path.insert(0, str(SKILL_ROOT))

from scripts.migration_execute import execute  # noqa: E402
from scripts.migration_port import InMemoryMigrationPort  # noqa: E402
from scripts.migration_plan import (  # noqa: E402
    MigrationSafetyError,
    build_plan,
)


def legacy(tmp_path: Path, extra: bool = False) -> Path:
    root = tmp_path / "legacy"
    candidate = root / "personal-experience" / "candidates" / "one.md"
    candidate.parent.mkdir(parents=True)
    candidate.write_text("fixture evidence", encoding="utf-8")
    if extra:
        approved = root / "personal-experience" / "approved" / "two.md"
        approved.parent.mkdir(parents=True)
        approved.write_text("fixture evidence", encoding="utf-8")
    return root


def apply(
    root: Path,
    port: InMemoryMigrationPort,
    target: Path,
) -> dict[str, object]:
    plan = build_plan(root, target, {"old": "new"})
    return execute(
        plan,
        port,
        apply=True,
        confirmation_token=plan.fingerprint,
        allow_fixture=True,
    )


def test_dry_run_apply_evidence_and_replay_are_stable(tmp_path: Path) -> None:
    root = legacy(tmp_path)
    target, port = tmp_path / "target.sqlite3", InMemoryMigrationPort()
    plan = build_plan(root, target, {"old": "new"})
    assert execute(plan, port)["mode"] == "DRY_RUN"
    first = execute(
        plan,
        port,
        apply=True,
        confirmation_token=plan.fingerprint,
        allow_fixture=True,
    )
    second = execute(
        plan,
        port,
        apply=True,
        confirmation_token=plan.fingerprint,
        allow_fixture=True,
    )
    assert first == second
    assert first["verification"]["governance"] == "PRESERVED"
    assert first["active_generations"] == {"new": 1}


def test_content_dedupe_preserves_two_source_provenance(tmp_path: Path) -> None:
    result = apply(
        legacy(tmp_path, extra=True),
        InMemoryMigrationPort(),
        tmp_path / "x.sqlite3",
    )
    assert result["source_entries"] == 2
    assert result["verification"]["counts"]["unique_documents"] == 1


def test_unknown_partition_downgrades_visible_candidate(tmp_path: Path) -> None:
    root = legacy(tmp_path)
    unknown = root / "legacy-unknown" / "note.md"
    unknown.parent.mkdir()
    unknown.write_text("unknown evidence", encoding="utf-8")
    plan = build_plan(root, tmp_path / "x.sqlite3", {"old": "new"})
    entry = next(
        item
        for item in plan.entries
        if item.relative_path == "legacy-unknown/note.md"
    )
    assert entry.state == "candidate"
    assert entry.downgrade_reason


def test_failed_validation_keeps_existing_active_and_reports_rollback(
    tmp_path: Path,
) -> None:
    port, target = InMemoryMigrationPort(), tmp_path / "x.sqlite3"
    first = apply(legacy(tmp_path / "one"), port, target)

    port.readback = lambda _: {  # type: ignore[method-assign]
        "documents": {}, "memberships": {}, "anchors": []
    }
    failed = apply(legacy(tmp_path / "two"), port, target)
    assert first["active_generations"] == {"new": 1}
    assert port.active == {"new": 1}
    assert len(port.documents) == 1
    assert failed["switch"] == "FAILED"
    assert failed["rollback"] == "SUCCEEDED"


def test_interrupt_resumes_stage_without_new_generation(tmp_path: Path) -> None:
    root, target = legacy(tmp_path), tmp_path / "x.sqlite3"

    class InterruptingPort(InMemoryMigrationPort):
        interrupted = False

        def import_document(self, stage_id: str, entry, content: bytes) -> str:
            if not self.interrupted:
                self.interrupted = True
                raise KeyboardInterrupt()
            return super().import_document(stage_id, entry, content)

    port = InterruptingPort()
    plan = build_plan(root, target, {"old": "new"})
    with pytest.raises(KeyboardInterrupt):
        execute(
            plan,
            port,
            apply=True,
            confirmation_token=plan.fingerprint,
            allow_fixture=True,
        )
    assert port.stages
    result = execute(
        plan,
        port,
        apply=True,
        confirmation_token=plan.fingerprint,
        allow_fixture=True,
    )
    assert result["active_generations"] == {"new": 1}


def test_missing_citation_capability_blocks_without_activation(
    tmp_path: Path,
) -> None:
    class MissingCitationPort(InMemoryMigrationPort):
        def citation_matches(
            self, stage_id: str, project: str, query: str
        ) -> tuple[str, ...]:
            return ()

    port = MissingCitationPort()
    result = apply(legacy(tmp_path), port, tmp_path / "x.sqlite3")
    assert result["status"] == "BLOCKED"
    assert result["rollback"] == "SUCCEEDED"
    assert port.active == {}


def test_abort_failure_is_reported_without_claiming_rollback(
    tmp_path: Path,
) -> None:
    class AbortFailurePort(InMemoryMigrationPort):
        def abort_stage(self, stage_id: str) -> bool:
            return False

    port = AbortFailurePort()
    port.readback = lambda _: {  # type: ignore[method-assign]
        "documents": {}, "memberships": {}, "anchors": []
    }
    result = apply(legacy(tmp_path), port, tmp_path / "x.sqlite3")
    assert result["rollback"] == "FAILED"
    assert port.stages


def test_token_source_change_and_sensitive_input_fail_closed(
    tmp_path: Path,
) -> None:
    root, target = legacy(tmp_path), tmp_path / "x.sqlite3"
    plan = build_plan(root, target, {"old": "new"})
    with pytest.raises(MigrationSafetyError, match="fingerprint"):
        execute(
            plan,
            InMemoryMigrationPort(),
            apply=True,
            confirmation_token="bad",
            allow_fixture=True,
        )
    source = root / "personal-experience" / "candidates" / "one.md"
    source.write_text("changed", encoding="utf-8")
    with pytest.raises(MigrationSafetyError, match="changed after preflight"):
        execute(
            plan,
            InMemoryMigrationPort(),
            apply=True,
            confirmation_token=plan.fingerprint,
            allow_fixture=True,
        )


def test_fixture_apply_rejected_without_explicit_test_switch(
    tmp_path: Path,
) -> None:
    root = legacy(tmp_path)
    plan = build_plan(root, tmp_path / "x.sqlite3", {"old": "new"})
    result = execute(
        plan,
        InMemoryMigrationPort(),
        apply=True,
        confirmation_token=plan.fingerprint,
    )
    assert result["status"] == "NOT_CONFIGURED"
    assert result["adapter"] == "IN_MEMORY_FIXTURE"
    assert result["production_apply"] is False
    assert result["target_database_write"] == "NOT_CONFIGURED"


def test_missing_capability_blocks_before_stage_creation(
    tmp_path: Path,
) -> None:
    root = legacy(tmp_path)
    plan = build_plan(root, tmp_path / "x.sqlite3", {"old": "new"})
    port = InMemoryMigrationPort()
    port.citation_matches = None  # type: ignore[method-assign]
    result = execute(
        plan, port, apply=True, confirmation_token=plan.fingerprint
    )
    assert result["status"] == "BLOCKED"
    assert result["reason"] == "PORT_CAPABILITY_MISSING"
    assert "citation_matches" in result["missing_capabilities"]
    assert port.stages == {}


def test_activate_lie_returns_unknown_not_completed(tmp_path: Path) -> None:
    class LyingPort(InMemoryMigrationPort):
        def activate_cas(
            self, stage_id: str, active: dict[str, object]
        ) -> bool:
            return True

    result = apply(legacy(tmp_path), LyingPort(), tmp_path / "x.sqlite3")
    assert result["status"] == "BLOCKED"
    assert result["reason"] == "ACTIVE_READBACK_MISMATCH"
    assert result["switch"] == "UNKNOWN"


def test_active_drift_after_cas_returns_unknown(tmp_path: Path) -> None:
    class DriftingPort(InMemoryMigrationPort):
        def active_readback(self) -> dict[str, object]:
            result = super().active_readback()
            for row in result["projects"].values():
                row["generation"] = 99
            return result

    result = apply(legacy(tmp_path), DriftingPort(), tmp_path / "x.sqlite3")
    assert result["status"] == "BLOCKED"
    assert result["reason"] == "ACTIVE_READBACK_MISMATCH"


def test_abort_residue_reports_failed_rollback(tmp_path: Path) -> None:
    class ResidualPort(InMemoryMigrationPort):
        def abort_stage(self, stage_id: str) -> bool:
            super().abort_stage(stage_id)
            self.stages[stage_id] = object()  # type: ignore[assignment]
            return True

        def stage_artifacts(self, stage_id: str) -> dict[str, object]:
            return {
                "documents": {"residue": "x"},
                "anchors": [],
                "citations": [],
            }

    port = ResidualPort()
    port.readback = lambda _: {  # type: ignore[method-assign]
        "documents": {}, "memberships": {}, "anchors": []
    }
    result = apply(legacy(tmp_path), port, tmp_path / "x.sqlite3")
    assert result["rollback"] == "FAILED"


@pytest.mark.parametrize("readiness", [None, False, "yes", 1])
def test_non_production_port_is_blocked_before_stage(
    tmp_path: Path, readiness: object
) -> None:
    class ProductionPort(InMemoryMigrationPort):
        fixture_only = False
        adapter_name = "TEST_PRODUCTION_PORT"

    if readiness is None:
        class MissingReadyPort(ProductionPort):
            def __getattribute__(self, name: str):
                if name == "production_ready":
                    raise AttributeError(name)
                return super().__getattribute__(name)

        port = MissingReadyPort()
    else:
        port = ProductionPort()
        port.production_ready = readiness  # type: ignore[assignment]
    before = copy.deepcopy(port.__dict__)
    root = legacy(tmp_path)
    plan = build_plan(root, tmp_path / "x.sqlite3", {"old": "new"})

    result = execute(
        plan, port, apply=True, confirmation_token=plan.fingerprint
    )

    assert result["status"] == "NOT_CONFIGURED"
    assert result["reason"] == "PRODUCTION_ADAPTER_NOT_READY"
    assert result["production_apply"] is False
    assert port.__dict__ == before


def test_strict_true_production_port_runs_full_apply(tmp_path: Path) -> None:
    class ProductionPort(InMemoryMigrationPort):
        fixture_only = False
        production_ready = True
        adapter_name = "TEST_PRODUCTION_PORT"

    port = ProductionPort()
    root = legacy(tmp_path)
    plan = build_plan(root, tmp_path / "x.sqlite3", {"old": "new"})
    result = execute(
        plan, port, apply=True, confirmation_token=plan.fingerprint
    )

    assert result["status"] == "COMPLETED"
    assert result["production_apply"] is True
    assert result["verification"]["active_snapshot"] == "MATCHED"

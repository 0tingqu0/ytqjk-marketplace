from __future__ import annotations

from pathlib import Path

from project_source import project_identity


def project_lock(knowledge_root: Path, project_root: Path) -> Path:
    project_id = project_identity(project_root)["id"]
    return knowledge_root / ".locks" / f"project-{project_id}.lock"


def project_id_lock(knowledge_root: Path, project_id: str) -> Path:
    return knowledge_root / ".locks" / f"project-{project_id}.lock"


def global_lock(knowledge_root: Path) -> Path:
    return knowledge_root / ".locks" / "global-index.lock"

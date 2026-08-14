"""Sanitized local project knowledge-index initialization."""
from __future__ import annotations

import json
import subprocess
import sys
from pathlib import Path


RAG_CLI = (
    Path(__file__).resolve().parent / "plugins"
    / "ytqjk-agentic-orchestrator" / "skills" / "ytqjk" / "scripts"
    / "rag_cli.py"
)


def bootstrap_receipt(status: str) -> dict[str, object]:
    """Return a path-free project-index initialization receipt."""
    return {
        "status": status,
        "project_state": None,
        "project_files": 0,
        "global_state": None,
        "global_files": 0,
        "vector_mode": None,
        "failure_stage": None,
        "failure_code": None,
    }


def bootstrap_project(
    knowledge_root: Path, project_root: Path, vector_mode: str,
) -> dict[str, object]:
    """Initialize the local RAG cache without exposing local paths in output."""
    receipt = bootstrap_receipt("FAILED")
    try:
        completed = subprocess.run(
            [
                sys.executable, str(RAG_CLI), "--knowledge-root",
                str(knowledge_root), "bootstrap", "--project-root",
                str(project_root), "--vector-mode", vector_mode,
            ],
            check=False, capture_output=True, text=True, shell=False,
        )
    except OSError:
        receipt.update({
            "failure_stage": "PROCESS_START",
            "failure_code": "BOOTSTRAP_UNAVAILABLE",
        })
        return receipt
    if completed.returncode != 0:
        receipt.update({
            "failure_stage": "BOOTSTRAP",
            "failure_code": "BOOTSTRAP_FAILED",
        })
        return receipt
    try:
        output = json.loads(completed.stdout)
        project = output["project"]
        global_index = output["global"]
        project_stats = project["stats"]
        global_stats = global_index["stats"]
        if (
            not output["ok"]
            or not isinstance(project["state"], str)
            or not isinstance(global_index["state"], str)
            or not isinstance(project_stats["files"], int)
            or not isinstance(global_stats["files"], int)
            or output["vector_mode"] not in {"off", "auto", "on"}
        ):
            raise ValueError("unexpected bootstrap receipt")
    except (KeyError, TypeError, ValueError, json.JSONDecodeError):
        receipt.update({
            "failure_stage": "BOOTSTRAP_RECEIPT",
            "failure_code": "BOOTSTRAP_RECEIPT_INVALID",
        })
        return receipt
    receipt.update({
        "status": "SUCCEEDED",
        "project_state": project["state"],
        "project_files": project_stats["files"],
        "global_state": global_index["state"],
        "global_files": global_stats["files"],
        "vector_mode": output["vector_mode"],
    })
    return receipt

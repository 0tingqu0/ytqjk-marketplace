"""Non-interactive third-party grill-me installation metadata."""
from __future__ import annotations


GRILL_COMMAND: tuple[str, ...] = (
    "npx",
    "skills@latest",
    "add",
    "mattpocock/skills",
    "--agent",
    "codex",
    "--skill",
    "grill-me",
    "--yes",
    "--copy",
)


def grill_action() -> dict[str, object]:
    return {
        "kind": "third-party-stage",
        "name": "skill:grill-me",
        "command": list(GRILL_COMMAND),
        "verification": "unverified",
        "confirmation_required": True,
        "scope": "target-root staging",
    }

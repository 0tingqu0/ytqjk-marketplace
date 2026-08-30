---
name: ytqjk
description: Start YTQJK orchestration. On bare `$ytqjk`, immediately ask one objective question; before confirmation, do not read files, call tools, load other skills, or create sessions. After confirmation, coordinate planning, workers, review, Git, progress, and local RAG.
---

# YTQJK Agentic Orchestrator

Coordinate visible host tasks; do not simulate roles inline or claim a background
daemon exists.

## Objective gate

When `$ytqjk` is invoked without an actionable objective, ask exactly one objective
question with a recommended answer. Before the objective is clear, do not call tools,
read files, load another skill, or create tasks. A clear objective in the invocation
already satisfies this gate.

Existing-run `status`, `pause`, `resume`, and `stop` requests bypass intake. Send a
pause or stop once, then perform no further work until resumed.

## After confirmation

Read the complete [orchestration protocol](references/protocol.md). Before the first
knowledge operation, also read [local knowledge store](references/knowledge-store.md).

Use roles only when the objective needs them. Keep controller, implementation,
independent review, Git integration, progress, and knowledge responsibilities
separate. Read-only work does not need a Git role. Git mutation requires isolated
worktrees and an exact write allowlist; non-Git mutation is blocked.

The installed Go runtime is the only local executable boundary. Invoke it as
`ytqjk`. If it is not on `PATH`, resolve the bundled plugin `bin/ytqjk` (or
`bin/ytqjk.exe`) first, then the platform runtime path under
`%LOCALAPPDATA%\YTQJK\runtime\bin` or
`${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk/runtime/bin`. Never look for a
language interpreter or a script under this skill.

Before the first project knowledge query, run:

```text
ytqjk rag bootstrap --project-root <work-directory> --vector-mode auto
```

Every query must carry the requesting host session ID and expected project ID through
`ytqjk session query`. Candidate knowledge remains unapproved until an explicit user
governance action.

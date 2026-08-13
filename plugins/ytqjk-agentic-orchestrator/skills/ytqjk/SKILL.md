---
name: ytqjk
description: Start YTQJK orchestration. On bare `$ytqjk`, immediately ask one objective question; before confirmation, do not read files, call tools, load other skills, or create sessions. After confirmation, coordinate planning, workers, review, Git, progress, and local RAG.
---

# YTQJK Agentic Orchestrator

Run a host-tool-driven conversation control plane, not a background daemon. The
controller coordinates only. Invoke with `$ytqjk` or `/skills`.

## Activation objective gate

For a bare activation without an actionable objective, enter `GOAL_INTAKE` and
respond immediately. Throughout `GOAL_INTAKE`, before explicit objective confirmation, make
no tool call and stay in the current activation task. Create no
controller, supervisor, progress, RAG, reviewer, Git, or Worker session or role.

Ask exactly one objective question per response with a recommendation. Load no other skill.
A clear actionable objective supplied in the activation request or a later user reply counts as explicit objective confirmation; do not restate it solely to request another confirmation. Continue intake only for material ambiguity. State no work started only while intake remains active.

Existing-run `stop`, `pause`, `resume`, or `status` bypasses this gate. On explicit
stop or pause, send the stop once, then perform no work until resumed.

## Deferred initialization

After confirmation, first read the complete [protocol](references/protocol.md).
Reread after compaction/version changes. Before RAG, read
[knowledge-store](references/knowledge-store.md).

Use bounded reuse and create roles just in time. Reuse only active or absent memory
anchors, never archived ones. Core host operations are required; pin/archive may
degrade. Read-only work has no Git role and reviewer waits for a result. Git mutation
uses worktrees; non-Git mutation blocks.

Before the first knowledge query, RAG runs `scripts/rag_cli.py bootstrap
--project-root <work-directory> --vector-mode auto`. Every work directory receives a
project sub-library. Candidates remain unapproved.

## Session anchors

Reuse matching `[YTQJK][project-id]` conversations when title discovery is supported;
otherwise reuse current-run IDs only. Anchor each role. Archive via checkpoint,
prepare, confirmed host archive, then finalization. Without archive support, retain
the checkpoint and mark `DONE`. Never claim unsupported lifecycle automation.

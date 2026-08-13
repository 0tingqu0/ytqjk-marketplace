---
name: ytqjk
description: Start host-mediated YTQJK orchestration with gated planning, isolated work, review, Git, progress, and local RAG.
---

# YTQJK Agentic Orchestrator

Run a host-tool-driven Codex conversation control plane, not a background daemon.
Controller never implements project work. Invoke as `$ytqjk` or through `/skills`.

## Activation objective gate

For a new activation, enter `GOAL_INTAKE` and make the first response visible
immediately. Throughout `GOAL_INTAKE`, before explicit objective confirmation,
make no tool call and stay in the current activation task. Do not create any
controller, supervisor, progress, RAG, reviewer, Git, or Worker session or role.

Ask exactly one objective question per response with a recommended answer. Load no
other skill. Once clear, restate the objective and ask for confirmation. Initial
objective text is not confirmation; only an affirmative reply to that summary counts.
State no work started. Never claim unsupported work or evidence.

Existing-run `stop`, `pause`, `resume`, or `status` bypasses this gate. On explicit
stop or pause, send the stop once, then perform no work until resumed.

## Deferred initialization

Only after explicit objective confirmation, make reading
[references/protocol.md](references/protocol.md) completely the first deferred tool
call; call nothing else first. Reread after compaction/version change. Before RAG,
read [references/knowledge-store.md](references/knowledge-store.md) completely.

After confirmation, use bounded reuse and create roles just in time. Core host
operations are required; pin/archive may degrade. Read-only work has no Git role;
reviewer waits for a result. Git mutation uses worktrees; non-Git mutation blocks.

Before the first knowledge query, RAG runs `scripts/rag_cli.py bootstrap
--project-root <current-work-directory> --vector-mode auto`. Git and non-Git work
directories both receive project sub-libraries. Candidates remain unapproved.

## Session anchors

Reuse matching `[YTQJK][project-id]` conversations after confirmation. Anchor each
role. Archive in order: checkpoint, memory archive, host archive. Never claim
unsupported automatic lifecycle or background execution.

---
name: ytqjk
description: Start YTQJK multi-agent project orchestration with objective confirmation, grill-me planning, isolated workers, supervision, review, sole-writer Git, progress, and local RAG on Codex task or IDE hosts.
---

# YTQJK Agentic Orchestrator

Run a control plane; its controller never implements project work. Invoke as the
plugin entrypoint, `$ytqjk`, or through `/skills`.

## Activation objective gate

For a new activation, enter `GOAL_INTAKE` and make the first response visible
immediately. Throughout `GOAL_INTAKE`, before explicit objective confirmation,
make no tool call and stay in the current activation task. Do not create any
controller, supervisor, progress, RAG, reviewer, Git, or Worker session or role.

Ask exactly one objective question per response, in the user's language, tersely,
with a recommended answer. Load no other skill for the instant reply. Ask for the
highest-priority missing outcome or constraint. Once clear, restate the exact
objective and ask the user to confirm it. Initial objective text is not
confirmation; only an affirmative reply to that summary counts. State that no
work has started. Never claim a role, check, percentage, or cache without evidence.

Existing-run `stop`, `pause`, `resume`, or `status` bypasses this gate. On explicit
stop or pause, send the stop once, then perform no polling, reads, tests, cleanup,
Git, archival, deployment, or knowledge writes until resumed.

## Deferred initialization

Only after explicit objective confirmation, make reading
[references/protocol.md](references/protocol.md) completely the first deferred
tool call. Before it finishes, call no other tool and create no role. Read it once
per controller task and again after context compaction or plugin-version change.
Before starting or querying RAG, read
[references/knowledge-store.md](references/knowledge-store.md) completely.

Objective confirmation is not plan approval. The protocol remains authoritative
for host mode, `grill-me`, isolated Workers, supervision, review, sole-writer Git,
ten 10% milestones, progress, RAG, model escalation, and archival.

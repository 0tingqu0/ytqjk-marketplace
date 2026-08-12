---
name: ytqjk
description: Start YTQJK multi-agent project orchestration with objective confirmation, grill-me planning, isolated workers, supervision, review, sole-writer Git, progress, and local RAG on Codex task or IDE hosts.
---

# YTQJK Agentic Orchestrator

Run a Codex-conversation control plane; its controller never implements project work. Invoke as the
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

After objective confirmation and before the first knowledge query, the RAG role
must automatically run `scripts/rag_cli.py bootstrap --project-root <current-work-directory> --vector-mode auto`.
This builds the current work directory's project sub-library and refreshes the
approved global index. It applies to Git and non-Git work directories alike.
Do not treat candidates as approved knowledge or auto-promote any candidate.

Objective confirmation is not plan approval. The protocol remains authoritative
for host mode, `grill-me`, isolated Workers, supervision, review, sole-writer Git,
ten 10% milestones, progress, RAG, model escalation, and archival.

## Session anchors

After confirmation, reuse matching `[YTQJK][project-id]` conversations before
creating new ones. Follow the protocol's session-memory flow for reused or new
conversations. Do not claim unrelated-session, automatic-compaction, or idle-event
access without host support.

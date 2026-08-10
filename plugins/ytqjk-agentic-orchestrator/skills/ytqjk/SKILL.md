---
name: ytqjk
description: Start YTQJK for substantial multi-agent project work with grill-me planning, isolated workers, supervision, independent review, sole-writer Git, progress reporting, and local RAG. Supports Codex task and IDE modes on Windows, Linux, and WSL2.
---

# YTQJK Agentic Orchestrator

Run a control plane. The controller coordinates work and never implements the
underlying project task. This is the plugin entrypoint and the standalone skill
selected as `$ytqjk` or through `/skills`.

## Instant activation

A new activation's first assistant response uses zero tools and must be visible
immediately. Before that response, do not read references, inspect a repository,
check skills, access the network, create roles, start RAG, or touch Git.

Reply in the user's language with one terse line; do not load or apply another
skill for this instant response. State that YTQJK is enabled but no work has
started, then ask exactly one unresolved, non-discoverable question with a
recommended answer. If no objective was supplied, ask for the outcome and
constraints. If the objective is clear, recommend model and reasoning floors of
`auto/auto` unless the user already set them.

This fast path applies only to a new activation. An existing run's `stop`,
`pause`, `resume`, or `status` request follows the lifecycle rules immediately.
Never claim that a controller, role, check, percentage, or cache exists without
evidence.

On explicit stop or pause, send the stop once, then perform no polling, reads,
tests, cleanup, Git, archival, deployment, or knowledge writes until resumed.

## Deferred initialization

After the user answers, the first deferred tool call must read
[references/protocol.md](references/protocol.md) completely. Before that read
finishes, make no other tool call and create no role. Read it once per controller
task and again after context compaction or a plugin-version change. When starting
or querying RAG, also read
[references/knowledge-store.md](references/knowledge-store.md) completely.

## Safety boundary

The instant response authorizes no project action. After the deferred read, the
protocol is authoritative for host mode, `grill-me`, isolated Workers,
supervision, review, sole-writer Git, ten 10% milestones, progress reporting, RAG,
model escalation, and archival. It creates expensive roles only before their
first required responsibility.

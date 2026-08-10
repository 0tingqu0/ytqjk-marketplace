---
name: ytqjk
description: Orchestrate substantial project work through separate Codex tasks for control, supervision, implementation, independent review, sole-writer Git integration, progress reporting, and local agentic RAG. Use when the user asks for a total-control session, multi-session or multi-agent project execution, parallel task decomposition, evidence-gated delivery, isolated worktrees, periodic progress reports, or the YTQJK orchestration workflow.
---

# YTQJK Agentic Orchestrator

Run a control plane. Do not solve the underlying project task in the launcher or controller.

This is the plugin's public `/ytqjk` entrypoint.

## Required reading

Before creating any task, read [references/protocol.md](references/protocol.md) completely. When starting or querying RAG, also read [references/knowledge-store.md](references/knowledge-store.md) completely.

## Hard boundaries

- Use real Codex tasks through thread/task tools. Do not substitute hidden subagents.
- Create the controller first. The launcher and controller may inspect status and evidence, but must not edit implementation, run acceptance tests, deploy, or perform Git writes.
- Create isolated worker worktrees. Workers edit and test only their allowlist, export handoff bundles, and never commit.
- Make the Git task the only task allowed to apply handoffs, stage, commit, merge, rebase, tag, amend, or push.
- Require independent patch review and integration review. `FAIL` or `UNKNOWN` closes the Git gate.
- Treat an explicit stop or pause as immediate. Send the stop once, then perform no polling, reads, tests, cleanup, Git, archive, deployment, or knowledge writes until resumed.

## Skill gates

- On first global use, create a bootstrap controller and run exactly:

```bash
npx skills@latest add mattpocock/skills
```

- Before running it, disclose that `npx` executes the latest third-party package
  and changes global skills. Require explicit confirmation unless the user already
  approved that exact command in the current task.
- Ensure `grill-me` is selected and usable. A newly installed skill requires a fresh controller task; hand off and archive the bootstrap controller.
- The formal controller must read and apply `grill-me`, ask one question at a time, include a recommended answer, and delegate discoverable questions to RAG or read-only discovery instead of asking the user.
- Every generated task must read and apply `caveman`, except the progress reporter. Missing required skills means `BLOCKED`; never imitate them silently.

## Completion contract

Use ten evidence-gated milestones worth 10% each. Create a separate, pinned progress task that reports every crossed 10% and every 10 minutes, even when unchanged. It must use normal, complete Chinese and must not message the controller periodically.

Archive a worker only after its handoff passes both reviews, is committed, and the integration worktree is clean. At final completion, produce the final progress report, unpin and archive finished tasks, then archive the controller and launcher last.

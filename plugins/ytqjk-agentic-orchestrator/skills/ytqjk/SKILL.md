---
name: ytqjk
description: Orchestrate substantial project work through separate Codex tasks or IDE subagents for control, supervision, implementation, independent review, sole-writer Git integration, progress reporting, and local agentic RAG on Windows, Linux, or WSL2. Use when the user asks for a total-control session, multi-session or multi-agent project execution, parallel task decomposition, evidence-gated delivery, isolated worktrees, periodic progress reports, VS Code orchestration, or the YTQJK workflow.
---

# YTQJK Agentic Orchestrator

Run a control plane. Do not solve the underlying project task in the launcher or controller.

This is the plugin's `/ytqjk` entrypoint and the standalone skill selected as
`$ytqjk` or through `/skills` in the Codex IDE extension.

## Required reading

Before creating any task, read [references/protocol.md](references/protocol.md) completely. When starting or querying RAG, also read [references/knowledge-store.md](references/knowledge-store.md) completely.

## Hard boundaries

- Select exactly one host mode from the protocol: real Codex tasks in Task mode,
  or real visible subagent threads in IDE Agent mode. Never simulate roles inline.
- In Task mode, create the controller first. In IDE Agent mode, the current chat
  is the control-only controller. A controller may inspect status and evidence,
  but must not edit implementation, run acceptance tests, deploy, or perform Git writes.
- Create isolated worker worktrees. Workers edit and test only their allowlist, export handoff bundles, and never commit.
- Make the Git task the only task allowed to apply handoffs, stage, commit, merge, rebase, tag, amend, or push.
- Require independent patch review and integration review. `FAIL` or `UNKNOWN` closes the Git gate.
- Treat an explicit stop or pause as immediate. Send the stop once, then perform no polling, reads, tests, cleanup, Git, archive, deployment, or knowledge writes until resumed.

## Skill gates

- On first user-level use, create a bootstrap controller and run exactly:

```bash
npx skills@latest add mattpocock/skills
```

- Run the exact command from the operating-system user home, never from the target
  repository. The CLI's default project path then resolves to the user-level
  `$HOME/.agents/skills` location recognized by Codex, without dirtying the repo.
- Before running it, disclose that `npx` executes the latest third-party package
  and writes user skill files plus installer metadata. Require explicit confirmation
  unless the user already approved that exact command in the current task.
- Select Codex plus `grill-me` and its `grilling` dependency, then verify both are
  usable. The plugin already bundles the attributed `caveman` skill. A newly
  installed skill requires a fresh controller task; hand off and archive the
  bootstrap controller.
- The formal controller must read and apply `grill-me`, ask one question at a time, include a recommended answer, and delegate discoverable questions to RAG or read-only discovery instead of asking the user.
- Every generated task must explicitly read and apply bundled `$caveman`, except the progress reporter. Missing required skills means `BLOCKED`; never imitate them silently.

## Completion contract

Use ten evidence-gated milestones worth 10% each. Create a separate progress task
or visible progress subagent that reports every crossed 10% and every 10 minutes,
even when unchanged. Pin it when the host exposes pinning. It must use normal,
complete Chinese and must not message the controller periodically.

Archive a worker only after its handoff passes both reviews, is committed, and the
integration worktree is clean. In IDE Agent mode, close completed subagent threads
promptly; use stop only to cancel active work, and archive when the host exposes
archival. Never claim an unsupported close, archive, or pin action. At final
completion, produce the final progress report and close roles in the protocol order,
with the Task-mode controller and launcher last.

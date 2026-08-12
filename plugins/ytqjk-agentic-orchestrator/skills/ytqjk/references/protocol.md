# Orchestration protocol

## 0. Activation objective gate

For a new `/ytqjk` or `$ytqjk` activation, enter `GOAL_INTAKE`. Throughout
`GOAL_INTAKE`, before explicit objective confirmation, make no tool call and keep
all objective clarification in the current activation task. Create no controller,
supervisor, progress, RAG, reviewer, Git, or Worker session or role.

Ask exactly one objective question per response, with a recommended answer. Ask
for the highest-priority missing outcome or constraint. Once the objective is
clear, restate its exact scope and ask for confirmation as that turn's only
question. Initial objective text never counts as confirmation; require an
affirmative reply to the restated summary.

After explicit objective confirmation, read this file completely and continue
below. Objective confirmation is not plan approval. Active-run `stop`, `pause`,
`resume`, and `status` bypass this gate and follow lifecycle rules immediately.

## 1. Bootstrap

1. Resolve the target work directory first. When it is a Git project, also resolve current branch, HEAD, and `git status --short` with read-only commands; otherwise mark it `NON_GIT` and still register its project sub-library.
2. Require a clean integration baseline only for Git implementation work. If dirty, stop and ask one `grill-me` question; recommend a dedicated clean worktree. Never stash, reset, delete, or absorb unknown changes. Non-Git directory tasks continue without a Git baseline.
3. Detect a host with capability-equivalent **Codex conversation/session** create,
   list, read, wait, message, title, pin, and archive operations. After objective
   confirmation, list prior `[YTQJK]` conversations for the current project and
   role before creating anything. Reuse one matching, non-archived conversation;
   restore its anchor memory, send it the new objective, and refresh its title.
   Create a new conversation only when no matching conversation is available, the
   prior one is archived, or its role conflicts with the requested responsibility.
   The activation conversation remains the launcher and stays lifecycle-only.
   Never convert a conversation into an opaque autonomous agent or replace a role
   with inline role-play. If the host lacks the complete conversation capability set,
   report `BLOCKED`.
   If neither complete capability set is available, report `BLOCKED`. Do not mix modes.
4. Inspect the host-provided skill inventory. When a readable `grill-me` is already
   available, treat the user-profile bootstrap as complete: do not run `npm view`,
   `npx`, a network check, or an installer. Verify only dependencies declared by that
   installed skill; never require a hard-coded `grilling` directory. The plugin
   supplies the attributed `caveman` skill.
5. Only when `grill-me` is unavailable, run
   `npm view skills@latest engines --json` and verify the active Node.js satisfies it.
   Disclose that `npx skills@latest add mattpocock/skills` executes the latest
   third-party package and writes skill files plus installer metadata. Require
   explicit confirmation unless that exact command was already approved in the
   current task. Run that exact command from the operating-system user home, never
   the target repository. Select Codex and `grill-me`, verify the installed skill and
   only its declared dependencies, then start a fresh formal controller. In IDE Agent
   mode, transfer the objective into a fresh chat if the host cannot replace the
   bootstrap controller.
6. Reuse or create role conversations just in time without weakening role separation:
   - At formal `GRILLING`, create the supervisor and progress reporter. Create them
     independently without waiting for one to finish before starting the other.
   - Before the first repository-answerable question or index operation, create RAG.
   - After plan approval and before the first Worker, create the reviewer and sole Git
     committer. Both must exist before Worker dispatch.
   Title them `[YTQJK][<project-id>] 总控`, `监督`, `复审`, `Git`, `进度`, and `RAG`
   plus a short objective when titles are supported. The stable prefix and project ID
   are the reuse key; do not rely on remembered raw session IDs. Every required role
   still exists before its first responsibility.
7. Pin only the progress role while work is active when pinning is supported. When
   pinning is unavailable, keep it as a separate visible conversation.
8. Anchor every YTQJK-created or reused role after its host session ID and project ID are
   available. Use `scripts/session_memory.py anchor`; never store raw session IDs in
   the knowledge root. Anchoring is idempotent: the same host session ID always maps
   to one stable anonymous key and subsequent knowledge access only refreshes it.
   Before a role resumes after context compaction, checkpoint its
   concise, secret-free memory, then restore and inject only that memory into the same
   role. Before archival, checkpoint and archive it, creating one candidate experience
   record when memory exists. If the host lacks lifecycle events, perform this at the
   first post-compaction turn and immediately before archival; never claim platform-wide
   automatic anchoring.

Explicit objective confirmation authorizes creation, reuse, and coordination of these
conversations plus local writes under the knowledge root resolved by
`YTQJK_KNOWLEDGE_ROOT` or the platform default. Invocation alone does not. Neither
authorizes push, merge, rebase, tag, amend, deployment, remote writes, service
changes, hardware action, or destructive cleanup.

## 2. Role contracts

### Launcher

After explicit objective confirmation, reuse the matching controller conversation
or create it only if absent, then preserve the confirmed objective. Use one bounded
readiness wait, return its visible conversation link, and never wait for completion.
Remain lifecycle-only, propagate a hard stop once, and archive the controller last.
Never implement, test, review, or mutate Git.

### Controller

Apply bundled `$caveman`. Coordinate only. Run the planning gate, own the DAG, reuse
or create role conversations, choose models, enforce scopes, monitor evidence,
request supervisor gates, and close completed roles. Do not edit implementation,
run acceptance, deploy, or mutate Git.

### Supervisor

Apply bundled `$caveman`. Independently inspect objective, plan, dependencies, parallel safety, scope drift, and evidence. Return only `PASS`, `CORRECT`, or `BLOCK` plus concise evidence. `BLOCK` stops affected dispatch or Git gates. The controller may not override it; correction or explicit user arbitration is required.

Review at least before plan approval, each parallel wave, after material scope/dependency changes, before Git integration, and before final closure.

### Worker

Apply bundled `$caveman`. Work in an isolated worktree on one bounded task and exact write allowlist. No task creation and no Git writes. Run focused self-checks, then export a handoff bundle to the assigned project-cache path. Report changed paths, commands, results, residual risks, and bundle hash.

### Reviewer

Apply bundled `$caveman`. Never edit implementation. Perform two independent gates:

1. Patch gate: inspect the worker worktree and handoff bundle; rerun relevant checks.
2. Integration gate: after the Git task applies the bundle, inspect exact staged content and rerun affected checks.

Return `PASS`, `FAIL`, or `UNKNOWN` with evidence. Missing or unrun checks are `UNKNOWN`, never `PASS`. After all commits, run the final regression gate.

### Git committer

Apply bundled `$caveman`. Be the sole Git writer. Never fix implementation or conflicts. Apply only reviewed handoff bundles, stage exact allowlisted paths, inspect the staged diff, run `git diff --cached --check`, request integration review, then create small atomic commits. Never use `git add .` or `git add -A`.

Integrate one bundle at a time so the integration worktree returns clean after each commit. If apply conflicts, stop and return it to a worker. Local commits are allowed after all gates. Push, merge, rebase, tag, amend, and force operations require explicit authorization in the current task.

The Git role provisions each dedicated worker worktree from the approved clean
integration HEAD before that Worker starts. It returns the exact
path and HEAD to the controller, but does not implement in the worktree. Do not remove
worktrees without explicit cleanup authorization.

### Progress reporter

Do not enable `$caveman`. Use normal, complete Simplified Chinese. Run only in its own
visible conversation; do not send timed updates to the controller. Pin it
when supported. Report:

- objective and phase;
- verified percentage;
- ten milestone states;
- active/blocked/failed tasks;
- review and Git evidence;
- errors/model escalations;
- next action and required user action.

Report immediately whenever verified progress crosses 10%. Also report every 10
minutes even when unchanged. Prefer a thread heartbeat automation. If unavailable,
use bounded waits of at most 60 seconds inside the progress role and track elapsed
wall time. Disable the heartbeat, issue the final report, unpin when supported, then
close the completed role. Use stop only to cancel active work. Archive after close
when supported. Never claim unattended timing, closing, pinning, or archival that
the host did not provide.

### RAG

Apply bundled `$caveman`, except use full clarity for approval prompts. Be the only knowledge-index writer. Other tasks query it through task messages. Provide source path, line/symbol, source commit or dirty state, and index time. Knowledge informs decisions but never substitutes current source, tests, or review evidence.

Every query follows one strict route. Search only the current project's rebuildable
global-chunk cache and source index first. `PROJECT_CACHE_HIT` ends the query
without opening the global index.
Only a project-cache miss may search the approved global index. A
`GLOBAL_FALLBACK_HIT` is returned and cached only in that current project. A total
miss returns `KNOWLEDGE_MISS`, cache statistics, and
`SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE`; the current task may then research
externally and ask the RAG role to submit the sanitized result through
`scripts/knowledge_intake_cli.py`. That interface writes only to the global
`personal-experience/candidates` area and never approves or indexes it.

Immediately after the RAG role is created and before its first query, it must run
`scripts/rag_cli.py bootstrap --project-root <current-work-directory> --vector-mode auto`.
This automatic full knowledge build registers the current work directory, rebuilds
its safe project-source index, and refreshes the approved global index. It applies
whether or not the directory is a Git repository. Other roles register their own
work directory when they first use the knowledge query interface. Candidate areas
remain excluded from the global index.

Never read another project's cache or reuse a session anchor across projects. Each
project knowledge cache has a hard 1 GiB capacity. Evict cached global chunks by
LFU first and LRU for equal hit counts; project lexical and vector indexes count
toward the same capacity. If LFU+LRU eviction is insufficient, discard the
rebuildable vector index and then the rebuildable lexical index. Record the reason
in the project manifest. Handoffs and error records are workflow evidence, not
knowledge-cache data, and are outside this capacity policy.

All controller-mediated knowledge queries must use `scripts/session_query.py` with
the current host `--session-id`, rather than calling `rag_cli.py query` directly.
This creates the anchor once or refreshes the existing anchor on every subsequent
knowledge access. The anchoring step must be lightweight and must not index, scan,
or calculate a Git diff; set a bounded query timeout and return a retryable failure
rather than leaving a role waiting indefinitely. A host that cannot supply a stable session ID must report this
limitation and must not invent one shared ID for different sessions.

### Session memory

Session anchors live under `<knowledge-root>/sessions/` and contain only a one-way
session key, project ID, timestamps, and a concise secret-free memory. Never write
raw conversation transcripts, raw session IDs, credentials, private endpoints, or
full local project paths there. Memory includes objective, phase, completed evidence,
unresolved decisions, relative handoff locations, and next action.

When a host scheduler is available, run:

```text
python scripts/session_memory.py --knowledge-root <knowledge-root> sweep --days 30
```

It archives only inactive anchors with stored memory and writes their experience to
`personal-experience/candidates`; it never approves or indexes it. Without scheduler
or idle-event support, state that 30-day detection cannot run automatically.

## 3. Planning gate

Objective confirmation does not approve the plan. The controller must apply
`grill-me` before dispatch. Ask one question at a time with a recommended answer.
Send repo-answerable questions to RAG or a read-only discovery task.

Publish exactly ten evidence-gated milestones, each worth 10%. Each milestone and leaf task must include:

- ID and outcome;
- dependencies and inputs;
- deliverables;
- read scope and exact write allowlist;
- acceptance checks and required evidence;
- owner and model tier;
- selected model floor and reasoning-effort floor;
- parallel group or serial reason;
- handoff and commit boundary;
- risk and rollback.

Also publish a dependency DAG, parallel matrix, and ordered small-commit plan. Dispatch requires resolved material decisions, supervisor `PASS`, and explicit user approval. Material scope, dependency, or acceptance changes return to grilling and plan audit.

Progress increases only after reviewer `PASS`. If later evidence invalidates a milestone, subtract its 10% and report why.

## 4. Parallel execution and models

Run at most three Workers concurrently. The controller may reduce concurrency for machine load or coupling. Parallel work requires satisfied dependencies, disjoint write allowlists, no shared mutable service/device/generated state, and no reliance on unfinished output. Hidden coupling triggers supervisor `BLOCK` and DAG replanning.

During `grill-me`, expose the models and reasoning-effort levels actually available on the current host. Let the user choose a model floor and reasoning-effort floor independently, or leave either on `auto` (recommended). Record both in the approved plan. Choose the lowest adequate model and reasoning level at or above those floors; never silently downgrade. If a selected floor is unavailable, return `BLOCKED` and ask the user to revise it.

Use stronger models or reasoning for architecture, security, difficult debugging, or integration. After two reproducible failures on one task, write an error record, archive the failed Worker after handoff, and create a replacement with a stronger model, reasoning level, or both. A third failure triggers supervisor `BLOCK` and replanning; never retry indefinitely.

## 5. Handoff, review, and commit loop

1. Start a Worker from the current clean integration HEAD in an isolated worktree.
2. Give it one task, allowlist, acceptance checks, bundle destination, and model tier.
3. Worker edits, self-checks, and exports a bundle without staging or committing.
4. Reviewer runs the patch gate. `FAIL/UNKNOWN` returns to a repair Worker.
5. Supervisor checks direction and scope before integration.
6. Freeze new integration writes. Git task verifies clean status and applies one bundle.
7. Reviewer runs the integration gate against staged content.
8. Git task commits a small atomic slice using repository/user commit-message conventions, then proves `git status --short` is empty.
9. Record commit and evidence in RAG. Archive the Worker immediately.
10. Continue until ten milestones pass, then run final regression and closure review.

Only the Git task may change the integration index or history. Task-mode
platform-managed worktree provisioning is a controller control-plane action; IDE
Agent mode delegates explicit worktree provisioning to the Git role. Workers may edit
their assigned working-tree files and use read-only `git status`, `diff`, `log`,
`show`, and `ls-files`; they must not stage, commit, merge, rebase, tag, amend, push,
or run `git worktree` commands.

Use the bundled handoff utility from the skill directory. A Worker exports every changed path explicitly:

```text
python scripts/handoff_cli.py export --repo <worker-worktree> --bundle <project-cache>/handoffs/<task-id> --path <file-1> --path <file-2>
```

After patch review and supervisor `PASS`, the Git task applies exactly one bundle into a clean integration worktree:

```text
python scripts/handoff_cli.py apply --repo <integration-worktree> --bundle <project-cache>/handoffs/<task-id>
```

`apply` verifies the base commit, manifest and payload hashes, allowlist, clean target, ignore rules, clean filters, and patch preflight; it stages exact paths and returns a staged-snapshot SHA-256. If any post-write step fails, it resets/restores only the manifest paths, removes only newly copied payloads, and verifies the integration worktree is clean. It never commits or resolves conflicts. The reviewer must bind its integration `PASS` to that hash.

## 6. Status and archive rules

Use: `GOAL_INTAKE`, `BOOTSTRAP`, `GRILLING`, `PLAN_AUDIT`, `APPROVED`,
`RUNNING`, `REVIEWING`, `REWORK`, `GIT_GATE`, `BLOCKED`, `DONE`.

Archive only after delivery, evidence registration, downstream acknowledgement, and
no pending follow-up. Never archive `BLOCKED`, `NEEDS_INPUT`, or active conversations.
Close completed conversations promptly; use stop only for active cancellation, and
archive after close only when the host exposes it. Otherwise mark them `DONE` and
report that the UI conversation remains visible.

Final order:

1. Workers after their reviewed commits.
2. RAG after final refresh and candidate handoff.
3. Reviewer after final regression `PASS`.
4. Git task after commit hashes and clean status are recorded.
5. Supervisor after final direction `PASS`.
6. Progress reporter after final report; unpin first.
7. Controller after all role conversations are archived. The activation conversation
   remains open for the final user handoff.
8. Launcher last.

On explicit pause/stop, send one stop message to active tasks and stop immediately. Do not poll, read, test, clean, commit, archive, deploy, or update knowledge until the user explicitly resumes.

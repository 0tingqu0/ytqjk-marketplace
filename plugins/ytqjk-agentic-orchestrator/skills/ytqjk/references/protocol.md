# Orchestration protocol

## 0. Activation objective gate

For a bare `/ytqjk` or `$ytqjk` activation without an actionable objective, enter
`GOAL_INTAKE`. Throughout `GOAL_INTAKE`, before explicit objective confirmation, make no tool
call and keep all objective clarification in the current activation task. Create no
controller, supervisor, progress, RAG, reviewer, Git, or Worker session or role.

Ask exactly one objective question per response, with a recommended answer. Ask
for the highest-priority missing outcome or constraint. A clear actionable objective supplied in the activation request or a later user reply counts as explicit objective confirmation; do not restate it solely to request another confirmation. If material ambiguity remains, continue one-question intake.

After explicit objective confirmation, read this file completely and continue
below. Objective confirmation is not plan approval. Active-run `stop`, `pause`,
`resume`, and `status` bypass this gate and follow lifecycle rules immediately.

## 1. Bootstrap

1. Resolve the target work directory and classify the objective as read-only or
   mutation. For Git, also resolve branch, HEAD, and `git status --short` with
   read-only commands. Otherwise mark it `NON_GIT` and still register its project
   sub-library. Non-Git read-only work may continue. Non-Git mutation must reach the
   planning gate as `BLOCKED`; recommend initializing Git or changing the approved
   scope to read-only. Never dispatch a non-Git mutation Worker.
2. For Git mutation work, inspect dirty paths with read-only commands and compare them
   with the objective's read scope, write allowlist, and baseline assumptions. If dirty
   paths do not overlap those scopes and the task does not depend on their uncommitted
   state, preserve them and continue from the recorded HEAD in a dedicated clean integration worktree. If any dirty path overlaps, or the task depends on uncommitted
   state, stop and ask one `grill-me` question; recommend a user-provided clean worktree
   or an explicit decision about that state. Never stash, reset, delete, or absorb
   unknown changes. Read-only work needs no clean baseline.
3. Require host-equivalent Codex conversation/session create, list, read, wait, and
   message operations. If any core operation is absent, report `BLOCKED`; never replace
   visible conversations with inline role-play or opaque autonomous agents. Title is optional: when absent, retain returned session IDs for the current run and report that cross-activation discovery and reuse are unavailable; do not block the current run.
   Pin and archive are optional enhancements: when pin is absent, keep the
   progress conversation visible; when archive is absent, mark a completed role
   `DONE` and report that it remains visible. This protocol is driven by explicit
   host tool calls. It is not a background daemon and must not claim autonomous
   session discovery, scheduling, or lifecycle events.
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
   - After confirmation, the launcher obtains only the controller.
   - At formal `GRILLING`, obtain the supervisor and progress reporter independently.
   - Before the first repository-answerable question or index operation, obtain RAG.
   - Obtain each Worker immediately before its one approved task.
   - Obtain the reviewer only after a reviewable result exists and before that result's
     first independent gate. No result means no reviewer conversation.
   - Obtain the sole Git committer only for an approved Git mutation, before the first
     mutation Worker needs an isolated worktree. In task mode it verifies the baseline
     and the controller requests the host-managed worktree; in IDE Agent mode it
     provisions the worktree itself. Read-only objectives never create it.
   When titles are supported, title them `[YTQJK][<project-id>] 总控`, `监督`, `复审`,
   `Git`, `进度`, and `RAG` plus a short objective. The stable prefix and project ID
   identify candidates; role and handshake state decide reuse. Every required role
   must exist before its first responsibility, not before the objective needs it.
7. Pin only the progress role while work is active when pinning is supported. When
   pinning is unavailable, keep it as a separate visible conversation.
8. When titles are unavailable, reuse only session IDs retained by the current run; on
   a later activation create roles normally rather than guessing among unrelated
   sessions. Otherwise, for every needed role, list matching `[YTQJK][<project-id>] <role>` conversations
   before creation. Exclude host-archived candidates and candidates whose memory
   anchor is archived. Prefer the newest reachable `DONE` or idle candidate; a
   `RUNNING` candidate is reusable only for the same run/objective. Never overwrite a
   different active objective. Read the candidate with a bounded timeout, then inspect
   its anchor before sending work: an absent anchor is created for this project; an
   active anchor is restored; an archived anchor is skipped; an `ARCHIVE_PREPARED`
   anchor may only retry the same run's archive transaction; a corrupt or cross-project
   anchor is `BLOCKED` and is never overwritten. Send an idempotent run token plus
   objective and wait once for acknowledgement. If acknowledgement is lost, re-list
   and re-read once: reuse when the token is present; retry one message when the role
   is reachable but unacknowledged; create a replacement only when the old conversation
   is missing or consistently unreachable. An ambiguous delivery after the bounded
   retry is `BLOCKED`, preventing duplicate active roles. Report every skip or
   replacement.
9. Anchor every YTQJK-created or reused role after its host session ID and project ID are
   available. Use `scripts/session_memory.py anchor`; never store raw session IDs in
   the knowledge root. Anchoring is idempotent: the same host session ID always maps
   to one stable anonymous key and subsequent knowledge access only refreshes it.
   Before a role resumes after context compaction, checkpoint its
   concise, secret-free memory, then restore and inject only that memory into the same
   role. Before archival, follow the explicit checkpoint and memory-archive sequence
   in section 6. If the host lacks lifecycle events, perform this at the
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
through the section 1 handshake or create it only when that handshake permits, then
send the launcher host session ID, callback run token, and confirmed objective. Preserve
that callback envelope. Return its visible conversation link after one
bounded readiness wait; never block that initial response on completion. Remain
available for lifecycle messages. At terminal state, require the controller to send
an idempotent run-token handoff containing status, evidence, remaining risks, and the
final user-facing result to the preserved launcher session ID. If acknowledgement is
lost, re-read the controller once, accept an already persisted matching token, or
request one idempotent resend; ambiguous delivery after that bounded recovery is
`BLOCKED`. Acknowledge it once, deliver it to the user, and only then
close the launcher. Propagate a hard stop once. Never implement, test, review, or
mutate Git.

### Controller

Apply bundled `$caveman`. Coordinate only. Run the planning gate, own the DAG, reuse
or create role conversations, choose models, enforce scopes, monitor evidence,
request supervisor gates, and close completed roles. Persist the launcher session ID
and callback run token received at startup. At terminal state, send the complete
handoff to that launcher; on missing acknowledgement, retain the same token and resend
at most once after a bounded launcher read. Do not edit implementation,
run acceptance, deploy, or mutate Git.

### Supervisor

Apply bundled `$caveman`. Independently inspect objective, plan, dependencies, parallel safety, scope drift, and evidence. Return only `PASS`, `CORRECT`, or `BLOCK` plus concise evidence. `BLOCK` stops affected dispatch or Git gates. The controller may not override it; correction or explicit user arbitration is required.

Review at least before plan approval, each parallel wave, after material scope/dependency changes, before Git integration, and before final closure.

### Worker

Apply bundled `$caveman`. Own one bounded task and exact scope. A read-only Worker
uses an exact read allowlist and makes no file or Git writes. A Git mutation Worker
uses its assigned isolated worktree, provisioned according to section 5, and an exact
write allowlist.
No Worker creates tasks or mutates Git metadata/history. Run focused self-checks and
report commands, results, residual risks, and evidence. A mutation Worker also exports
a handoff bundle to the assigned project-cache path and reports changed paths and its
bundle hash.

### Reviewer

Apply bundled `$caveman`. Never edit implementation. Perform two independent gates:

1. Result gate: for read-only work, inspect the result and rerun relevant evidence
   checks; for mutation work, inspect the Worker worktree and handoff bundle.
2. Integration gate: only for Git mutation, after the Git task applies the bundle,
   inspect exact staged content and rerun affected checks.

Return `PASS`, `FAIL`, or `UNKNOWN` with evidence. Missing or unrun checks are `UNKNOWN`, never `PASS`. After all commits, run the final regression gate.

### Git committer

Apply bundled `$caveman`. Be the sole Git writer. Never fix implementation or conflicts. Apply only reviewed handoff bundles, stage exact allowlisted paths, inspect the staged diff, run `git diff --cached --check`, request integration review, then create small atomic commits. Never use `git add .` or `git add -A`.

Integrate one bundle at a time so the integration worktree returns clean after each commit. If apply conflicts, stop and return it to a worker. Local commits are allowed after all gates. Push, merge, rebase, tag, amend, and force operations require explicit authorization in the current task.

This role exists only for approved Git mutation. It owns clean-baseline, expected-HEAD,
and returned-worktree verification. In task mode the controller performs only the host
control-plane creation call, then this role verifies the returned path and HEAD. In IDE
Agent mode this role provisions the dedicated worktree itself. It never implements in
the worktree. Do not remove worktrees without explicit cleanup authorization.

### Progress reporter

Do not enable `$caveman`. Use normal, complete Simplified Chinese. Run only in its own
visible conversation; do not send timed updates to the controller. Pin it
when supported. Report:

- objective and phase;
- verified percentage;
- weighted milestone states;
- active/blocked/failed tasks;
- review and Git evidence;
- errors/model escalations;
- next action and required user action.

Report immediately whenever verified progress crosses 10%. Also report every 10
minutes even when unchanged. Prefer a thread heartbeat automation. If unavailable,
use bounded waits of at most 60 seconds inside the progress role and track elapsed
wall time. Disable the heartbeat, issue the final report, unpin when supported, then
complete the role through section 6. Use stop only to cancel active work. Never claim
unattended timing, closing, pinning, or archival that the host did not provide.

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

Before the first query, inspect both manifests and rebuild any missing, stale, or
security-incompatible index. Refresh lexical evidence first; vectors are size-gated
only after that refresh. One completed bootstrap covers this check, so do not repeat
queries or rebuilds merely to force vector activation.

Never read another project's cache or reuse a session anchor across projects. Each
project knowledge cache has a hard 1 GiB capacity. Evict cached global chunks by
LFU first and LRU for equal hit counts; project lexical and vector indexes count
toward the same capacity. If LFU+LRU eviction is insufficient, discard the
rebuildable vector index and then the rebuildable lexical index. Record the reason
in the project manifest. Handoffs and error records are workflow evidence, not
knowledge-cache data, and are outside this capacity policy.

All controller-mediated knowledge queries must use `scripts/session_query.py`, rather
than calling `rag_cli.py query` directly. The request message to RAG must carry the
requesting role's stable host session ID, project ID, work directory, and query. RAG
uses that requester session ID for `--session-id`, the project ID for
`--expected-project-id`, never its own ID, and rejects a mismatch before creating an
anchor. This creates the requester's anchor once
or refreshes it on subsequent knowledge access. The anchoring step must be lightweight
and must not index, scan, or calculate a Git diff; cap each query at 60 seconds and
return a retryable failure rather than leaving a role waiting indefinitely. A host
that cannot supply a stable session ID must report this limitation and must not invent
one shared ID for different sessions.

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

Record `GIT_READ_ONLY`, `GIT_MUTATION`, or `NON_GIT_READ_ONLY` in the plan. A
`NON_GIT_MUTATION` plan is `BLOCKED`: do not request plan approval or dispatch;
recommend initializing Git or narrowing the objective to read-only.

Publish an evidence-gated dependency DAG at natural task granularity. Assign each
milestone a positive whole-number percentage; all milestone weights must total 100%.
Never split work merely to manufacture ten milestones. Each milestone and leaf task must include:

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

Also publish a parallel matrix and ordered small-commit plan. Dispatch requires resolved material decisions, supervisor `PASS`, and explicit user approval. Material scope, dependency, or acceptance changes return to grilling and plan audit.

Verified progress equals the sum of weights for milestones with reviewer `PASS`.
Report immediately when it first reaches or crosses each 10-percentage-point boundary;
one milestone may cross several boundaries, but emit one current-state report. If later
evidence invalidates a milestone, subtract that milestone's weight and report why.

## 4. Parallel execution and models

Run at most three Workers concurrently. The controller may reduce concurrency for machine load or coupling. Parallel work requires satisfied dependencies, disjoint write allowlists, no shared mutable service/device/generated state, and no reliance on unfinished output. Hidden coupling triggers supervisor `BLOCK` and DAG replanning.

During `grill-me`, expose the models and reasoning-effort levels actually available on the current host. Let the user choose a model floor and reasoning-effort floor independently, or leave either on `auto` (recommended). Record both in the approved plan. Choose the lowest adequate model and reasoning level at or above those floors; never silently downgrade. If a selected floor is unavailable, return `BLOCKED` and ask the user to revise it.

Use stronger models or reasoning for architecture, security, difficult debugging, or integration. After two reproducible failures on one task, write an error record, archive the failed Worker after handoff, and create a replacement with a stronger model, reasoning level, or both. A third failure triggers supervisor `BLOCK` and replanning; never retry indefinitely.

## 5. Review, handoff, and commit loops

### Read-only loop

1. Start a Worker only when an approved read-only task needs independent execution.
2. Give it one task, exact read scope, acceptance checks, and model tier.
3. Worker returns evidence without changing files or Git state.
4. Only now obtain the reviewer; it runs the result gate. `FAIL/UNKNOWN` returns to
   a replacement or repair Worker within the same read-only scope.
5. Supervisor checks direction and scope. Record the reviewed evidence, then archive
   the Worker through section 6. No Git role, worktree, bundle, staging, or commit is
   used in this loop.

### Git mutation loop

1. Obtain the Git role and verify the current clean integration HEAD. In task mode the
   controller makes the host worktree creation call; in IDE Agent mode the Git role
   provisions it. The Git role verifies the exact path and HEAD before Worker dispatch.
2. Give it one task, write allowlist, acceptance checks, bundle destination, and model tier.
3. Worker edits, self-checks, and exports a bundle without staging or committing.
4. Only now obtain the reviewer; it runs the result gate. `FAIL/UNKNOWN` returns to a repair Worker.
5. Supervisor checks direction and scope before integration.
6. Freeze new integration writes. Git task verifies clean status and applies one bundle.
7. Reviewer runs the integration gate against staged content.
8. Git task commits a small atomic slice using repository/user commit-message conventions, then proves `git status --short` is empty.
9. Record commit and evidence in RAG. Archive the Worker through section 6.
10. Continue until all milestone weights pass, then run final regression and closure review.

Only the Git task may change the integration index or history. In task mode, the Git
role verifies the clean baseline and expected HEAD, then the controller performs only
the host control-plane call that creates the platform-managed worktree and returns its
path to the Git role for verification. In IDE Agent mode, the Git role performs
explicit `git worktree` provisioning. Workers may edit their assigned working-tree
files and use read-only `git status`, `diff`, `log`,
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
For each completed role, perform this explicit transaction in order:

1. Create a concise, secret-free memory and run `session_memory.py checkpoint`.
2. If host archive is supported, run `session_memory.py prepare-archive`, then archive
   the host conversation. Only after the host confirms archival, run
   `session_memory.py finalize-archive`; verify the anchor is archived and any non-empty
   changed memory produced its candidate record. The controller or launcher finalizes
   a child that can no longer issue calls after host archival.
3. If host archival fails, leave the anchor `ARCHIVE_PREPARED`, keep the host
   conversation unarchived, and report a retryable failure. The same run may checkpoint
   again, returning the anchor to `ACTIVE`, then repeat the transaction. Never reuse a
   prepared anchor for a different objective.
4. If host archive is unavailable, do not prepare or archive the memory anchor. Keep the active
   checkpoint reusable, mark the host conversation `DONE`, and report that it remains
   visible. A later real host archival or 30-day sweep performs the memory archive.

If finalization fails after confirmed host archival, `archive_sync.py` may recover it
from the host archive log. A memory-archived conversation is never a reuse candidate.

Use stop only for active cancellation. Never treat dashboard refresh or archive-log
scanning as the primary lifecycle path; those mechanisms are recovery aids only.

For roles that were actually created, final order is:

1. Workers after their reviewed results or commits.
2. RAG after final refresh and candidate handoff.
3. Reviewer after final regression `PASS`.
4. Git task after commit hashes and clean status are recorded.
5. Supervisor after final direction `PASS`.
6. Progress reporter after final report; unpin first.
7. Controller after all created child roles are archived or explicitly marked `DONE`,
   and after the launcher acknowledges the terminal run-token handoff.
8. Launcher last, after delivering that final user handoff. Because the launcher
   cannot archive itself while issuing the handoff, mark it `DONE`; a host lifecycle
   action or external caller may archive it afterward. Never claim self-archival.

On explicit pause/stop, send one stop message to active tasks and stop immediately. Do not poll, read, test, clean, commit, archive, deploy, or update knowledge until the user explicitly resumes.

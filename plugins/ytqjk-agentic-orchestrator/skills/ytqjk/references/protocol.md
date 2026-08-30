# Orchestration protocol

## Activation and authority

For a bare activation without an actionable objective, stay in `GOAL_INTAKE` and ask
one objective question with a recommendation. Do not call tools, inspect the
workspace, load another skill, or create tasks before confirmation.

After confirmation, resolve the target directory and classify the work as
`GIT_READ_ONLY`, `GIT_MUTATION`, `NON_GIT_READ_ONLY`, or `NON_GIT_MUTATION`.
Block non-Git mutation. Inspect a Git worktree with read-only commands and preserve
unknown user changes. If overlapping dirty state makes the baseline ambiguous, ask
for direction; never stash, reset, delete, or absorb it.

Objective confirmation permits in-scope role coordination and local writes under the
resolved knowledge root. It does not authorize push, merge, rebase, tag, amend,
deployment, remote writes, service changes, hardware actions, or destructive cleanup.

## Roles

Create or reuse visible host tasks just in time. If the host cannot create, list,
read, wait for, and message tasks, report `BLOCKED`; do not replace them with role-play.

- Launcher: preserves the user objective and receives the terminal handoff.
- Controller: owns the plan and DAG; it does not implement, test, review, or mutate Git.
- Supervisor: independently returns `PASS`, `CORRECT`, or `BLOCK` on direction and scope.
- Worker: owns one bounded task and an exact read/write scope.
- Reviewer: independently checks a result and, for mutation, the staged integration.
- Git committer: is the only role allowed to stage and commit integrated changes.
- Progress reporter: reports evidence-backed progress in a separate visible task.
- RAG: is the only knowledge-index writer and returns source-aware receipts.

Use at most three Workers concurrently. Parallel tasks require satisfied dependencies,
disjoint write allowlists, and no shared mutable output. Create a reviewer only after a
reviewable result exists. Read-only objectives never create a Git role.

## Planning gate

Objective confirmation is not plan approval. Before dispatch, publish a dependency
DAG with outcomes, scopes, deliverables, acceptance checks, owners, parallel groups,
handoff boundaries, risks, and rollback. Weighted milestones must total 100 percent.
Obtain supervisor `PASS` and explicit plan approval before mutation dispatch. Material
scope or acceptance changes return to this gate.

Verified progress is the sum of milestone weights backed by reviewer `PASS`. Report
when it crosses each ten-percent boundary and report blocked or invalidated evidence
immediately. Never convert activity or elapsed time into progress.

## Knowledge and sessions

Before the first repository-answerable query, run:

```text
ytqjk rag bootstrap --project-root <work-directory> --vector-mode auto
```

Query on behalf of the requesting role:

```text
ytqjk session query --project-root <work-directory> \
  --session-id <host-session-id> --expected-project-id <project-id> <question>
```

Do not invent a shared session ID. `PROJECT_CACHE_HIT` ends the lookup. Only a project
miss may fall back to the approved global index. On `KNOWLEDGE_MISS`, research only
when the task permits it and submit sanitized, sourced content as a candidate:

```text
ytqjk knowledge intake --content-file <file> \
  --source-ref <source> --title <title>
```

Knowledge informs decisions but does not replace current source, tests, or review.
Never cross project-cache or session-anchor boundaries.

Checkpoint concise, secret-free session memory before compaction or archive:

```text
ytqjk session checkpoint --session-id <id> --project-id <project-id> \
  --memory-file <file>
```

When host archival is supported, run `prepare-archive`, archive the host task, then
run `finalize-archive` only after host confirmation. If archival is unavailable, keep
the active checkpoint and mark the role `DONE`; do not claim it was archived.

## Read-only loop

1. Dispatch one bounded Worker with exact read scope and acceptance evidence.
2. The Worker returns evidence without modifying files or Git.
3. The reviewer reruns relevant checks and returns `PASS`, `FAIL`, or `UNKNOWN`.
4. The supervisor verifies direction and scope. Record the result; no handoff bundle,
   staging, worktree, or commit is used.

## Git mutation loop

1. The Git role verifies a clean integration worktree and expected HEAD.
2. Dispatch a Worker in a separate worktree with an exact write allowlist.
3. The Worker edits and tests without staging or committing.
4. Export every changed path to a bundle outside the repository:

```text
ytqjk handoff export --repo <worker-worktree> --bundle <bundle-path> \
  --path <file-1> --path <file-2>
```

5. The reviewer checks the Worker result and bundle; the supervisor checks direction.
6. The Git role applies one reviewed bundle to a clean integration worktree:

```text
ytqjk handoff apply --repo <integration-worktree> --bundle <bundle-path>
```

7. Bind integration review to the returned staged-snapshot SHA-256. Run focused tests
   and `git diff --cached --check`.
8. Only after reviewer and supervisor pass may the Git role create a small local
   commit. Prove the integration worktree is clean before the next bundle.

The handoff runtime validates the base commit, manifest and payload hashes, path
allowlist, ignore rules, patch paths, and clean filters. It stages exact paths and
rolls back only its own writes on failure. It never commits or resolves conflicts.
Never use broad `git add` commands.

## Completion

Use `GOAL_INTAKE`, `BOOTSTRAP`, `GRILLING`, `PLAN_AUDIT`, `APPROVED`, `RUNNING`,
`REVIEWING`, `REWORK`, `GIT_GATE`, `BLOCKED`, and `DONE` accurately. Archive a role
only after delivery, evidence registration, downstream acknowledgement, and no pending
follow-up. Never archive active, blocked, or input-waiting work.

Close Workers first, then RAG, reviewer, Git, supervisor, progress, controller, and
launcher. The controller sends one idempotent terminal handoff to the launcher with
status, evidence, risks, and user-facing result. The launcher acknowledges it, reports
to the user, and is marked `DONE`; never claim self-archival.

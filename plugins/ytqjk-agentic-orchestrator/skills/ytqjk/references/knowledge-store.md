# Local knowledge store

## Root and layout

Resolve the knowledge root in this order: explicit `--knowledge-root`,
`YTQJK_KNOWLEDGE_ROOT`, `%LOCALAPPDATA%\YTQJK\knowledge` on Windows, then
`${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk` on Unix.

```text
<knowledge-root>/
  catalog.json
  service/
    knowledge.sqlite3
    orchestration.sqlite3
    orchestration.key
  global/
  personal-experience/
    candidates/
    approved/
  global-cache/
    manifest.json
    index.json
  projects/<project-id>/
    manifest.json
    index.json
    handoffs/
  sessions/<session-key>/anchor.json
```

The service database is durable. Project and global JSON indexes are rebuildable.
Windows, Linux, and WSL must not open one SQLite database concurrently across
filesystems. Use the active platform's local filesystem unless the user explicitly
chooses another root.

## Identity and safe indexing

Git projects use a normalized origin URL when available; otherwise they use the
canonical Git directory. Ordinary directories use their canonical path. A one-way
digest forms the project ID, and all worktrees of one remote share an identity.

Git indexing reads tracked files from `git ls-files`; non-Git indexing walks regular
files. Symbolic links, binaries, dependency/vendor trees, generated output, caches,
oversized files, `.env` files, private keys, tokens, credentials, and session material
are excluded. Remote userinfo, query, and fragment data are removed before storage.

The Go runtime currently performs lexical retrieval. `--vector-mode` remains a
compatible planning input, but receipts report lexical-only operation until a real
vector backend is configured. Never claim semantic-vector evidence from this release.

## Lookup and governance

Run one project lookup first. `PROJECT_CACHE_HIT` returns immediately. A miss may
search the approved global index and return `GLOBAL_FALLBACK_HIT`; a total miss returns
`KNOWLEDGE_MISS`. Candidates are never included in approved global retrieval.

External findings, dashboard intake, and archived session lessons enter candidate
state. Editing and soft deletion are limited to active candidates. Approval,
verification, tombstone transitions, immutable snapshots, and feedback are recorded
through the Go KnowledgeService. No query automatically promotes knowledge.

## Commands

```text
ytqjk rag init --project-root <directory>
ytqjk rag index --project-root <directory> --vector-mode auto
ytqjk rag index-global --vector-mode auto
ytqjk rag bootstrap --project-root <directory> --vector-mode auto
ytqjk session query --project-root <directory> --session-id <session-id> \
  --expected-project-id <project-id> <question>
```

Rebuild the project index after relevant source changes and the global index after an
approved promotion. A stale receipt must not substitute for current source on a
critical decision.

Submit a sourced candidate with:

```text
ytqjk knowledge intake --title <title> --content-file <file> \
  --source-ref <source>
```

## Dashboard

The dashboard binds only to `127.0.0.1`. Its write endpoints require a matching local
Host and Origin plus JSON content. It never auto-approves candidates.

```text
ytqjk dashboard status
ytqjk dashboard start
ytqjk dashboard stop
```

Open `http://127.0.0.1:8765`. Dashboard state is management metadata, not proof that
repository source is current.

## Session anchors

The `SessionStart` hook invokes `ytqjk hook session-start`. Anchors contain a one-way
session key, project ID, timestamps, and concise sanitized memory; they never store raw
session IDs or transcripts. Every session query refreshes the requesting session's
anchor and rejects a cross-project mismatch.

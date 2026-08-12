# Local knowledge store

## Layout

Resolve one knowledge root in this order:

1. explicit `--knowledge-root` command argument;
2. `YTQJK_KNOWLEDGE_ROOT`;
3. Windows: `D:\knowledge` when the D drive exists, otherwise
   `%LOCALAPPDATA%\YTQJK\knowledge`;
4. Linux/WSL2: `${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk`.

Use the resolved root for this layout:

```text
<knowledge-root>/
  config.json
  catalog.json
  models/
  .runtime/
  verified/
  personal-experience/
    candidates/
    approved/
  error-experience/
    candidates/
    approved/
  global-cache/
    manifest.json
    lexical.sqlite3
    vectors/
  projects/
    <project-name>--<short-hash>/
      manifest.json
      cache/
      handoffs/
      lexical.sqlite3
      vectors/
```

The personal root is durable. `global-cache` indexes only `verified` plus both `approved` areas. Each project directory is a rebuildable computer-local cache. Query both global and current-project caches, then fuse lexical and vector ranks. Never auto-delete caches; mark stale and report size/last access. Cache deletion requires explicit approval. Back up metadata before schema migrations.

WSL2 uses its Linux root by default. Only use `/mnt/d/knowledge` when the user
explicitly chooses to share it, and never open the same SQLite or LanceDB cache from
Windows and WSL concurrently. Prefer a root on the active Linux filesystem.

## Project identity

Normalize the Git remote URL and hash it for the short ID. If no remote exists, hash the canonical absolute project path. All worktrees of one remote share one project cache. Record remote, path aliases, HEAD, dirty state, schema version, and last index time in `manifest.json`.

## Index scope

Default to text files returned by `git ls-files`. Deny common credential paths such as `.env*`, `.npmrc`, `.pypirc`, `.netrc`, token/credential files, private keys, Terraform state/variable files, cloud credential directories, binaries, dependency/vendor directories, generated output, caches, and oversized files. Untracked files require an explicit allowlist. Reject files containing high-confidence private-key, provider-token, credential-URL, or secret-assignment patterns before chunking. This built-in scan is defense in depth, not a replacement for a repository-wide secret scanner; never index a repository that fails its dedicated scan. Never copy raw secrets into chunks, logs, errors, or reports.

Canonicalize network remote URLs before persistence: remove URL userinfo, query, and fragment components. Replace local and `file://` remotes with a basename plus one-way fingerprint so private local directory names are not stored. Never write raw remotes containing credentials or private local paths to manifests, catalogs, logs, or chunks.

Security schema changes invalidate existing lexical and vector caches. Re-run `index` and `index-global` before querying. Old cache files may still contain previously indexed data until the user explicitly approves their deletion; do not delete them automatically.

Mark entries `STALE` when HEAD or source hashes change. Critical answers require current source refresh. Every answer must cite source file and line/symbol plus indexed commit/time.

## Retrieval modes

Support `off`, `auto`, and `on`; default `auto`.

- `off`: lexical retrieval only.
- `on`: lexical plus local embedding/vector retrieval.
- `auto`: enable vectors only when indexed text is at least 10 MiB or chunks reach
  2,000. Repeated low-confidence queries never download a model for a small corpus;
  use `on` explicitly when semantic retrieval is wanted below those thresholds.

Store thresholds in `config.json` and allow project overrides. Run embeddings locally with FastEmbed and store vectors in embedded LanceDB. Cache models under `<knowledge-root>/models` and isolated Python dependencies under `<knowledge-root>/.runtime`; do not modify system Python or run a permanent service. Before first download, report model name/version/estimated size. Use flat search below 50,000 chunks and create a local HNSW-SQ index at or above that size. If vector setup fails, report the downgrade and keep lexical retrieval; never claim vector evidence.

## Knowledge promotion

Write raw discoveries, task state, errors, and hypotheses only to the project cache. Verified reusable facts may enter `verified` with source and review evidence.

Generate personal lessons under `personal-experience/candidates`; only explicit user approval promotes them to `approved`. Generate reviewed error lessons under `error-experience/candidates`; require confirmed root cause, effective fix, and passing verification before user-approved promotion to `approved`. Mark unresolved hypotheses `UNVERIFIED` and never promote them.

Show candidate approvals only in the RAG task. Do not interrupt controller or progress tasks.

## Operational commands

Run these from the skill directory with Python 3.10 or newer. First inspect the
isolated runtime; install its pinned packages only when the check reports not ready.

Windows PowerShell:

```powershell
$env:YTQJK_KNOWLEDGE_ROOT = 'D:\knowledge'
python scripts/bootstrap_runtime.py --check
python scripts/bootstrap_runtime.py
```

Use the isolated interpreter afterward:

```powershell
$ragPython = Join-Path $env:YTQJK_KNOWLEDGE_ROOT '.runtime\Scripts\python.exe'
$repo = 'C:\absolute\path\to\repo'
& $ragPython scripts\rag_cli.py init --project-root $repo
& $ragPython scripts\rag_cli.py index --project-root $repo --vector-mode auto
& $ragPython scripts\rag_cli.py index-global --vector-mode auto
& $ragPython scripts\rag_cli.py query --project-root $repo "<question>"
```

Linux or WSL2 Bash:

```bash
export YTQJK_KNOWLEDGE_ROOT="${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk"
python3 scripts/bootstrap_runtime.py --check
python3 scripts/bootstrap_runtime.py

rag_python="$YTQJK_KNOWLEDGE_ROOT/.runtime/bin/python"
repo="/absolute/path/to/repo"
"$rag_python" scripts/rag_cli.py init --project-root "$repo"
"$rag_python" scripts/rag_cli.py index --project-root "$repo" --vector-mode auto
"$rag_python" scripts/rag_cli.py index-global --vector-mode auto
"$rag_python" scripts/rag_cli.py query --project-root "$repo" "<question>"
```

Before a first vector build, the RAG task must report the configured model, package versions, estimated download, and cache path. `off` never loads vector dependencies; `auto` builds them only after a configured threshold; `on` requests them immediately. Re-run project indexing after HEAD changes and global indexing after an approved knowledge promotion.

Before the first query in a run, the RAG task must inspect both manifests. Run
`index` when the project cache is missing, stale, or on an older security schema;
run `index-global` when the global cache is missing, stale, or on an older security
schema. Then query once. If both current caches contain no matching evidence,
report that result instead of repeating queries to force vector activation.

## Read-only dashboard

Use the bundled local dashboard to inspect project cache metadata plus verified,
approved, and candidate Markdown entries. It binds only to `127.0.0.1` and never
indexes, promotes, deletes, or modifies knowledge:

```text
python dashboard/knowledge_dashboard.py --knowledge-root <knowledge-root>
```

Open `http://127.0.0.1:8765`. Candidate entries must remain visibly labeled
`CANDIDATE`; the dashboard is not evidence of approval or current-source state.

The dashboard can accept a local file or pasted text into
`personal-experience/candidates/imports`. It permits `.md`, `.txt`, `.json`,
`.yaml`, `.yml`, `.csv`, `.log`, `.rst`, `.docx`, `.pptx`, `.xlsx`, `.png`,
`.jpg`, `.jpeg`, `.gif`, and `.webp` up to 10 MiB. Modern Office files have their
text and table content extracted; image files record their dimensions but do not
perform OCR. The original upload is retained under `imports/originals` alongside
the candidate Markdown analysis. Before saving, the dashboard rejects sensitive
filenames and high-confidence secret patterns in extracted text. Intake never
approves, promotes, or indexes the candidate.

It also accepts common UTF-8 source, configuration, and data files, including
`.py`, `.ts`, `.tsx`, `.js`, `.java`, `.go`, `.rs`, `.sql`, `.xml`, `.toml`,
`.ini`, `.properties`, `.sh`, `.ps1`, `.diff`, `.jsonl`, `.svg`, and related
web styles and markup. The dashboard deliberately does not accept arbitrary
extensions or legacy binary Office files (`.doc`, `.ppt`, `.xls`).

Candidate Markdown entries can be edited or deleted from the dashboard. These
operations are limited to the two `candidates` directories; verified and approved
knowledge has no edit or delete endpoint. Deleting an imported candidate also
removes its safely linked original attachment.

Every new intake is automatically assessed for approval readiness. The check
requires usable extracted text, at least 200 non-whitespace characters, and a
source, evidence, or validation signal. It records `READY_FOR_REVIEW` or
`NOT_READY` with reasons in the candidate Markdown and in the dashboard status.
This assessment does not approve, promote, or index the candidate; approval still
requires an explicit user decision and RAG review.

Each external intake is also stored as a candidate knowledge package: the original
attachment is retained, a candidate overview records extraction and approval
assessment, and usable text is split into Markdown knowledge chunks. Splitting
prefers Markdown headings, then paragraph boundaries, with chunks capped at about
1,800 characters. Every chunk records its parent intake ID, source filename,
sequence number, and summary. Deleting the overview removes its generated chunks
and linked original attachment; this package remains in candidates until approval.

## Session anchors

YTQJK-created or reused Codex conversations can be anchored in
`<knowledge-root>/sessions/<session-key>/`.
The session key is a one-way hash; the anchor stores project identity, activity times,
and a concise sanitized memory, never raw session IDs or full transcripts. On context
compaction, the role checkpoints then restores this memory. On archive, it exports a
candidate experience record. A scheduled `sweep --days 30` can archive inactive
anchors; it requires a host scheduler and never approves or indexes resulting records.
Every knowledge query uses the same session key to refresh one anchor; duplicate
anchors and duplicate exports of an unchanged memory are prevented.

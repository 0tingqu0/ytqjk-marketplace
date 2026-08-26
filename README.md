# YTQJK Agentic Orchestrator

English | [简体中文](README.zh-CN.md)

A Codex multi-task orchestrator for complex projects. It provides plans weighted by natural task size, parallel workers, independent supervision and review, a sole Git committer, a dedicated progress reporter, and a local agentic RAG knowledge cache. The orchestrator itself does not implement project changes.

The knowledge-base scripts support Windows, Linux, and WSL2. The Codex IDE extension in VS Code, Cursor, and Windsurf can load standalone skills but not plugins. Full multi-session orchestration also depends on whether the host exposes visible session APIs.

## Recommended: one-command deployment after Git clone

Use the argument-free installer in the repository root. It uses the cloned repository as both the installation target and the project to index, installs two Codex plugins, copies project-level skills, imports Codex candidate material that the current user permits, builds the project knowledge index, and registers the knowledge dashboard as a background process that starts when the current user signs in. The installer writes only to the current user's directories and the local knowledge root. It does not require administrator or root privileges, and it does not download the publisher's private knowledge.

### 1. Check the runtime environment

On Windows, only Git must be installed beforehand. `install.cmd` bypasses the PowerShell execution policy for this installation process only; it does not change system or user policy. If Python 3.11+ is missing, `install.ps1` silently installs per-user Python 3.12 through `winget`. If Node.js, npm, `npx`, or Codex CLI is missing, `install.ps1` prepares portable Node.js 24.15.0 under `%LOCALAPPDATA%\YTQJK\runtime`, verifies the official Node.js `SHASUMS256.txt`, and installs the pinned `@openai/codex@0.147.0`. This runtime needs no administrator access, does not modify the system PATH, is used only by the installation process, and is verified and reused on later runs.

Check Git first. If Python is already installed, check its version as well:

```powershell
git --version
python --version
```

On Linux, macOS, or WSL, use `python3` instead of `python`, and provide Node.js/npm, `npx`, and Codex CLI in advance. With Remote SSH, Dev Containers, or WSL, run the checks and installer inside the remote host, container, or WSL terminal. A Windows-host installation does not count as a remote installation.

### 2. Clone and install

Windows PowerShell:

```powershell
$repo = Join-Path ([Environment]::GetFolderPath('Desktop')) 'ytqjk-marketplace'
git clone https://github.com/0tingqu0/ytqjk-marketplace.git $repo
& "$repo\install.cmd"
```

The default installation directory is `ytqjk-marketplace` on the actual system desktop. `install.cmd` automatically uses the repository containing the script as both the installation target and the knowledge-index project, so there is no need to run `cd` first. To install elsewhere, replace `$repo` with the desired absolute path.

You can also invoke PowerShell explicitly. Keep the process-scoped execution-policy option:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "$repo\install.ps1"
```

Linux, macOS, or WSL:

```bash
git clone https://github.com/0tingqu0/ytqjk-marketplace.git
cd ytqjk-marketplace
sh ./install.sh
```

The installer immediately prints a startup message and forwards dependency-download and Codex-plugin installation output in real time. A first Windows installation may access `winget` sources, `nodejs.org`, the npm registry, GitHub, and Codex plugin sources. It downloads and runs Python, Node.js, the pinned Codex CLI, Codex plugins, and the third-party `skills` CLI as needed, so run it only on a trusted network. The first run may take several minutes. Do not close the terminal while output is still being produced. A successful installation exits with code `0` and ends with a concise receipt; append `--json` for troubleshooting or automation.

### 3. Verify the installation

A first complete deployment receipt should satisfy all of the following:

- `apply.status` is `APPLIED`.
- `cli_runtime.status` is `SYSTEM`, `BOOTSTRAPPED`, or `REUSED`. The latter two indicate a portable runtime in the user's directory.
- `knowledge_bootstrap.status` is `SUCCEEDED`.
- `knowledge_import.status` is `SUCCEEDED`; a repeated install may report `SKIPPED_MARKER`. If a few historical files cannot be parsed, the status is `SUCCEEDED_WITH_WARNINGS`; usable material is still imported and plugin/project knowledge deployment is unaffected.
- `knowledge_import.discovered_count` equal to `0` means the current user has no importable material; it is not an installation failure.
- `apply.codex_plugins.stable_paths` contains both stable plugin directories.
- `dashboard_service.status` is `RUNNING`, and `dashboard_service.autostart` is `INSTALLED`.

Windows PowerShell can further verify the stable plugins and knowledge layout:

```powershell
$knowledgeRoot = if (Test-Path 'D:\') {
  'D:\knowledge'
} else {
  Join-Path $env:LOCALAPPDATA 'YTQJK\knowledge'
}

Test-Path "$HOME\.codex\plugins\ytqjk-agentic-orchestrator\.codex-plugin\plugin.json"
Test-Path "$HOME\.codex\plugins\ytqjk-knowledge\.codex-plugin\plugin.json"
Test-Path "$knowledgeRoot\config.json"
Test-Path "$knowledgeRoot\catalog.json"
Get-ChildItem "$knowledgeRoot\projects" -Directory
```

Linux, macOS, or WSL:

```bash
knowledge_root="${YTQJK_KNOWLEDGE_ROOT:-${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk}"

test -f "$HOME/.codex/plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json"
test -f "$HOME/.codex/plugins/ytqjk-knowledge/.codex-plugin/plugin.json"
test -f "$knowledge_root/config.json"
test -f "$knowledge_root/catalog.json"
find "$knowledge_root/projects" -mindepth 1 -maxdepth 1 -type d -print
```

After installation, open `http://127.0.0.1:8765`. Closing the installation terminal does not stop the dashboard. The installer also writes a clearly marked YTQJK-managed block to the global Codex `AGENTS.md`, or to a non-empty `AGENTS.override.md` when one exists. New sessions then discover the knowledge base automatically, register the current Git project or ordinary directory, and create an anonymous anchor. Uninstallation removes only the managed block and preserves the user's existing content. Restart Codex to load the changes.

You can also enter `/hooks` and review and trust YTQJK's `SessionStart` hook so anchoring completes before the first response. Codex security policy prevents installers from trusting plugin hooks on a user's behalf. Leaving the hook untrusted does not prevent the global guidance from connecting the knowledge base before the first project action in a new session.

YTQJK manages only explicitly marked blocks and does not copy or rewrite the user's global rules. When moving `AGENTS.md` between computers, do not retain absolute user paths such as `C:\Users\some-user\...`. Optional global memory should use a current-user path such as `~/.codex/mem.md`. That file is unrelated to knowledge queries, and a complete `KNOWLEDGE_RECEIPT` must not be shortened to the anchor value alone.

### 4. Custom installation

To preview changes or specify the installation target and indexed project independently, pass explicit arguments. Parameterized runs remain dry-runs by default:

```bash
python3 setup.py --mode all --target-root /path/to/install \
  --project-root /path/to/project --json
```

Apply explicitly after review:

```powershell
.\install.cmd --mode all --target-root C:\path\to\install `
  --project-root C:\path\to\project --apply --yes --json
```

`--target-root` controls only the installation location and never implicitly becomes the project to index. Project indexing reads only the explicit `--project-root`; without it, the receipt reports `NOT_CONFIGURED`. Use `--project-bootstrap off` to skip project-index initialization. A normal successful apply exits with `0`; warnings for individual files during the default automatic import also exit with `0`. An unrecoverable candidate-import failure or a `--codex-import force` parse failure exits with `3`, project-index initialization failure with `4`, dashboard configuration failure with `5`, global knowledge-guidance configuration failure with `6`, and installation or argument errors with `2`. JSON receipts do not contain absolute project paths or knowledge content.

On Windows, the installer prefers a per-user scheduled task for the persistent dashboard. If the system rejects task creation, it falls back to the current user's Startup folder without requiring administrator access.

## Uninstall earlier versions

The current installer can remove any installed YTQJK version. By default it only previews the plugin and skill directories to be removed; apply after review. Uninstallation removes only YTQJK's Codex plugins, marketplace, and skill directories. It does not delete third-party `grill-me`, knowledge data, the `%LOCALAPPDATA%\YTQJK\runtime` portable runtime, or Python installed through `winget`. It also removes only the marked YTQJK-managed block from the global `AGENTS.md`. If the runtime or Python must be removed, first confirm that no dashboard or installer process is running, then uninstall them separately.

```powershell
.\install.cmd --uninstall
```

This entry point uses the current repository as `--target-root` and supplies `--mode all --apply --yes`. To narrow the scope, append `--mode codex-only`, `--mode ide-only`, or `--mode knowledge-only`. Do not copy placeholder paths such as `C:\path\to\project` literally.

Installation and uninstallation print concise results by default. Append `--json` only when diagnosing failures or consuming the structured receipt in automation.

On the first successful `all` or `knowledge-only` apply, the installer imports Codex material as candidates by default. It resolves the source root in this order: `--codex-root`, `CODEX_HOME`, then `~/.codex`. It processes only `mem.md` and supported files under `memories/`, `knowledge/`, and `attachments/` that pass safety checks. Extensionless regular files under `memories/` are accepted only as strict UTF-8 text. Office files, images, and audio without a configured parser increase `not_configured_count` and are not falsely reported as imported. Credential-, token-, secret-, auth-, config-, session-, log-, cache-, plugin-, skill-, worktree-, and archive-named families are permanently excluded and are not opened.

Every new source enters the store as an independent `CANDIDATE` proof. Even if its content duplicates approved or verified material, it does not inherit approval or modify the approved version's source list.

The destination is resolved in this order: `--knowledge-root`, `YTQJK_KNOWLEDGE_ROOT`, then the platform default. Candidates are written to the `global-candidates` scope in `<knowledge-root>/service/knowledge.sqlite3`, separate from retrieval-cache databases. Use `--codex-import off` to disable import or `--codex-import force` to retry a marked import. Dry-runs do not read Codex material. Default `auto` mode isolates an unparseable historical file and continues importing other safe material. The receipt becomes `SUCCEEDED_WITH_WARNINGS`, preserves `parse_failed_count`, `failure_stage=PARSING`, and `failure_code=PARSE_FAILED`, and exits with `0`. `--codex-import force` remains strict so users can repair the source and retry. An unrecoverable import failure does not roll back successfully installed files: `apply.status` remains `APPLIED`, `knowledge_import.status` becomes `FAILED`, and the process exits with `3`.

## Codex desktop and CLI

To install only the plugins without initializing an index for a specified project:

```powershell
codex plugin marketplace add 0tingqu0/ytqjk-marketplace
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

This installs both the orchestrator and the local knowledge plugin. Restart Codex, create a new task, and enter `$ytqjk`; you can also select `ytqjk` through `/skills`. Use `/ytqjk` only when the current host explicitly exposes that shortcut; it is not the portable entry point. A bare invocation without a clear objective remains in the active task and asks one objective question at a time, including a recommended answer. It does not call tools or create orchestrator or role sessions. If the invocation already contains an actionable objective, or you later provide one, it does not ask for confirmation again. Only the first tool call after objective confirmation reads the protocol and creates the orchestrator. Objective confirmation is not plan approval: the later `grill-me`, supervision, and plan-approval gates still apply.

### Host capability boundary

| Usage | Loadable content | Full multi-session orchestration |
| --- | --- | --- |
| Codex desktop | Plugins and bundled skills | Available when the host exposes session create, list, read, wait, and message APIs; missing titles affect only cross-run reuse |
| Codex CLI | Marketplace plugins and bundled skills | Requires the same visible core session APIs; returns `BLOCKED` when capability checks fail |
| Codex IDE extension | Project-level standalone skills; no plugin loading | Requires the same visible core session APIs; returns `BLOCKED` when capability checks fail, so use a desktop or CLI host that exposes them |

A successful installation proves only that the skill can be discovered. It does not prove that the host supports full multi-session control. Pinning and archiving are optional enhancements. When absent, the progress task remains visible and completed tasks are marked `DONE`. YTQJK does not bypass core capability checks with hidden agents or role-play inside the current session.

## VS Code, Cursor, and Windsurf

The Codex IDE extension currently loads standalone skills but not plugins. Run the following project-level installation in the target project's terminal. With Remote SSH, Dev Containers, or WSL, run it inside the remote, container, or WSL workspace terminal:

```bash
npx --yes skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/main/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

Reload the IDE or create a new chat, then enter `$ytqjk`; you can also select `ytqjk` through `/skills`. Do not use `/ytqjk` in the IDE.

`skills@latest` is a third-party CLI maintained by Vercel. It downloads and executes the latest code and writes to the project skill directory, so inspect the source before installation. Project-level installation affects only the current repository.

## Updates and rollback

The dashboard always displays the local version in the upper-left corner. When a newer version is available, the version changes color and opens an update action. If GitHub is temporarily unavailable, the local version remains visible. If a background restart expires the update token, the page refreshes the token and retries once. The backend performs one delayed, windowless restart only after the response stream closes. If operating-system timing still interrupts the connection, the page checks the installed version after recovery and reports success only when the version actually changed; it no longer shows a successful installation as `Failed to fetch`. Updates from `0.4.8` are handled directly and do not require manually stopping the knowledge service.

Dashboard updates use an internal stable-plugin-only path. They replace only the two manifest-owned Codex plugin directories and do not invoke the Codex Marketplace or Codex CLI. Knowledge-service configuration and the knowledge root—including candidates, approved material, indexes, models, and imports—remain outside the update transaction and are neither migrated nor rebuilt. Run the full installer explicitly when those components need to change.

Windows users who deployed through Git clone should reuse the desktop directory from the first installation. These commands work from any directory. The installer updates manifest-owned plugin directories and reuses the knowledge base without deleting candidate or approved material:

```powershell
$repo = Join-Path ([Environment]::GetFolderPath('Desktop')) 'ytqjk-marketplace'
git -C $repo pull --ff-only
& "$repo\install.cmd"
```

If the first installation used a custom `$repo`, use that same absolute path for updates.

Linux, macOS, or WSL:

```bash
git pull --ff-only
sh ./install.sh
```

In Codex desktop, uninstall and reinstall from the Plugins page. In Codex CLI, enter `/plugins`, uninstall from the `ytqjk` marketplace, and reinstall. You can also upgrade the configured marketplace and install again:

```bash
codex plugin marketplace upgrade ytqjk
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

Create a new task after installation; existing tasks do not reload bundled skills. The current release version is pure SemVer `0.6.3`. `+codex.*` is only for refreshing local development caches and is neither committed nor included in release manifests.

After updating project-level IDE skills, reload the IDE or create a new chat:

```bash
npx --yes skills@latest update ytqjk caveman -p
```

If the update command cannot identify an older installation record, run the project-level `skills add` command again.

When a corresponding release tag exists, the marketplace can be pinned back to that tag. This rollback is unavailable if the publisher did not create the tag:

```bash
codex plugin marketplace remove ytqjk
codex plugin marketplace add 0tingqu0/ytqjk-marketplace --ref <release-tag>
codex plugin add ytqjk-agentic-orchestrator@ytqjk
codex plugin add ytqjk-knowledge@ytqjk
```

For an IDE rollback, reinstall the skills path from the same tag:

```bash
npx --yes skills@latest add https://github.com/0tingqu0/ytqjk-marketplace/tree/<release-tag>/plugins/ytqjk-agentic-orchestrator/skills --agent codex --skill ytqjk --skill caveman --copy
```

## Requirements

- Git. Linux, macOS, and WSL also require Python 3.11 or later; Python 3.12 is recommended. On Windows, the installer uses `winget` to install per-user Python 3.12 if Python is missing.
- If Node.js/npm, `npx`, or Codex CLI is missing on Windows, the installer uses a per-user portable runtime. Linux, macOS, and WSL users must install these commands themselves.
- Node.js/npm must satisfy `npm view skills@latest engines`. The current Windows bootstrap uses Node.js 24.15.0 and pins Codex CLI 0.147.0. At release time, `skills@1.5.22` requires Node.js 22.20.0 or later.
- Linux requires a working Python `venv` module, commonly provided by `python3-venv` on Ubuntu/Debian.
- The first deep image or PDF analysis creates an isolated Python runtime in the knowledge root, installs pinned direct dependencies, and downloads the required official models. Verified runtimes and models are reused idempotently.
- Transitive dependencies are determined by the first installation's `pip` resolution. After installation, the complete distribution inventory, required imports, CPU provider, and a SHA-256 hash of the runtime tree are verified and sealed. One installation can therefore detect drift, but first-time installations on different dates or platforms are not byte-for-byte reproducible builds.
- The document runtime includes Docling, RapidOCR, PaddleOCR, PyTorch, and local vision models. The first download and installation may transfer several GiB and consume more disk space. It may take minutes or tens of minutes depending on the network, CPU/GPU, and available Python wheels. Offline environments must prepare a complete local runtime and models with verified hashes.
- Windows prefers `D:\knowledge`; without a D drive it uses `%LOCALAPPDATA%\YTQJK\knowledge`.
- Linux/WSL2 uses `${XDG_DATA_HOME:-$HOME/.local/share}/ytqjk`.
- `YTQJK_KNOWLEDGE_ROOT` overrides either platform default. WSL does not automatically reuse the Windows cache. Do not let Windows and WSL open the same SQLite/LanceDB cache concurrently.
- Before the first RAG query, missing, stale, or security-incompatible project and global indexes are refreshed. `auto` enables vectors only when text reaches 10 MiB or 2,000 chunks; repeated empty queries on a small knowledge base do not download models.
- Normal queries do not rescan the entire global store. They validate only source files actually matched. Each query waits at most 60 seconds and returns a retryable timeout instead of blocking a session indefinitely.
- Every session queries the current project cache first. A hit ends the lookup; a miss falls back to the global store. A global hit is cached into the current project. If the global store also misses, the result is `KNOWLEDGE_MISS`; the current session performs external research and submits sanitized findings through the candidate interface. A session cannot switch projects or read another project's cache. Each project cache has a 1 GiB capacity and uses LFU+LRU eviction to retain frequently reused knowledge.
- Only when the current user's configuration has no `grill-me` does the orchestrator run `npx --yes skills@latest add mattpocock/skills --agent codex --skill grill-me --yes --copy` after confirmation. Warm starts perform no npm or network check. Vector search installs isolated dependencies and downloads local models only after the relevant information is confirmed.

## Knowledge dashboard

The local dashboard displays the knowledge root, project indexes, verified/approved experience, and candidate experience. It can submit, edit, delete, or manually approve candidates. The one-command installer starts the dashboard immediately in the background and registers per-user login autostart. Closing the installer terminal does not stop it.

Windows PowerShell can inspect, restart, or stop the service:

```powershell
$service = "$HOME\.codex\plugins\ytqjk-agentic-orchestrator\skills\ytqjk\dashboard\dashboard_service.py"
python $service status
python $service start
python $service stop
```

Linux, macOS, or WSL2:

```bash
service="$HOME/.codex/plugins/ytqjk-agentic-orchestrator/skills/ytqjk/dashboard/dashboard_service.py"
python3 "$service" status
python3 "$service" start
python3 "$service" stop
```

Open `http://127.0.0.1:8765`. The service binds only to the local loopback interface. You can drag, select, or paste text, Word (`.docx`), PowerPoint (`.pptx`), Excel (`.xlsx`/`.csv`), common image formats, and audio files up to 10 MiB. Office text and tables are extracted. WAV files record channels, sample rate, and duration; other audio records format only. Audio is not transcribed. Original files are retained with candidate analysis records under `imports/originals`. Candidates are never automatically approved, never enter `verified`, and never trigger automatic reindexing. Sensitive filenames and high-confidence credentials in extracted text are rejected.

Images are first processed with RapidOCR. Images with no text, short pages with low confidence, or at least 20% low-confidence text blocks are checked again with PP-OCRv6. Image classification uses DocumentFigureClassifier-v2.5 and attempts to combine it with a local SmolVLM-256M-Instruct description for summaries and tags. If the description format is invalid or cannot be verified, OCR and classification results are preserved and the item is sent for manual review. OCR text, summaries, and tags all become structured searchable knowledge chunks. Inference uses only local models verified against manifest hashes. Missing required models or incomplete integrity evidence fail closed.

The automatic installation pins direct dependency versions, verifies the full runtime, and uses CPU inference only. It does not install `onnxruntime-gpu`. Every Hugging Face model is pinned to a commit SHA, and the RapidOCR dictionary is verified with SHA-256.

PDF processing uses Docling to distinguish native-text, scanned, and mixed pages while preserving tables, physical page numbers, coordinates, and confidence evidence. Scanned pages with no text, measured low confidence, or complex layouts are checked with PaddleOCR; complex layouts are further analyzed by the eight local PP-StructureV3 models. Normal high-confidence pages are not processed twice. An all-zero recognizer confidence is treated as “confidence not reported”: text is preserved and sent for manual review instead of being mislabeled as measured low confidence. Embedded PDF images retain their positions but do not use SmolVLM by default and are marked for manual review. Missing required models, version mismatches, or checksum failures return `NOT_CONFIGURED`; they do not fetch models from the network or claim secondary recognition succeeded.

The dashboard also accepts common UTF-8 source, configuration, and data formats such as `.py`, `.ts`, `.java`, `.go`, `.sql`, `.xml`, `.toml`, `.ini`, `.sh`, `.ps1`, `.diff`, `.jsonl`, and `.svg`. Convert legacy binary Office formats (`.doc`, `.ppt`, `.xls`) to their modern equivalents before submission. Candidates can be edited or deleted from the dashboard. Verified and approved knowledge has no such entry point. Deleting a submitted item also deletes its associated original file.

### LAN knowledge collaboration

The Knowledge Tree page can initialize a separate LAN knowledge service, generate a one-time shared secret, configure authorized computers, and explicitly query and read material from authorized remote nodes and their local subtrees in the same project. The web admin interface always remains loopback-only. The LAN service is also loopback-only by default. It is exposed to the LAN only after a private-subnet IP is entered, the unencrypted-HTTP warning is acknowledged, and the dashboard is restarted. HTTP requests and successful responses use separate HMAC authentication and integrity checks. Request nonces are persisted so replays remain rejected after a service restart. HMAC does not encrypt content; use an HTTPS reverse proxy or VPN across untrusted networks. The plugin does not change firewall rules, broadcast discovery, or establish trust automatically. The fixed address of each authorized computer must be configured explicitly.

Every authorized connection has two directional scopes. `export_node_ids` contains one or more local roots exposed to the peer, while the optional `remote_node_id` is the remote root this computer selected for access. An accessed computer can therefore save an inbound-only authorization first. The accessing computer then authenticates to fetch only that peer's advertised `export_nodes` and selects one before enabling outbound queries. Legacy single-root `export_node_id` settings migrate on read. Cross-computer queries can read only an authorized root and its local descendants. They never traverse upward to a parent store, read unrelated siblings or other project stores, or forward through a mounted node to a third computer. Remote material reads revalidate that the requested root is still advertised and that the actual source node remains inside its authorized subtree. Saved secrets are never displayed again. Leaving the secret blank while editing preserves it; entering a new value rotates it. Ordinary session queries continue to use only the current project cache and the global fallback chain. Merely configuring a LAN connection never enables automatic cross-computer retrieval.

After startup, the dashboard checks the latest stable GitHub Release of `0tingqu0/ytqjk-marketplace`. The current version remains visible in the upper-left corner. When a newer version is found, the version changes color and exposes an Update button. After confirmation, the dashboard downloads a release archive from the fixed repository, validates safe paths and matching versions in both plugin manifests, then calls the atomic installer to update the stable plugin directories under `~/.codex/plugins`. Failures preserve the current version and never delete knowledge data. After the browser response completes, the dashboard restarts in the background without opening a window, and new Codex tasks load the updated plugin. Drafts, prereleases, non-pure-SemVer tags, and alternative download locations are excluded. Versions `0.3.2` and earlier have no web updater; first follow [Updates and rollback](#updates-and-rollback) to run `git pull --ff-only` and the installer, upgrading to `0.4.0` or later.

Every submission is automatically evaluated for approval readiness, and the decision and reasons are stored with the candidate. Readiness requires parseable content, at least 200 meaningful characters, and source, evidence, or verification clues. Passing this evaluation means only “ready for approval review”; it never automatically enters `approved` or the global index.

External material is stored as a candidate package. The original is retained, an overview records the analysis, and the body is split by headings and paragraphs into knowledge chunks of about 1,800 characters or fewer. Every chunk records the source file, chunk number, and parent material ID. Deleting the overview also deletes its chunks and original file.

After one-command installation, the installer copies two stable per-user directories from the cloned release package and manages them through a manifest: `~/.codex/plugins/ytqjk-agentic-orchestrator` and `~/.codex/plugins/ytqjk-knowledge` (`$HOME\.codex\plugins\...` on Windows). These directories do not depend on Codex marketplace versioned caches. Repeated installation safely updates only project-managed directories, and uninstallation removes only those two manifest-owned directories.

## Session anchoring

After the user trusts the Codex plugin's `SessionStart` hook, sessions in Git projects and ordinary directories, plus orchestrator, supervisor, reviewer, Git, progress, RAG, and worker sessions created by YTQJK, receive anonymous anchors in the knowledge root. Ordinary directories do not need `git init`; the current working directory becomes a separate project cache and can query the global knowledge store.

Compaction recovery retrieves a sanitized task summary for that session. Archiving writes reusable experience to the candidate area. Anchors do not store raw session IDs or full conversations. Every knowledge call in a session refreshes the same anonymous anchor instead of creating duplicates or re-exporting unchanged experience. On hosts that support scheduled tasks, use the following commands to sweep sessions inactive for 30 days that have summaries.

Windows PowerShell:

```powershell
python plugins\ytqjk-agentic-orchestrator\skills\ytqjk\scripts\session_memory.py sweep --days 30
```

Linux or WSL2:

```bash
python3 plugins/ytqjk-agentic-orchestrator/skills/ytqjk/scripts/session_memory.py sweep --days 30
```

When Codex does not expose global new-session, automatic-compaction, and idle-event subscriptions to plugins, the plugin cannot observe every session. In that case, anchoring and cleanup apply only to sessions created and managed through the YTQJK lifecycle.

After `$ytqjk` is enabled and the objective is confirmed, the orchestrator first tries to reuse existing unarchived `[YTQJK][project ID]` role tasks for the current project and restores their active anchor summaries. It creates new tasks only when no suitable task exists, the old task or anchor is archived, the task remains unreachable, or role responsibilities conflict. It uses Codex task collaboration and does not replace the current task with an agent.

## Local data and security

- The plugin stays lightweight after objective confirmation. On the first question that the project can answer, or the first indexing request, it builds the complete knowledge base for the current session directory: a separate project cache, an index of safe project text, and a global index containing only verified or approved content. The directory need not be a Git repository, and candidates are never approved automatically. Git projects index tracked text only. Ordinary directories safely scan regular text files. Both modes exclude common sensitive paths and high-confidence secrets before chunking; this does not replace the project's own secret scanning.
- The knowledge root stores source-retrieval caches, absolute project paths, sanitized network remotes or local-remote fingerprints, models, and isolated runtimes.
- Project knowledge stores are rebuildable caches. At the 1 GiB hard limit, cached global knowledge is evicted with LFU+LRU. Rebuildable vector and lexical indexes are dropped only when necessary. Old caches created before a safety-rule update are not automatically deleted, and every manual deletion still requires explicit approval.
- The knowledge root, models, SQLite/vector databases, handoffs, and all local credentials are excluded from this repository.

Before installation, inspect the [plugin manifest](plugins/ytqjk-agentic-orchestrator/.codex-plugin/plugin.json), [orchestration protocol](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/protocol.md), and [knowledge-store documentation](plugins/ytqjk-agentic-orchestrator/skills/ytqjk/references/knowledge-store.md).

## Attribution

- Plan interrogation uses Matt Pocock's [`grill-me`](https://github.com/mattpocock/skills), installed only when missing through the user-approved `npx --yes skills@latest add mattpocock/skills` command. Copyright and licensing remain with the original author.
- Concise output uses an MIT-licensed snapshot of Matt Pocock's historical `caveman` skill. The current upstream no longer includes this skill, so the plugin distributes an audited version. See [Third-Party Notices](plugins/ytqjk-agentic-orchestrator/THIRD_PARTY_NOTICES.md) for source, modifications, and license details.
- Skill installation commands use Vercel Labs' open-source [`skills` CLI](https://github.com/vercel-labs/skills). Plugin and skill structures follow the [OpenAI Codex Plugins](https://developers.openai.com/codex/plugins) and [Skills](https://developers.openai.com/codex/skills) specifications.
- OpenAI's `plugin-creator` is used only for this project's scaffolding, cache busting, and validation. It is not a runtime dependency. Except for the explicitly listed skills, this plugin embeds no third-party plugin code.

## License

[MIT](LICENSE), Copyright (c) 2026 一听曲就困.

# YTQJK Marketplace

[简体中文](README.zh-CN.md)

YTQJK is a local-first Codex orchestration and knowledge marketplace. Version 0.6.10
uses one cross-platform Go binary for installation, plugin hooks, local RAG, session
anchors, the versioned SQLite service, dashboard APIs, orchestration identity, and
reviewed Git handoffs. The dashboard remains ordinary static HTML, CSS, and JavaScript.

## Requirements

- Windows 10/11, Linux, macOS, or WSL2
- Git for project identity and handoff workflows
- Go 1.25 or newer for development

The installation scripts use an existing compatible Go toolchain or download the
official Go 1.27.0 archive and verify its SHA-256 before building YTQJK. No Python
runtime, virtual environment, or package installation is used.

## Install

Windows:

```powershell
.\install.ps1
```

Linux or macOS:

```sh
sh ./install.sh
```

The no-argument path builds the `ytqjk` binary, installs both plugins, bootstraps the
current project index, imports safe Codex knowledge candidates, and starts the local
dashboard. Preview a plan without changing state:

```powershell
.\install.ps1 --mode all --target-root . --json
```

Mutation requires all of `--target-root`, `--apply`, and `--yes`. Uninstall preserves
the knowledge database:

```powershell
.\install.ps1 --uninstall
```

## CLI

```text
ytqjk install [options]
ytqjk uninstall [options]
ytqjk rag <init|index|index-global|bootstrap|query> [options]
ytqjk session <query|anchor|checkpoint|inspect|prepare-archive|finalize-archive> [options]
ytqjk knowledge <create-project|create-candidate|edit|delete|state|snapshot|feedback|search|intake|workbench> [options]
ytqjk dashboard <serve|start|stop|status|restart> [options]
ytqjk orchestration <start-run|show-run|transition|grant|attest|verify> [options]
ytqjk handoff <export|apply> [options]
```

Examples:

```sh
ytqjk rag bootstrap --project-root . --vector-mode auto
ytqjk session query --project-root . --session-id "$CODEX_THREAD_ID" "installer architecture"
ytqjk dashboard start
```

The current Go retrieval implementation is lexical. `--vector-mode` is retained as a
compatible planning input, and receipts explicitly report `LEXICAL_ONLY` until a real
vector backend is configured.

## Architecture

```text
cmd/ytqjk                 unified executable
internal/cli              command boundary
internal/install          transactional installer and plugin materialization
internal/knowledge        SQLite schema v4 and governed writer service
internal/rag              project identity, indexing, querying, session anchors
internal/dashboard        loopback dashboard and workbench
internal/orchestration    signed, session-bound run/role leases
internal/handoff          hashed allowlisted Git export/apply bundles
plugins/                  Codex manifests, skills, hooks, and static assets
```

Knowledge roots resolve from `--knowledge-root`, then `YTQJK_KNOWLEDGE_ROOT`, then a
platform data directory. Windows, Linux, and WSL should use separate SQLite files; do
not open one database concurrently across host filesystems.

## Security properties

- All HTTP listeners bind to `127.0.0.1`; write requests enforce local Host/Origin
  checks and JSON-only bodies.
- Indexing rejects symbolic links, binaries, generated/dependency trees, credential
  paths, private keys, tokens, session material, and oversized input.
- New and imported knowledge remains candidate state until explicit governance.
- Git handoff bundles must live outside a repository and bind base HEAD, allowlisted
  paths, patch bytes, payload bytes, and staged output with SHA-256.
- Install replacement uses scoped snapshots and rollback; unrelated user files are
  not removed.

## Development

```sh
go test ./...
go vet ./...
go build ./cmd/ytqjk
```

CI tests Go 1.25 and 1.27 on Windows and Linux, runs the race detector, verifies
formatting, rejects remaining first-party Python sources, and cross-compiles Linux,
Windows, and macOS release binaries.

## License

MIT. See [LICENSE](LICENSE) and the plugin third-party notices.

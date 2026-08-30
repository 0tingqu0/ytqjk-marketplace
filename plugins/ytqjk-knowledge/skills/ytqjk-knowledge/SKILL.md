---
name: ytqjk-knowledge
description: Manage a local-first, versioned SQLite knowledge store through the YTQJK Go service and CLI.
---

# YTQJK Knowledge

Use the installed `ytqjk knowledge` commands as the only data boundary. Resolve the
plugin-bundled `bin/ytqjk` or the platform runtime directory if `ytqjk` is not on
`PATH`. Adapters must
not open or mutate SQLite directly. The Go service owns schema migration,
transactions, validation, durable FIFO jobs, audit records, feedback, and snapshots.

The database path is explicit caller input or resolves under the platform knowledge
root. The service does not expose a network listener or write to an unrelated drive.
The optional workbench binds only to `127.0.0.1`.

## Guarantees

- Schema v4 preserves governed documents, imports, immutable snapshots, explicit
  feedback, and project-to-global mirrors.
- Projects and documents use immutable UUIDs; original content is SHA-256 addressed.
- Imports and new documents begin as candidates and are never auto-approved.
- Candidate edits, soft deletion, state transitions, feedback, and snapshot activation
  run through transactional writer jobs.
- Reads use one active immutable snapshot generation at a time.

## Typical flow

```text
ytqjk knowledge create-project --scope project --alias my-project
ytqjk knowledge create-candidate --project-id <uuid> --title note \
  --content-file <file> --source manual
ytqjk knowledge snapshot --project-id <uuid>
ytqjk knowledge search --project-id <uuid> <query>
```

Use `ytqjk knowledge workbench --project-id <uuid>` only when an interactive local
candidate editor is useful. Promotion remains an explicit governance action.

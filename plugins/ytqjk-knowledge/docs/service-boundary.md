# Go service boundary

All adapters call the Go `knowledge.Service`; no adapter creates a SQLite connection
or queries tables directly. The service owns migrations, transactions, payload
validation, leased FIFO jobs, audit entries, feedback, and snapshot reads.

`ImportCandidates` is the bootstrap persistence boundary. It atomically ensures the
target project, deduplicated candidate documents, chunks, source provenance, and the
first-import receipt. Imported material is always candidate state. Receipt SHA-256
values are integrity checks, not authentication against a process able to rewrite the
database.

`RecordFeedback` is invocation-idempotent and commits local and mirrored effects in
one transaction. Ordinary `AppendState` remains monotonic; public edits cannot mutate
system-managed mirrors. `CreateSnapshot` captures exact document-version membership
before atomically switching the active generation.

The command line, dashboard, workbench, import scanner, and future adapters must use
these methods rather than reproducing storage rules.

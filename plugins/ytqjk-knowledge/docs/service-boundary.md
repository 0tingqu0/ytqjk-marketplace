# Service Boundary

All adapters call `KnowledgeService`. No adapter may create a SQLite connection
or query tables directly. The service owns migrations, transactions, payload
validation, leased FIFO jobs, audit entries, and snapshot reads.

`import_candidates()` is the only bootstrap persistence boundary. It atomically
ensures the target project, deduplicated candidate documents, real chunks,
source provenance, and the first-import marker. Callers provide scanned
`CandidateImport` values; the service revalidates every proof and rejects any
governance state other than `CANDIDATE`.

Imported provenance has its own fixed `CANDIDATE` governance state. A new
source that deduplicates against approved or verified content is recorded as
candidate provenance without being attached to the governed version's source
list. Re-reading an unchanged source is idempotent; changed raw source bytes
refresh its stored scan proof even when parsed content is unchanged.

Receipt SHA-256 values are integrity checksums, not authentication against a
process that can rewrite the database. Reads enforce exact fields, types,
counter ranges, and marker/project row binding before exposing prior results.

`search_capabilities()` advertises future FTS and LanceDB extension points as
`NOT_IMPLEMENTED`. This plugin does not claim a search backend it does not run.
Expired `RUNNING` job leases recover to FIFO `QUEUED` before a writer claim.
Repair evaluates all `RUNNING` leases against one UTC instant. It recovers
expired or legacy-incomplete leases, but an unparsable lease or more than one
live lease fails closed and rolls back the entire migration transaction.

`record_feedback(document_id, invocation_id, correct)` is the only feedback
counter boundary. The canonical invocation UUID makes repeated equal feedback
idempotent and rejects a contradictory result. Retrieval alone never changes a
governance score. The event count still reflects only `record_feedback`; its
score uses the current governance state as a floor so correct feedback can never
downgrade a document advanced through ordinary governance. Correct feedback
keeps the first use as `candidate`, promotes the second to `approved` with an
atomic `global` mirror, and promotes the third to `verified`. Incorrect feedback
steps back through `approved`, `candidate`, and an append-only `tombstone`;
`recycle_bin()` exposes only latest tombstones.

Feedback, source and mirror versions, the immutable sync link, and the durable
FIFO job terminal transition commit in one SQLite transaction. The v4 storage
trigger permits the feedback-only downgrade graph, while ordinary
`append_state()` continues to reject downgrades. Project source indexes and
reconstructable RAG caches are not KnowledgeService documents and are never
mirrored by this API.

A document referenced by `global_sync.global_document_id` is a system-managed
mirror. Public append, candidate edit, and candidate deletion jobs fail without
knowledge side effects; only the internal `record_feedback()` transaction may
advance or downgrade that mirror. Unlinked global documents keep their ordinary
public mutation behavior, including when the database is migrated to schema v3.

Each event binds one unique succeeded job plus its input, local result, and
optional global result version IDs. Repair replays stable event primary keys,
not timestamps, and validates every local and mirror-version suffix.

Schema v4 repair recreates only missing canonical triggers and queue guards.
Missing or altered tables, constraints, relationships, or semantically invalid
feedback rows fail closed and roll back the entire repair transaction.

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

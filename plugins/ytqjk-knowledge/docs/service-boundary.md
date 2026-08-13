# Service Boundary

All adapters call `KnowledgeService`. No adapter may create a SQLite connection
or query tables directly. The service owns migrations, transactions, payload
validation, leased FIFO jobs, audit entries, and snapshot reads.

`search_capabilities()` advertises future FTS and LanceDB extension points as
`NOT_IMPLEMENTED`. This plugin does not claim a search backend it does not run.
Expired `RUNNING` job leases recover to FIFO `QUEUED` before a writer claim.

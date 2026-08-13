---
name: ytqjk-knowledge
description: Manage a local-first, versioned SQLite knowledge store through the YTQJK KnowledgeService.
---

# YTQJK Knowledge

Use `KnowledgeService` as only data boundary. Future CLI, HTTP, MCP, and
workbench adapters call service methods; adapters never open SQLite directly.

Store location is explicit caller input. Service never starts networking,
creates a background daemon, or writes `D:\knowledge` unless a caller passes
that path explicitly. Search adapters are declared only as capabilities; FTS
and LanceDB retrieval are not implemented in this release.

## Core guarantees

- Schema v1 provides governed documents and durable writer jobs. Schema v2 adds
  immutable snapshots. Transactional downgrade keeps v1 service writes usable.
- Projects use immutable UUID, scope, and alias.
- Originals are SHA-256 content-addressed and deduplicated.
- Candidates can be edited or soft deleted. Approved, verified, and tombstone
  states append a new version only.
- Writes pass leased durable FIFO jobs with `QUEUED -> RUNNING -> SUCCEEDED|FAILED`.
- Snapshots capture immutable document-version membership then atomically become
  active. Read one snapshot generation at a time.

## Local API

```python
from pathlib import Path
from scripts.service import KnowledgeService

service = KnowledgeService(Path("local-knowledge.sqlite3"))
project_id = service.create_project("project", "my-project")
document_id = service.create_candidate(project_id, "note", "manual")
snapshot_id = service.create_snapshot(project_id)
```

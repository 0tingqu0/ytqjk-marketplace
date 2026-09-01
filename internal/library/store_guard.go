package library

import (
	"context"
	"database/sql"
	"sync"
)

// SnapshotGuard holds a canonical Library snapshot and its SQLite write lock.
type SnapshotGuard struct {
	closeMu     sync.Mutex
	transaction *sql.Tx
	snapshot    Snapshot
	closed      bool
	closeErr    error
}

// BeginSnapshotGuard freezes the persisted Library state until the guard closes.
func (s *Store) BeginSnapshotGuard(ctx context.Context) (*SnapshotGuard, error) {
	transaction, err := s.database.BeginTx(ctx, nil)
	if err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	revision, _, nodes, err := loadState(transaction.QueryRow)
	if err != nil {
		return failSnapshotGuard(transaction, err)
	}
	tree, err := NewTree(nodes, revision)
	if err != nil {
		return failSnapshotGuard(
			transaction,
			storeError("LIBRARY_STORE_CORRUPT", err),
		)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		return failSnapshotGuard(
			transaction,
			storeError("LIBRARY_STORE_CORRUPT", err),
		)
	}
	return &SnapshotGuard{transaction: transaction, snapshot: snapshot}, nil
}

// Snapshot returns a deep copy of the state frozen by this guard.
func (g *SnapshotGuard) Snapshot() Snapshot {
	result := Snapshot{
		Revision: g.snapshot.Revision,
		Nodes:    make([]Node, len(g.snapshot.Nodes)),
		Edges:    make([]Edge, len(g.snapshot.Edges)),
		Roots:    make([]string, len(g.snapshot.Roots)),
		Digest:   g.snapshot.Digest,
	}
	for index, node := range g.snapshot.Nodes {
		result.Nodes[index] = cloneNode(node)
	}
	copy(result.Edges, g.snapshot.Edges)
	copy(result.Roots, g.snapshot.Roots)
	return result
}

// Close rolls back the read transaction and releases its SQLite write lock.
func (g *SnapshotGuard) Close() error {
	g.closeMu.Lock()
	defer g.closeMu.Unlock()
	if g.closed {
		return g.closeErr
	}
	g.closed = true
	if err := g.transaction.Rollback(); err != nil {
		g.closeErr = storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	g.transaction = nil
	return g.closeErr
}

func failSnapshotGuard(transaction *sql.Tx, cause error) (*SnapshotGuard, error) {
	if err := transaction.Rollback(); err != nil {
		return nil, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return nil, cause
}

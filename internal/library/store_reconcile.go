package library

import (
	"context"
	"fmt"
	"time"
)

// ReconcileSeedNodes atomically adds missing global/project nodes as one CAS batch.
// Existing configuration is never rewritten, even when seed labels or parents differ.
func (s *Store) ReconcileSeedNodes(seed []Node) (Snapshot, error) {
	additions, err := validateSeedNodes(seed)
	if err != nil {
		return Snapshot{}, err
	}
	transaction, err := s.database.BeginTx(context.Background(), nil)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	defer transaction.Rollback()
	revision, digest, nodes, err := loadState(transaction.QueryRow)
	if err != nil {
		return Snapshot{}, err
	}
	byID := make(map[string]Node, len(nodes)+len(additions))
	for _, node := range nodes {
		byID[node.ID] = cloneNode(node)
	}
	changed := false
	for _, candidate := range additions {
		existing, found := byID[candidate.ID]
		if found {
			if existing.Type != candidate.Type {
				return Snapshot{}, storeError(
					"LIBRARY_SEED_CONFLICT",
					fmt.Errorf("node %s has type %s, seed type %s", candidate.ID, existing.Type, candidate.Type),
				)
			}
			continue
		}
		candidate.Stats = Statistics{}
		byID[candidate.ID] = cloneNode(candidate)
		changed = true
	}
	if !changed {
		tree, treeErr := NewTree(nodes, revision)
		if treeErr != nil {
			return Snapshot{}, storeError("LIBRARY_STORE_CORRUPT", treeErr)
		}
		return commitReadOnlyReconcile(transaction, tree)
	}
	if revision == MaxRevision {
		return Snapshot{}, contractError("REVISION_EXHAUSTED")
	}
	merged := make([]Node, 0, len(byID))
	for _, node := range byID {
		merged = append(merged, node)
	}
	tree, err := NewTree(merged, revision+1)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_SEED_CONFLICT", err)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_SEED_CONFLICT", err)
	}
	nodesJSON, err := configurationJSON(snapshot.Nodes)
	if err != nil {
		return Snapshot{}, err
	}
	result, err := transaction.Exec(`
		UPDATE library_state
		SET revision = ?, digest = ?, nodes_json = ?, updated_at = ?
		WHERE singleton = 1 AND revision = ? AND digest = ?`,
		snapshot.Revision, snapshot.Digest, nodesJSON,
		s.now().UTC().Format(time.RFC3339Nano), revision, digest,
	)
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	if updated != 1 {
		return Snapshot{}, contractError("REVISION_CONFLICT")
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return snapshot, nil
}

func validateSeedNodes(seed []Node) ([]Node, error) {
	result := make([]Node, 0, len(seed))
	seen := make(map[string]Type, len(seed))
	for _, node := range seed {
		if node.Type != TypeGlobal && node.Type != TypeProject {
			return nil, storeError("LIBRARY_SEED_CONFLICT", fmt.Errorf("seed type %s is forbidden", node.Type))
		}
		node.Stats = Statistics{}
		if err := node.Validate(); err != nil {
			return nil, storeError("LIBRARY_SEED_CONFLICT", err)
		}
		if existingType, duplicate := seen[node.ID]; duplicate {
			return nil, storeError(
				"LIBRARY_SEED_CONFLICT",
				fmt.Errorf("duplicate seed %s (%s, %s)", node.ID, existingType, node.Type),
			)
		}
		seen[node.ID] = node.Type
		result = append(result, cloneNode(node))
	}
	return result, nil
}

func commitReadOnlyReconcile(transaction interface {
	Commit() error
}, tree *Tree) (Snapshot, error) {
	snapshot, err := tree.Snapshot()
	if err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_CORRUPT", err)
	}
	if err := transaction.Commit(); err != nil {
		return Snapshot{}, storeError("LIBRARY_STORE_UNAVAILABLE", err)
	}
	return snapshot, nil
}

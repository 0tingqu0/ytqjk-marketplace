package tree

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
)

func (t *Tree) planAttach(nodeID, parentID string) (Preview, error) {
	actual, _, err := t.attachPlan(nodeID, parentID)
	return actual, err
}

func (t *Tree) attachPlan(nodeID, parentID string) (Preview, map[string]string, error) {
	if err := t.requireNodes(nodeID, parentID); err != nil {
		return Preview{}, nil, err
	}
	if nodeID == parentID {
		return Preview{}, nil, errors.New("SELF_PARENT")
	}
	if current, exists := t.parents[nodeID]; exists {
		if current == parentID {
			return Preview{}, nil, errors.New("DUPLICATE_EDGE")
		}
		return Preview{}, nil, errors.New("MULTIPLE_PARENTS")
	}
	parents := cloneParents(t.parents)
	parents[nodeID] = parentID
	preview, err := t.plan("attach", nodeID, parentID, parents)
	return preview, parents, err
}

func (t *Tree) detachPlan(nodeID string) (Preview, map[string]string, error) {
	if err := t.requireNodes(nodeID); err != nil {
		return Preview{}, nil, err
	}
	oldParent, exists := t.parents[nodeID]
	if !exists {
		return Preview{}, nil, errors.New("NODE_ALREADY_ROOT")
	}
	parents := cloneParents(t.parents)
	delete(parents, nodeID)
	preview, err := t.plan("detach", nodeID, oldParent, parents)
	return preview, parents, err
}

func (t *Tree) movePlan(nodeID, parentID string) (Preview, map[string]string, error) {
	if err := t.requireNodes(nodeID, parentID); err != nil {
		return Preview{}, nil, err
	}
	if nodeID == parentID {
		return Preview{}, nil, errors.New("SELF_PARENT")
	}
	current, exists := t.parents[nodeID]
	if !exists {
		return Preview{}, nil, errors.New("NODE_IS_ROOT")
	}
	if current == parentID {
		return Preview{}, nil, errors.New("DUPLICATE_EDGE")
	}
	parents := cloneParents(t.parents)
	parents[nodeID] = parentID
	preview, err := t.plan("move", nodeID, parentID, parents)
	return preview, parents, err
}

func (t *Tree) insertPlan(parentID, nodeID, middleID string) (Preview, map[string]string, error) {
	if err := t.requireNodes(parentID, nodeID, middleID); err != nil {
		return Preview{}, nil, err
	}
	if parentID == nodeID || nodeID == middleID || parentID == middleID {
		return Preview{}, nil, errors.New("SELF_PARENT")
	}
	if t.parents[nodeID] != parentID {
		return Preview{}, nil, errors.New("EDGE_NOT_FOUND")
	}
	if _, exists := t.parents[middleID]; exists {
		return Preview{}, nil, errors.New("MULTIPLE_PARENTS")
	}
	parents := cloneParents(t.parents)
	parents[middleID] = parentID
	parents[nodeID] = middleID
	preview, err := t.plan("insert_between", nodeID, middleID, parents)
	return preview, parents, err
}

func (t *Tree) plan(operation, nodeID, relatedID string, parents map[string]string) (Preview, error) {
	if err := t.validateAcyclic(parents); err != nil {
		return Preview{}, err
	}
	preview := Preview{
		Operation: operation, NodeID: nodeID, RelatedID: relatedID,
		OldParentID: t.parents[nodeID], NewParentID: parents[nodeID],
		OldChain: chain(nodeID, t.parents), NewChain: chain(nodeID, parents),
		SubtreeSize: len(t.subtreeMembers(nodeID)), AnchorImpact: AnchorImpactPending,
		BaseRevision: t.revision,
	}
	binding := struct {
		Preview Preview  `json:"preview"`
		Members []string `json:"members"`
		Nodes   []Node   `json:"nodes"`
		Edges   []Edge   `json:"edges"`
	}{Preview: preview, Members: t.subtreeMembers(nodeID), Nodes: t.Nodes(), Edges: t.Edges()}
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	preview.PreviewDigest = hex.EncodeToString(digest[:])
	return preview, nil
}

func (t *Tree) commit(actual Preview, parents map[string]string, supplied Preview, expectedRevision int64, planError error) error {
	if expectedRevision != t.revision {
		return ErrRevisionConflict
	}
	if t.revision == MaxRevision {
		return errors.New("REVISION_EXHAUSTED")
	}
	if planError != nil {
		return planError
	}
	actualJSON, _ := json.Marshal(actual)
	suppliedJSON, _ := json.Marshal(supplied)
	if !strings.EqualFold(actual.PreviewDigest, supplied.PreviewDigest) || string(actualJSON) != string(suppliedJSON) {
		return ErrPreviewMismatch
	}
	t.parents = parents
	t.revision++
	return nil
}

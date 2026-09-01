package library

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"sort"
)

const anchorImpactPending = "NOT_EVALUATED"

// Preview binds one proposed mutation to both its base and target topology.
type Preview struct {
	Operation      string   `json:"operation"`
	NodeID         string   `json:"node_id"`
	RelatedID      *string  `json:"related_id"`
	OldParentID    *string  `json:"old_parent_id"`
	NewParentID    *string  `json:"new_parent_id"`
	OldChain       []string `json:"old_chain"`
	NewChain       []string `json:"new_chain"`
	SubtreeSize    int      `json:"subtree_size"`
	AnchorImpact   string   `json:"anchor_impact"`
	BaseRevision   int64    `json:"base_revision"`
	PreviewDigest  string   `json:"preview_digest"`
	subtreeMembers []string
	baseDigest     string
	targetDigest   string
}

func (t *Tree) previewCreate(request CreateRequest) (Preview, error) {
	node, err := NewNode(request)
	if err != nil {
		return Preview{}, err
	}
	if _, exists := t.nodes[node.ID]; exists {
		return Preview{}, contractError("DUPLICATE_NODE")
	}
	targetNodes := cloneNodeMap(t.nodes)
	targetParents := cloneParents(t.parents)
	targetNodes[node.ID] = node
	if node.ParentID != nil {
		if _, exists := t.nodes[*node.ParentID]; !exists {
			return Preview{}, contractError("UNKNOWN_NODE")
		}
		targetParents[node.ID] = *node.ParentID
	}
	return t.plan("create", node.ID, node.ParentID, targetNodes, targetParents)
}

// Create commits one previewed Library creation.
func (t *Tree) Create(
	request CreateRequest,
	preview Preview,
	expectedRevision int64,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commit(expectedRevision, preview, func() (Preview, map[string]Node, map[string]string, error) {
		actual, err := t.previewCreate(request)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		node, err := NewNode(request)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		nodes := cloneNodeMap(t.nodes)
		parents := cloneParents(t.parents)
		nodes[node.ID] = node
		if node.ParentID != nil {
			parents[node.ID] = *node.ParentID
		}
		return actual, nodes, parents, nil
	})
}

func (t *Tree) previewAttach(nodeID, parentID string) (Preview, error) {
	if err := t.requireNodes(nodeID, parentID); err != nil {
		return Preview{}, err
	}
	if nodeID == parentID {
		return Preview{}, contractError("SELF_PARENT")
	}
	if current, exists := t.parents[nodeID]; exists {
		if current == parentID {
			return Preview{}, contractError("DUPLICATE_EDGE")
		}
		return Preview{}, contractError("MULTIPLE_PARENTS")
	}
	nodes, parents := t.targetWithParent(nodeID, &parentID)
	return t.plan("attach", nodeID, &parentID, nodes, parents)
}

// Attach commits one previewed attach operation.
func (t *Tree) Attach(nodeID, parentID string, preview Preview, expectedRevision int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commit(expectedRevision, preview, func() (Preview, map[string]Node, map[string]string, error) {
		actual, err := t.previewAttach(nodeID, parentID)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		nodes, parents := t.targetWithParent(nodeID, &parentID)
		return actual, nodes, parents, nil
	})
}

func (t *Tree) previewDetach(nodeID string) (Preview, error) {
	if err := t.requireNodes(nodeID); err != nil {
		return Preview{}, err
	}
	parent, exists := t.parents[nodeID]
	if !exists {
		return Preview{}, contractError("NODE_ALREADY_ROOT")
	}
	nodes, parents := t.targetWithParent(nodeID, nil)
	return t.plan("detach", nodeID, &parent, nodes, parents)
}

// Detach commits one previewed detach operation.
func (t *Tree) Detach(nodeID string, preview Preview, expectedRevision int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commit(expectedRevision, preview, func() (Preview, map[string]Node, map[string]string, error) {
		actual, err := t.previewDetach(nodeID)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		nodes, parents := t.targetWithParent(nodeID, nil)
		return actual, nodes, parents, nil
	})
}

func (t *Tree) previewMove(nodeID, parentID string) (Preview, error) {
	if err := t.requireNodes(nodeID, parentID); err != nil {
		return Preview{}, err
	}
	if nodeID == parentID {
		return Preview{}, contractError("SELF_PARENT")
	}
	current, exists := t.parents[nodeID]
	if !exists {
		return Preview{}, contractError("NODE_IS_ROOT")
	}
	if current == parentID {
		return Preview{}, contractError("DUPLICATE_EDGE")
	}
	nodes, parents := t.targetWithParent(nodeID, &parentID)
	return t.plan("move", nodeID, &parentID, nodes, parents)
}

// Move commits one previewed parent replacement.
func (t *Tree) Move(nodeID, parentID string, preview Preview, expectedRevision int64) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commit(expectedRevision, preview, func() (Preview, map[string]Node, map[string]string, error) {
		actual, err := t.previewMove(nodeID, parentID)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		nodes, parents := t.targetWithParent(nodeID, &parentID)
		return actual, nodes, parents, nil
	})
}

func (t *Tree) previewInsertBetween(parentID, nodeID, middleID string) (Preview, error) {
	if err := t.requireNodes(parentID, nodeID, middleID); err != nil {
		return Preview{}, err
	}
	if parentID == nodeID || parentID == middleID || nodeID == middleID {
		return Preview{}, contractError("SELF_PARENT")
	}
	if t.parents[nodeID] != parentID {
		return Preview{}, contractError("EDGE_NOT_FOUND")
	}
	if _, exists := t.parents[middleID]; exists {
		return Preview{}, contractError("MULTIPLE_PARENTS")
	}
	nodes := cloneNodeMap(t.nodes)
	parents := cloneParents(t.parents)
	setParent(nodes, parents, middleID, &parentID)
	setParent(nodes, parents, nodeID, &middleID)
	return t.plan("insert_between", nodeID, &middleID, nodes, parents)
}

// InsertBetween commits one previewed edge insertion.
func (t *Tree) InsertBetween(
	parentID, nodeID, middleID string,
	preview Preview,
	expectedRevision int64,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.commit(expectedRevision, preview, func() (Preview, map[string]Node, map[string]string, error) {
		actual, err := t.previewInsertBetween(parentID, nodeID, middleID)
		if err != nil {
			return Preview{}, nil, nil, err
		}
		nodes := cloneNodeMap(t.nodes)
		parents := cloneParents(t.parents)
		setParent(nodes, parents, middleID, &parentID)
		setParent(nodes, parents, nodeID, &middleID)
		return actual, nodes, parents, nil
	})
}

type mutationPlan func() (Preview, map[string]Node, map[string]string, error)

func (t *Tree) commit(expectedRevision int64, preview Preview, planner mutationPlan) error {
	if expectedRevision != t.revision {
		return contractError("REVISION_CONFLICT")
	}
	if t.revision == MaxRevision {
		return contractError("REVISION_EXHAUSTED")
	}
	actual, nodes, parents, err := planner()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(preview, actual) {
		return contractError("PREVIEW_MISMATCH")
	}
	t.nodes = nodes
	t.parents = parents
	t.revision++
	return nil
}

func (t *Tree) plan(
	operation string,
	nodeID string,
	relatedID *string,
	targetNodes map[string]Node,
	targetParents map[string]string,
) (Preview, error) {
	if err := validateTopology(targetNodes, targetParents); err != nil {
		return Preview{}, err
	}
	base, err := buildSnapshot(t.revision, t.nodes, t.parents)
	if err != nil {
		return Preview{}, err
	}
	target, err := buildSnapshot(t.revision, targetNodes, targetParents)
	if err != nil {
		return Preview{}, err
	}
	oldChain := []string{}
	members := []string{nodeID}
	if _, exists := t.nodes[nodeID]; exists {
		oldChain = chain(nodeID, t.parents)
		members = t.subtreeMembers(nodeID)
	}
	preview := Preview{
		Operation: operation, NodeID: nodeID,
		RelatedID:   cloneString(relatedID),
		OldParentID: parentPointer(t.parents, nodeID),
		NewParentID: parentPointer(targetParents, nodeID),
		OldChain:    oldChain, NewChain: chain(nodeID, targetParents),
		SubtreeSize: len(members), AnchorImpact: anchorImpactPending,
		BaseRevision: t.revision, subtreeMembers: members,
		baseDigest: base.Digest, targetDigest: target.Digest,
	}
	digest, err := preview.digest()
	if err != nil {
		return Preview{}, err
	}
	preview.PreviewDigest = digest
	return preview, nil
}

func (p Preview) digest() (string, error) {
	binding := struct {
		AnchorImpact   string   `json:"anchor_impact"`
		BaseDigest     string   `json:"base_digest"`
		BaseRevision   int64    `json:"base_revision"`
		NewChain       []string `json:"new_chain"`
		NewParentID    *string  `json:"new_parent_id"`
		NodeID         string   `json:"node_id"`
		OldChain       []string `json:"old_chain"`
		OldParentID    *string  `json:"old_parent_id"`
		Operation      string   `json:"operation"`
		RelatedID      *string  `json:"related_id"`
		SubtreeMembers []string `json:"subtree_members"`
		TargetDigest   string   `json:"target_digest"`
	}{
		p.AnchorImpact, p.baseDigest, p.BaseRevision,
		p.NewChain, p.NewParentID, p.NodeID, p.OldChain,
		p.OldParentID, p.Operation, p.RelatedID,
		p.subtreeMembers, p.targetDigest,
	}
	content, err := json.Marshal(binding)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

func (t *Tree) targetWithParent(nodeID string, parentID *string) (map[string]Node, map[string]string) {
	nodes := cloneNodeMap(t.nodes)
	parents := cloneParents(t.parents)
	setParent(nodes, parents, nodeID, parentID)
	return nodes, parents
}

func setParent(nodes map[string]Node, parents map[string]string, nodeID string, parentID *string) {
	node := cloneNode(nodes[nodeID])
	node.ParentID = cloneString(parentID)
	nodes[nodeID] = node
	if parentID == nil {
		delete(parents, nodeID)
		return
	}
	parents[nodeID] = *parentID
}

func (t *Tree) requireNodes(nodeIDs ...string) error {
	for _, nodeID := range nodeIDs {
		if _, exists := t.nodes[nodeID]; !exists {
			return contractError("UNKNOWN_NODE")
		}
	}
	return nil
}

func (t *Tree) subtreeMembers(nodeID string) []string {
	pending := []string{nodeID}
	found := make(map[string]struct{})
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if _, exists := found[current]; exists {
			continue
		}
		found[current] = struct{}{}
		for child, parent := range t.parents {
			if parent == current {
				pending = append(pending, child)
			}
		}
	}
	result := make([]string, 0, len(found))
	for nodeID := range found {
		result = append(result, nodeID)
	}
	sort.Strings(result)
	return result
}

func cloneParents(parents map[string]string) map[string]string {
	result := make(map[string]string, len(parents))
	for child, parent := range parents {
		result[child] = parent
	}
	return result
}

func parentPointer(parents map[string]string, nodeID string) *string {
	parent, exists := parents[nodeID]
	if !exists {
		return nil
	}
	return &parent
}

package tree

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

const (
	AnchorImpactPending = "NOT_EVALUATED"
	MaxRevision         = int64(^uint64(0) >> 1)
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var (
	ErrRevisionConflict = errors.New("knowledge tree revision conflict")
	ErrPreviewMismatch  = errors.New("knowledge tree preview mismatch")
)

type Node struct {
	NodeID     string `json:"node_id"`
	Title      string `json:"title"`
	Kind       string `json:"kind"`
	MountID    string `json:"mount_id,omitempty"`
	Capability string `json:"capability,omitempty"`
}

type Edge struct {
	Parent string `json:"parent"`
	Child  string `json:"child"`
}

type Preview struct {
	Operation     string   `json:"operation"`
	NodeID        string   `json:"node_id"`
	RelatedID     string   `json:"related_id,omitempty"`
	OldParentID   string   `json:"old_parent_id,omitempty"`
	NewParentID   string   `json:"new_parent_id,omitempty"`
	OldChain      []string `json:"old_chain"`
	NewChain      []string `json:"new_chain"`
	SubtreeSize   int      `json:"subtree_size"`
	AnchorImpact  string   `json:"anchor_impact"`
	BaseRevision  int64    `json:"base_revision"`
	PreviewDigest string   `json:"preview_digest"`
}

type Snapshot struct {
	SchemaVersion int64  `json:"schema_version"`
	Revision      int64  `json:"revision"`
	Nodes         []Node `json:"nodes"`
	Edges         []Edge `json:"edges"`
}

type Tree struct {
	revision int64
	nodes    map[string]Node
	parents  map[string]string
}

func New(nodes []Node, edges []Edge, revision int64) (*Tree, error) {
	if revision < 0 || revision > MaxRevision {
		return nil, errors.New("INVALID_REVISION")
	}
	result := &Tree{revision: revision, nodes: map[string]Node{}, parents: map[string]string{}}
	for _, node := range nodes {
		if err := validateNode(node); err != nil {
			return nil, err
		}
		if _, duplicate := result.nodes[node.NodeID]; duplicate {
			return nil, errors.New("DUPLICATE_NODE")
		}
		result.nodes[node.NodeID] = node
	}
	seen := map[Edge]bool{}
	for _, edge := range edges {
		if _, ok := result.nodes[edge.Parent]; !ok {
			return nil, errors.New("UNKNOWN_NODE")
		}
		if _, ok := result.nodes[edge.Child]; !ok {
			return nil, errors.New("UNKNOWN_NODE")
		}
		if edge.Parent == edge.Child {
			return nil, errors.New("SELF_PARENT")
		}
		if seen[edge] {
			return nil, errors.New("DUPLICATE_EDGE")
		}
		if _, exists := result.parents[edge.Child]; exists {
			return nil, errors.New("MULTIPLE_PARENTS")
		}
		seen[edge] = true
		result.parents[edge.Child] = edge.Parent
	}
	if err := result.validateAcyclic(result.parents); err != nil {
		return nil, err
	}
	return result, nil
}

func Default() *Tree {
	result, _ := New([]Node{{NodeID: "global", Title: "Personal knowledge", Kind: "global"}}, nil, 0)
	return result
}

func FromSnapshot(snapshot Snapshot) (*Tree, error) {
	if snapshot.SchemaVersion != 1 {
		return nil, errors.New("UNSUPPORTED_TREE_SCHEMA")
	}
	return New(snapshot.Nodes, snapshot.Edges, snapshot.Revision)
}

func (t *Tree) Snapshot() Snapshot {
	return Snapshot{SchemaVersion: 1, Revision: t.revision, Nodes: t.Nodes(), Edges: t.Edges()}
}

func (t *Tree) Revision() int64 { return t.revision }

func (t *Tree) Node(nodeID string) (Node, bool) {
	node, ok := t.nodes[nodeID]
	return node, ok
}

func (t *Tree) Parent(nodeID string) (string, bool) {
	parent, ok := t.parents[nodeID]
	return parent, ok
}

func (t *Tree) Subtree(nodeID string) ([]Node, error) {
	if _, ok := t.nodes[nodeID]; !ok {
		return nil, errors.New("UNKNOWN_NODE")
	}
	members := t.subtreeMembers(nodeID)
	result := make([]Node, 0, len(members))
	for _, member := range members {
		result = append(result, t.nodes[member])
	}
	return result, nil
}

func (t *Tree) Contains(rootID, nodeID string) (bool, error) {
	if err := t.requireNodes(rootID, nodeID); err != nil {
		return false, err
	}
	current := nodeID
	for {
		if current == rootID {
			return true, nil
		}
		parent, ok := t.parents[current]
		if !ok {
			return false, nil
		}
		current = parent
	}
}

func (t *Tree) Nodes() []Node {
	result := make([]Node, 0, len(t.nodes))
	for _, node := range t.nodes {
		result = append(result, node)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result
}

func (t *Tree) Edges() []Edge {
	result := make([]Edge, 0, len(t.parents))
	for child, parent := range t.parents {
		result = append(result, Edge{Parent: parent, Child: child})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Parent == result[j].Parent {
			return result[i].Child < result[j].Child
		}
		return result[i].Parent < result[j].Parent
	})
	return result
}

func (t *Tree) Ancestors(nodeID string) ([]string, error) {
	if _, ok := t.nodes[nodeID]; !ok {
		return nil, errors.New("UNKNOWN_NODE")
	}
	return chain(nodeID, t.parents), nil
}

func (t *Tree) PreviewAttach(nodeID, parentID string) (Preview, error) {
	return t.planAttach(nodeID, parentID)
}

func (t *Tree) Attach(nodeID, parentID string, preview Preview, expectedRevision int64) error {
	actual, parents, err := t.attachPlan(nodeID, parentID)
	return t.commit(actual, parents, preview, expectedRevision, err)
}

func (t *Tree) PreviewDetach(nodeID string) (Preview, error) {
	actual, _, err := t.detachPlan(nodeID)
	return actual, err
}

func (t *Tree) Detach(nodeID string, preview Preview, expectedRevision int64) error {
	actual, parents, err := t.detachPlan(nodeID)
	return t.commit(actual, parents, preview, expectedRevision, err)
}

func (t *Tree) PreviewMove(nodeID, parentID string) (Preview, error) {
	actual, _, err := t.movePlan(nodeID, parentID)
	return actual, err
}

func (t *Tree) Move(nodeID, parentID string, preview Preview, expectedRevision int64) error {
	actual, parents, err := t.movePlan(nodeID, parentID)
	return t.commit(actual, parents, preview, expectedRevision, err)
}

func (t *Tree) PreviewInsertBetween(parentID, nodeID, middleID string) (Preview, error) {
	actual, _, err := t.insertPlan(parentID, nodeID, middleID)
	return actual, err
}

func (t *Tree) InsertBetween(parentID, nodeID, middleID string, preview Preview, expectedRevision int64) error {
	actual, parents, err := t.insertPlan(parentID, nodeID, middleID)
	return t.commit(actual, parents, preview, expectedRevision, err)
}

func (t *Tree) AddNode(node Node, parentID string) error {
	if err := validateNode(node); err != nil {
		return err
	}
	if _, exists := t.nodes[node.NodeID]; exists {
		return errors.New("DUPLICATE_NODE")
	}
	if parentID != "" {
		if _, exists := t.nodes[parentID]; !exists {
			return errors.New("UNKNOWN_NODE")
		}
	}
	t.nodes[node.NodeID] = node
	if parentID != "" {
		t.parents[node.NodeID] = parentID
	}
	return t.validateAcyclic(t.parents)
}

func (t *Tree) IncrementRevision(expected int64) error {
	if expected != t.revision {
		return ErrRevisionConflict
	}
	if t.revision == MaxRevision {
		return errors.New("REVISION_EXHAUSTED")
	}
	t.revision++
	return nil
}

func (t *Tree) requireNodes(values ...string) error {
	for _, value := range values {
		if _, ok := t.nodes[value]; !ok {
			return errors.New("UNKNOWN_NODE")
		}
	}
	return nil
}

func (t *Tree) subtreeMembers(nodeID string) []string {
	pending := []string{nodeID}
	found := map[string]bool{}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if found[current] {
			continue
		}
		found[current] = true
		for child, parent := range t.parents {
			if parent == current {
				pending = append(pending, child)
			}
		}
	}
	result := make([]string, 0, len(found))
	for value := range found {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (t *Tree) validateAcyclic(parents map[string]string) error {
	for nodeID := range t.nodes {
		seen := map[string]bool{}
		current := nodeID
		for {
			parent, exists := parents[current]
			if !exists {
				break
			}
			if seen[current] {
				return errors.New("CYCLE_DETECTED")
			}
			seen[current] = true
			current = parent
		}
	}
	return nil
}

func validateNode(node Node) error {
	if !identifierPattern.MatchString(node.NodeID) || strings.TrimSpace(node.Title) != node.Title || node.Title == "" || len(node.Title) > 200 {
		return errors.New("INVALID_NODE")
	}
	for _, character := range node.Title {
		if unicode.IsControl(character) {
			return errors.New("INVALID_TITLE")
		}
	}
	switch node.Kind {
	case "global", "group", "project":
		if node.MountID != "" || node.Capability != "" {
			return errors.New("MOUNT_METADATA_FORBIDDEN")
		}
	case "mounted":
		if !identifierPattern.MatchString(node.MountID) || !identifierPattern.MatchString(node.Capability) {
			return errors.New("INVALID_MOUNT_METADATA")
		}
		for _, value := range []string{node.Title, node.MountID, node.Capability} {
			lower := strings.ToLower(value)
			if strings.Contains(lower, "://") || strings.Contains(lower, "\\") || strings.Contains(lower, "../") || strings.Contains(lower, "..\\") || strings.Contains(lower, "password") || strings.Contains(lower, "credential") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
				return errors.New("UNSAFE_MOUNT_METADATA")
			}
		}
	default:
		return fmt.Errorf("INVALID_NODE_KIND: %s", node.Kind)
	}
	return nil
}

func chain(nodeID string, parents map[string]string) []string {
	result := []string{nodeID}
	for {
		parent, exists := parents[result[len(result)-1]]
		if !exists {
			return result
		}
		result = append(result, parent)
	}
}

func cloneParents(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for child, parent := range source {
		result[child] = parent
	}
	return result
}

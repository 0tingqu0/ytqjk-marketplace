package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"sync"
)

// MaxRevision is the highest revision accepted by the topology CAS.
const MaxRevision int64 = 1<<63 - 1

// Edge is a derived parent-child relationship in a Library snapshot.
type Edge struct {
	ChildID  string `json:"child_id"`
	ParentID string `json:"parent_id"`
}

// Snapshot is a detached, deterministic HTTP representation of a Library tree.
type Snapshot struct {
	Revision int64    `json:"revision"`
	Nodes    []Node   `json:"nodes"`
	Edges    []Edge   `json:"edges"`
	Roots    []string `json:"roots"`
	Digest   string   `json:"digest"`
}

// Tree owns one validated, single-parent Library topology.
type Tree struct {
	mu       sync.RWMutex
	revision int64
	nodes    map[string]Node
	parents  map[string]string
}

// NewTree revalidates and copies every node before exposing the topology.
func NewTree(nodes []Node, revision int64) (*Tree, error) {
	if revision < 0 || revision > MaxRevision {
		return nil, contractError("INVALID_REVISION")
	}
	byID := make(map[string]Node, len(nodes))
	parents := make(map[string]string)
	for _, node := range nodes {
		if err := node.Validate(); err != nil {
			return nil, err
		}
		if _, exists := byID[node.ID]; exists {
			return nil, contractError("DUPLICATE_NODE")
		}
		copied := cloneNode(node)
		byID[copied.ID] = copied
		if copied.ParentID != nil {
			parents[copied.ID] = *copied.ParentID
		}
	}
	if err := validateTopology(byID, parents); err != nil {
		return nil, err
	}
	return &Tree{revision: revision, nodes: byID, parents: parents}, nil
}

// Validate verifies one public Node without trusting its construction path.
func (n Node) Validate() error {
	if !identifierPattern.MatchString(n.ID) {
		return contractError("INVALID_NODE_ID")
	}
	if err := validateText(n.Title, 200); err != nil {
		return contractError("INVALID_TITLE")
	}
	if n.ParentID != nil && !identifierPattern.MatchString(*n.ParentID) {
		return contractError("INVALID_PARENT_ID")
	}
	if n.CapacityBytes < MinCapacityBytes || n.CapacityBytes > MaxCapacityBytes {
		return contractError("INVALID_CAPACITY_BYTES")
	}
	if n.Metadata == nil {
		return contractError("INVALID_NODE_METADATA")
	}
	switch n.Type {
	case TypeMounted:
		request := CreateRequest{
			NodeID: n.ID, Title: n.Title, Type: n.Type,
			ParentID: n.ParentID, CapacityBytes: n.CapacityBytes,
			Metadata: n.Metadata,
		}
		if err := validateMountMetadata(request); err != nil {
			return err
		}
	case TypeGlobal, TypeGroup, TypeProject:
		if len(n.Metadata) != 0 {
			return contractError("MOUNT_METADATA_FORBIDDEN")
		}
	default:
		return contractError("INVALID_NODE_KIND")
	}
	if err := n.Stats.validate(); err != nil {
		return err
	}
	return nil
}

func (s Statistics) validate() error {
	values := [...]int64{
		s.UsedBytes, s.IndexedDocuments, s.TotalDocuments,
		s.IndexedChunks, s.TotalChunks,
	}
	for _, value := range values {
		if value < 0 {
			return contractError("INVALID_LIBRARY_STATS")
		}
	}
	if s.IndexedDocuments > s.TotalDocuments || s.IndexedChunks > s.TotalChunks {
		return contractError("INVALID_LIBRARY_STATS")
	}
	return nil
}

// Revision returns the current topology revision.
func (t *Tree) Revision() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.revision
}

// Nodes returns sorted, detached copies of the current nodes.
func (t *Tree) Nodes() []Node {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return sortedNodes(t.nodes)
}

// Edges returns the current relationships in deterministic order.
func (t *Tree) Edges() []Edge {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return sortedEdges(t.parents)
}

// Ancestors returns the selected node followed by its ancestors to the root.
func (t *Tree) Ancestors(nodeID string) ([]string, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if _, exists := t.nodes[nodeID]; !exists {
		return nil, contractError("UNKNOWN_NODE")
	}
	return chain(nodeID, t.parents), nil
}

// Snapshot creates an isolated canonical response and configuration digest.
func (t *Tree) Snapshot() (Snapshot, error) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return buildSnapshot(t.revision, t.nodes, t.parents)
}

func buildSnapshot(
	revision int64,
	nodes map[string]Node,
	parents map[string]string,
) (Snapshot, error) {
	orderedNodes := sortedNodes(nodes)
	edges := sortedEdges(parents)
	roots := make([]string, 0, len(nodes))
	for _, node := range orderedNodes {
		if _, hasParent := parents[node.ID]; !hasParent {
			roots = append(roots, node.ID)
		}
	}
	body := digestBody{
		Edges: edges, Nodes: digestNodes(orderedNodes),
		Revision: revision, Roots: roots,
	}
	content, err := marshalCanonicalJSON(body)
	if err != nil {
		return Snapshot{}, err
	}
	digest := sha256.Sum256(content)
	return Snapshot{
		Revision: revision, Nodes: orderedNodes, Edges: edges,
		Roots: roots, Digest: hex.EncodeToString(digest[:]),
	}, nil
}

func marshalCanonicalJSON(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	return restoreJSONLineSeparators(encoded), nil
}

func restoreJSONLineSeparators(encoded []byte) []byte {
	result := make([]byte, 0, len(encoded))
	inString := false
	for index := 0; index < len(encoded); {
		character := encoded[index]
		if character == '"' {
			inString = !inString
			result = append(result, character)
			index++
			continue
		}
		if !inString || character != '\\' || index+1 >= len(encoded) {
			result = append(result, character)
			index++
			continue
		}
		if encoded[index+1] != 'u' || index+6 > len(encoded) {
			result = append(result, encoded[index:index+2]...)
			index += 2
			continue
		}
		escape := string(encoded[index+2 : index+6])
		switch escape {
		case "2028":
			result = append(result, "\u2028"...)
		case "2029":
			result = append(result, "\u2029"...)
		default:
			result = append(result, encoded[index:index+6]...)
		}
		index += 6
	}
	return result
}

type digestBody struct {
	Edges    []Edge       `json:"edges"`
	Nodes    []digestNode `json:"nodes"`
	Revision int64        `json:"revision"`
	Roots    []string     `json:"roots"`
}

type digestNode struct {
	CapacityBytes int64             `json:"capacity_bytes"`
	ID            string            `json:"id"`
	Metadata      map[string]string `json:"metadata"`
	ParentID      *string           `json:"parent_id"`
	Title         string            `json:"title"`
	Type          Type              `json:"type"`
}

func digestNodes(nodes []Node) []digestNode {
	result := make([]digestNode, 0, len(nodes))
	for _, node := range nodes {
		result = append(result, digestNode{
			CapacityBytes: node.CapacityBytes,
			ID:            node.ID, Metadata: cloneMetadata(node.Metadata),
			ParentID: cloneString(node.ParentID), Title: node.Title,
			Type: node.Type,
		})
	}
	return result
}

func sortedNodes(nodes map[string]Node) []Node {
	ids := make([]string, 0, len(nodes))
	for id := range nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]Node, 0, len(ids))
	for _, id := range ids {
		result = append(result, cloneNode(nodes[id]))
	}
	return result
}

func sortedEdges(parents map[string]string) []Edge {
	result := make([]Edge, 0, len(parents))
	for child, parent := range parents {
		result = append(result, Edge{ParentID: parent, ChildID: child})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ParentID == result[j].ParentID {
			return result[i].ChildID < result[j].ChildID
		}
		return result[i].ParentID < result[j].ParentID
	})
	return result
}

func cloneNode(node Node) Node {
	node.ParentID = cloneString(node.ParentID)
	node.Metadata = cloneMetadata(node.Metadata)
	return node
}

func cloneNodeMap(nodes map[string]Node) map[string]Node {
	result := make(map[string]Node, len(nodes))
	for id, node := range nodes {
		result[id] = cloneNode(node)
	}
	return result
}

func cloneMetadata(metadata map[string]string) map[string]string {
	result := make(map[string]string, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}
	return result
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	result := *value
	return &result
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

func validateTopology(nodes map[string]Node, parents map[string]string) error {
	for child, parent := range parents {
		if _, exists := nodes[child]; !exists {
			return contractError("UNKNOWN_NODE")
		}
		if _, exists := nodes[parent]; !exists {
			return contractError("UNKNOWN_NODE")
		}
		if child == parent {
			return contractError("SELF_PARENT")
		}
	}
	for nodeID := range nodes {
		seen := make(map[string]struct{})
		current := nodeID
		for {
			parent, exists := parents[current]
			if !exists {
				break
			}
			if _, duplicate := seen[current]; duplicate {
				return contractError("CYCLE_DETECTED")
			}
			seen[current] = struct{}{}
			current = parent
		}
	}
	return nil
}

package dashboard

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

const maxIssuedTreePreviews = 256

type treeActionArguments struct {
	NodeID     string
	Title      string
	Kind       string
	ParentID   string
	ParentSet  bool
	MiddleID   string
	MountID    string
	Capability string
}

type issuedTreePreview struct {
	Action           string
	ExpectedRevision int64
	BaseDigest       string
	Arguments        treeActionArguments
	Core             tree.Preview
	CreatedAt        time.Time
}

func (s *Server) treeSnapshot(writer http.ResponseWriter) int {
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	value, err := s.treeStore.Load(context.Background())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "tree": s.treePayload(value)})
	return http.StatusOK
}

func (s *Server) treeActionPreview(writer http.ResponseWriter, request *http.Request) int {
	var envelope struct {
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := readJSON(request, &envelope); err != nil || !validTreeAction(envelope.Action) {
		writeError(writer, http.StatusBadRequest, "INVALID_TREE_ACTION", "INVALID_TREE_ACTION")
		return http.StatusBadRequest
	}
	arguments, err := decodeTreeArguments(envelope.Action, envelope.Payload)
	if err != nil {
		return writeTreeOperationError(writer, err)
	}
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	value, err := s.treeStore.Load(request.Context())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	core, summary, affected, err := planTreeAction(value, envelope.Action, arguments)
	if err != nil {
		return writeTreeOperationError(writer, err)
	}
	token, err := safeio.RandomHex(32)
	if err != nil {
		writeError(writer, http.StatusInternalServerError, "PREVIEW_CREATE_FAILED", "PREVIEW_CREATE_FAILED")
		return http.StatusInternalServerError
	}
	payload := s.treePayload(value)
	baseDigest, _ := payload["digest"].(string)
	record := issuedTreePreview{
		Action: envelope.Action, ExpectedRevision: value.Revision(), BaseDigest: baseDigest,
		Arguments: arguments, Core: core, CreatedAt: time.Now(),
	}
	s.mu.Lock()
	if s.treePreviews == nil {
		s.treePreviews = map[string]issuedTreePreview{}
	}
	if len(s.treePreviews) >= maxIssuedTreePreviews {
		oldestKey := ""
		var oldest time.Time
		for key, candidate := range s.treePreviews {
			if oldestKey == "" || candidate.CreatedAt.Before(oldest) {
				oldestKey, oldest = key, candidate.CreatedAt
			}
		}
		delete(s.treePreviews, oldestKey)
	}
	s.treePreviews[token] = record
	s.mu.Unlock()
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true,
		"preview": map[string]any{
			"action": envelope.Action, "expected_revision": value.Revision(), "digest": token,
			"summary": summary, "affected_nodes": affected,
		},
	})
	return http.StatusOK
}

func (s *Server) treeActionCommit(writer http.ResponseWriter, request *http.Request, action string) int {
	if !validTreeAction(action) {
		writeError(writer, http.StatusBadRequest, "INVALID_TREE_ACTION", "INVALID_TREE_ACTION")
		return http.StatusBadRequest
	}
	var payload struct {
		Digest           string `json:"digest"`
		ExpectedRevision int64  `json:"expected_revision"`
	}
	if err := readJSON(request, &payload); err != nil || len(payload.Digest) != 64 || payload.ExpectedRevision < 0 {
		writeError(writer, http.StatusBadRequest, "INVALID_PREVIEW_DIGEST", "INVALID_PREVIEW_DIGEST")
		return http.StatusBadRequest
	}
	s.treeCommitMu.Lock()
	defer s.treeCommitMu.Unlock()
	s.mu.Lock()
	record, found := s.treePreviews[payload.Digest]
	delete(s.treePreviews, payload.Digest)
	s.mu.Unlock()
	if !found {
		writeError(writer, http.StatusConflict, "PREVIEW_NOT_FOUND", "PREVIEW_NOT_FOUND")
		return http.StatusConflict
	}
	if record.Action != action || record.ExpectedRevision != payload.ExpectedRevision {
		writeError(writer, http.StatusConflict, "PREVIEW_MISMATCH", "PREVIEW_MISMATCH")
		return http.StatusConflict
	}
	if err := s.ensureStores(); err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	value, err := s.treeStore.Load(request.Context())
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "TREE_NOT_CONFIGURED", "TREE_NOT_CONFIGURED")
		return http.StatusServiceUnavailable
	}
	currentDigest, _ := s.treePayload(value)["digest"].(string)
	if value.Revision() != record.ExpectedRevision {
		writeError(writer, http.StatusConflict, "REVISION_CONFLICT", "REVISION_CONFLICT")
		return http.StatusConflict
	}
	if currentDigest != record.BaseDigest {
		writeError(writer, http.StatusConflict, "TOPOLOGY_CHANGED", "TOPOLOGY_CHANGED")
		return http.StatusConflict
	}
	changed, err := tree.FromSnapshot(value.Snapshot())
	if err != nil {
		return writeTreeOperationError(writer, err)
	}
	arguments := record.Arguments
	switch action {
	case "create":
		node := tree.Node{NodeID: arguments.NodeID, Title: arguments.Title, Kind: arguments.Kind, MountID: arguments.MountID, Capability: arguments.Capability}
		if err = changed.AddNode(node, arguments.ParentID); err == nil {
			err = changed.IncrementRevision(record.ExpectedRevision)
		}
	case "attach":
		err = changed.Attach(arguments.NodeID, arguments.ParentID, record.Core, record.ExpectedRevision)
	case "detach":
		err = changed.Detach(arguments.NodeID, record.Core, record.ExpectedRevision)
	case "move":
		err = changed.Move(arguments.NodeID, arguments.ParentID, record.Core, record.ExpectedRevision)
	case "insert_between":
		err = changed.InsertBetween(arguments.ParentID, arguments.NodeID, arguments.MiddleID, record.Core, record.ExpectedRevision)
	}
	if err != nil {
		return writeTreeOperationError(writer, err)
	}
	if err := s.treeStore.Save(request.Context(), changed, record.ExpectedRevision); err != nil {
		return writeTreeOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "action": action, "revision": changed.Revision(), "tree": s.treePayload(changed),
	})
	return http.StatusOK
}

func planTreeAction(value *tree.Tree, action string, arguments treeActionArguments) (tree.Preview, map[string]any, []string, error) {
	if action == "create" {
		if _, exists := value.Node(arguments.NodeID); exists {
			return tree.Preview{}, nil, nil, errors.New("DUPLICATE_NODE")
		}
		if arguments.ParentSet {
			if _, exists := value.Node(arguments.ParentID); !exists {
				return tree.Preview{}, nil, nil, errors.New("UNKNOWN_NODE")
			}
		}
		chain := []string{arguments.NodeID}
		if arguments.ParentSet {
			parents, _ := value.Ancestors(arguments.ParentID)
			chain = append(chain, parents...)
		}
		affected := []string{arguments.NodeID}
		if arguments.ParentSet {
			affected = append(affected, arguments.ParentID)
		}
		sort.Strings(affected)
		return tree.Preview{}, map[string]any{
			"node_id": arguments.NodeID, "related_id": nullable(arguments.ParentID, arguments.ParentSet),
			"old_parent_id": nil, "new_parent_id": nullable(arguments.ParentID, arguments.ParentSet),
			"old_chain": []string{}, "new_chain": chain, "subtree_size": 1,
			"anchor_impact": tree.AnchorImpactPending,
		}, affected, nil
	}
	var (
		preview tree.Preview
		err     error
	)
	switch action {
	case "attach":
		preview, err = value.PreviewAttach(arguments.NodeID, arguments.ParentID)
	case "detach":
		preview, err = value.PreviewDetach(arguments.NodeID)
	case "move":
		preview, err = value.PreviewMove(arguments.NodeID, arguments.ParentID)
	case "insert_between":
		preview, err = value.PreviewInsertBetween(arguments.ParentID, arguments.NodeID, arguments.MiddleID)
	}
	if err != nil {
		return tree.Preview{}, nil, nil, err
	}
	members, _ := value.Subtree(arguments.NodeID)
	affectedSet := map[string]bool{arguments.NodeID: true}
	for _, node := range members {
		affectedSet[node.NodeID] = true
	}
	if arguments.ParentID != "" {
		affectedSet[arguments.ParentID] = true
	}
	if arguments.MiddleID != "" {
		affectedSet[arguments.MiddleID] = true
	}
	affected := make([]string, 0, len(affectedSet))
	for identifier := range affectedSet {
		affected = append(affected, identifier)
	}
	sort.Strings(affected)
	summary := map[string]any{
		"node_id": preview.NodeID, "related_id": preview.RelatedID,
		"old_parent_id": nullableText(preview.OldParentID), "new_parent_id": nullableText(preview.NewParentID),
		"old_chain": preview.OldChain, "new_chain": preview.NewChain,
		"subtree_size": preview.SubtreeSize, "anchor_impact": preview.AnchorImpact,
	}
	return preview, summary, affected, nil
}

func (s *Server) treePayload(value *tree.Tree) map[string]any {
	parents := map[string]string{}
	edges := make([]map[string]string, 0)
	for _, edge := range value.Edges() {
		parents[edge.Child] = edge.Parent
		edges = append(edges, map[string]string{"parent_id": edge.Parent, "child_id": edge.Child})
	}
	nodes := make([]map[string]any, 0)
	roots := make([]string, 0)
	for _, node := range value.Nodes() {
		parent := any(nil)
		if value, ok := parents[node.NodeID]; ok {
			parent = value
		} else {
			roots = append(roots, node.NodeID)
		}
		metadata := map[string]string{}
		if node.Kind == "mounted" {
			metadata["mount_id"], metadata["capability"] = node.MountID, node.Capability
		}
		item := map[string]any{
			"id": node.NodeID, "title": node.Title, "type": node.Kind,
			"parent_id": parent, "metadata": metadata,
		}
		if node.Kind == "group" {
			item["index"] = s.groupIndexStatus(node.NodeID)
		}
		nodes = append(nodes, item)
	}
	sort.Strings(roots)
	body := map[string]any{"revision": value.Revision(), "nodes": nodes, "edges": edges, "roots": roots}
	encoded, _ := json.Marshal(body)
	digest := sha256.Sum256(encoded)
	body["digest"] = hex.EncodeToString(digest[:])
	return body
}

func (s *Server) groupIndexStatus(nodeID string) map[string]any {
	s.groupIndexMu.RLock()
	status := rag.ReadGroupStatus(s.KnowledgeRoot, nodeID)
	s.groupIndexMu.RUnlock()
	result := map[string]any{"status": status.Status, "documents": status.Documents, "chunks": status.Chunks}
	if status.Generation != "" {
		result["generation"] = status.Generation
	}
	if status.IndexedAt != "" {
		result["indexed_at"] = status.IndexedAt
	}
	return result
}

func writeGroupIndexError(writer http.ResponseWriter, err error) int {
	code := err.Error()
	status := http.StatusServiceUnavailable
	switch code {
	case "INVALID_NODE_ID", "INVALID_DOCUMENT_IDS", "DUPLICATE_DOCUMENT_ID", "UNKNOWN_DOCUMENT_ID":
		status = http.StatusBadRequest
	case "SOURCE_CHANGED_DURING_BUILD":
		status = http.StatusConflict
	}
	if code == "" || strings.ContainsAny(code, " \t\r\n") {
		code = "GROUP_INDEX_BUILD_FAILED"
	}
	writeError(writer, status, code, code)
	return status
}

func writeTreeOperationError(writer http.ResponseWriter, err error) int {
	code := err.Error()
	status := http.StatusBadRequest
	if code == "UNKNOWN_NODE" {
		status = http.StatusNotFound
	} else if errors.Is(err, tree.ErrRevisionConflict) || errors.Is(err, tree.ErrPreviewMismatch) || strings.HasPrefix(code, "CYCLE") || strings.HasPrefix(code, "DUPLICATE") || strings.HasPrefix(code, "EDGE_") || strings.HasPrefix(code, "MULTIPLE") || strings.HasPrefix(code, "NODE_") || strings.HasPrefix(code, "PREVIEW") || strings.HasPrefix(code, "REVISION") || strings.HasPrefix(code, "SELF_PARENT") {
		status = http.StatusConflict
	}
	if code == "" || strings.ContainsAny(code, " \t\r\n") {
		code = "TREE_OPERATION_FAILED"
	}
	writeError(writer, status, code, code)
	return status
}

func nullable(value string, set bool) any {
	if !set {
		return nil
	}
	return value
}

func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

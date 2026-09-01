package library

import (
	"encoding/json"
	"fmt"
)

const (
	ActionCreate        = "create"
	ActionAttach        = "attach"
	ActionDetach        = "detach"
	ActionMove          = "move"
	ActionInsertBetween = "insert_between"
)

// MutationPreview is the persisted two-phase mutation contract used by the UI.
type MutationPreview struct {
	Action           string         `json:"action"`
	AffectedNodes    []string       `json:"affected_nodes"`
	Summary          PreviewSummary `json:"summary"`
	Digest           string         `json:"digest"`
	ExpectedRevision int64          `json:"expected_revision"`
}

// PreviewSummary contains the human-reviewable topology effects.
type PreviewSummary struct {
	NodeID       string   `json:"node_id"`
	RelatedID    *string  `json:"related_id"`
	OldParentID  *string  `json:"old_parent_id"`
	NewParentID  *string  `json:"new_parent_id"`
	OldChain     []string `json:"old_chain"`
	NewChain     []string `json:"new_chain"`
	SubtreeSize  int      `json:"subtree_size"`
	AnchorImpact string   `json:"anchor_impact"`
}

// PreviewRequest is the exact persisted-preview request envelope.
type PreviewRequest struct {
	Action  string
	Payload json.RawMessage
}

// CommitRequest is the exact digest-and-revision commit envelope.
type CommitRequest struct {
	Digest           string
	ExpectedRevision int64
}

type nodeParentRequest struct {
	NodeID   string
	ParentID string
}

type nodeRequest struct {
	NodeID string
}

type insertBetweenRequest struct {
	ParentID string
	NodeID   string
	MiddleID string
}

// DecodePreviewRequest rejects missing, extra, or duplicate envelope fields.
func DecodePreviewRequest(data []byte) (PreviewRequest, error) {
	fields, err := exactMutationFields(data, "action", "payload")
	if err != nil {
		return PreviewRequest{}, err
	}
	var request PreviewRequest
	if err := decodeField(fields, "action", &request.Action); err != nil || !validAction(request.Action) {
		return PreviewRequest{}, contractError("INVALID_ACTION")
	}
	request.Payload = append(json.RawMessage(nil), fields["payload"]...)
	return request, nil
}

// DecodeCommitRequest rejects incomplete CAS bindings and malformed digests.
func DecodeCommitRequest(data []byte) (CommitRequest, error) {
	fields, err := exactMutationFields(data, "digest", "expected_revision")
	if err != nil {
		return CommitRequest{}, err
	}
	var request CommitRequest
	if err := decodeField(fields, "digest", &request.Digest); err != nil || !validDigest(request.Digest) {
		return CommitRequest{}, contractError("INVALID_PREVIEW_DIGEST")
	}
	if err := decodeField(fields, "expected_revision", &request.ExpectedRevision); err != nil || request.ExpectedRevision < 0 {
		return CommitRequest{}, contractError("INVALID_EXPECTED_REVISION")
	}
	return request, nil
}

func planMutation(tree *Tree, action string, payload []byte) (Preview, error) {
	switch action {
	case ActionCreate:
		request, err := DecodeCreateRequest(payload)
		if err != nil {
			return Preview{}, err
		}
		return tree.PreviewCreate(request)
	case ActionAttach:
		request, err := decodeNodeParentRequest(payload)
		if err != nil {
			return Preview{}, err
		}
		return tree.PreviewAttach(request.NodeID, request.ParentID)
	case ActionDetach:
		request, err := decodeNodeRequest(payload)
		if err != nil {
			return Preview{}, err
		}
		return tree.PreviewDetach(request.NodeID)
	case ActionMove:
		request, err := decodeNodeParentRequest(payload)
		if err != nil {
			return Preview{}, err
		}
		return tree.PreviewMove(request.NodeID, request.ParentID)
	case ActionInsertBetween:
		request, err := decodeInsertBetweenRequest(payload)
		if err != nil {
			return Preview{}, err
		}
		return tree.PreviewInsertBetween(request.ParentID, request.NodeID, request.MiddleID)
	default:
		return Preview{}, contractError("INVALID_ACTION")
	}
}

func validAction(action string) bool {
	switch action {
	case ActionCreate, ActionAttach, ActionDetach, ActionMove, ActionInsertBetween:
		return true
	default:
		return false
	}
}

func commitMutation(tree *Tree, action string, payload []byte, preview Preview, revision int64) error {
	switch action {
	case ActionCreate:
		request, err := DecodeCreateRequest(payload)
		if err != nil {
			return err
		}
		return tree.Create(request, preview, revision)
	case ActionAttach:
		request, err := decodeNodeParentRequest(payload)
		if err != nil {
			return err
		}
		return tree.Attach(request.NodeID, request.ParentID, preview, revision)
	case ActionDetach:
		request, err := decodeNodeRequest(payload)
		if err != nil {
			return err
		}
		return tree.Detach(request.NodeID, preview, revision)
	case ActionMove:
		request, err := decodeNodeParentRequest(payload)
		if err != nil {
			return err
		}
		return tree.Move(request.NodeID, request.ParentID, preview, revision)
	case ActionInsertBetween:
		request, err := decodeInsertBetweenRequest(payload)
		if err != nil {
			return err
		}
		return tree.InsertBetween(request.ParentID, request.NodeID, request.MiddleID, preview, revision)
	default:
		return contractError("INVALID_ACTION")
	}
}

func mutationPreview(action string, preview Preview) MutationPreview {
	affected := append([]string(nil), preview.subtreeMembers...)
	if preview.RelatedID != nil && !containsString(affected, *preview.RelatedID) {
		affected = append(affected, *preview.RelatedID)
	}
	return MutationPreview{
		Action: action, AffectedNodes: affected,
		Summary: PreviewSummary{
			NodeID: preview.NodeID, RelatedID: cloneString(preview.RelatedID),
			OldParentID: cloneString(preview.OldParentID), NewParentID: cloneString(preview.NewParentID),
			OldChain: append([]string(nil), preview.OldChain...), NewChain: append([]string(nil), preview.NewChain...),
			SubtreeSize: preview.SubtreeSize, AnchorImpact: preview.AnchorImpact,
		},
		Digest: preview.PreviewDigest, ExpectedRevision: preview.BaseRevision,
	}
}

func decodeNodeParentRequest(data []byte) (nodeParentRequest, error) {
	fields, err := exactMutationFields(data, "node_id", "parent_id")
	if err != nil {
		return nodeParentRequest{}, err
	}
	var request nodeParentRequest
	if err := decodeIdentifierField(fields, "node_id", &request.NodeID, "INVALID_NODE_ID"); err != nil {
		return nodeParentRequest{}, err
	}
	if err := decodeIdentifierField(fields, "parent_id", &request.ParentID, "INVALID_PARENT_ID"); err != nil {
		return nodeParentRequest{}, err
	}
	return request, nil
}

func decodeNodeRequest(data []byte) (nodeRequest, error) {
	fields, err := exactMutationFields(data, "node_id")
	if err != nil {
		return nodeRequest{}, err
	}
	var request nodeRequest
	if err := decodeIdentifierField(fields, "node_id", &request.NodeID, "INVALID_NODE_ID"); err != nil {
		return nodeRequest{}, err
	}
	return request, nil
}

func decodeInsertBetweenRequest(data []byte) (insertBetweenRequest, error) {
	fields, err := exactMutationFields(data, "parent_id", "node_id", "middle_id")
	if err != nil {
		return insertBetweenRequest{}, err
	}
	var request insertBetweenRequest
	for _, field := range []struct {
		name  string
		value *string
		code  string
	}{{"parent_id", &request.ParentID, "INVALID_PARENT_ID"}, {"node_id", &request.NodeID, "INVALID_NODE_ID"}, {"middle_id", &request.MiddleID, "INVALID_NODE_ID"}} {
		if err := decodeIdentifierField(fields, field.name, field.value, field.code); err != nil {
			return insertBetweenRequest{}, err
		}
	}
	return request, nil
}

func exactMutationFields(data []byte, names ...string) (map[string]json.RawMessage, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return nil, err
	}
	required := make(map[string]struct{}, len(names))
	for _, name := range names {
		required[name] = struct{}{}
	}
	if !hasExactFields(fields, required) {
		return nil, contractError("INVALID_REQUEST_FIELDS")
	}
	return fields, nil
}

func decodeIdentifierField(fields map[string]json.RawMessage, name string, target *string, code string) error {
	if err := decodeField(fields, name, target); err != nil || !identifierPattern.MatchString(*target) {
		return contractError(code)
	}
	return nil
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}

func marshalMutationPayload(payload []byte) ([]byte, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, fmt.Errorf("decode mutation payload: %w", err)
	}
	result, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode mutation payload: %w", err)
	}
	return result, nil
}

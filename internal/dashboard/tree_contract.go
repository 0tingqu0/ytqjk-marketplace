package dashboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

func decodeTreeArguments(action string, raw json.RawMessage) (treeActionArguments, error) {
	if action == "create" {
		var payload struct {
			NodeID   string            `json:"node_id"`
			Title    string            `json:"title"`
			Type     string            `json:"type"`
			ParentID *string           `json:"parent_id"`
			Metadata map[string]string `json:"metadata"`
		}
		if err := decodeRawExact(raw, &payload, "node_id", "title", "type", "parent_id", "metadata"); err != nil ||
			!safeIdentifier(payload.NodeID) || (payload.Type != "group" && payload.Type != "mounted") {
			return treeActionArguments{}, errors.New("INVALID_REQUEST_FIELDS")
		}
		arguments := treeActionArguments{NodeID: payload.NodeID, Title: payload.Title, Kind: payload.Type}
		if payload.ParentID != nil {
			if !safeIdentifier(*payload.ParentID) {
				return treeActionArguments{}, errors.New("INVALID_PARENT_ID")
			}
			arguments.ParentID, arguments.ParentSet = *payload.ParentID, true
		}
		if payload.Type == "group" && len(payload.Metadata) != 0 {
			return treeActionArguments{}, errors.New("GROUP_METADATA_FORBIDDEN")
		}
		if payload.Type == "mounted" {
			if len(payload.Metadata) != 2 || !safeIdentifier(payload.Metadata["mount_id"]) ||
				!safeIdentifier(payload.Metadata["capability"]) {
				return treeActionArguments{}, errors.New("INVALID_MOUNT_METADATA")
			}
			arguments.MountID, arguments.Capability = payload.Metadata["mount_id"], payload.Metadata["capability"]
		}
		candidate := tree.Node{
			NodeID: arguments.NodeID, Title: arguments.Title, Kind: arguments.Kind,
			MountID: arguments.MountID, Capability: arguments.Capability,
		}
		probe := tree.Default()
		if err := probe.AddNode(candidate, ""); err != nil {
			return treeActionArguments{}, err
		}
		return arguments, nil
	}
	if action == "detach" {
		var payload struct {
			NodeID string `json:"node_id"`
		}
		if err := decodeRawExact(raw, &payload, "node_id"); err != nil || !safeIdentifier(payload.NodeID) {
			return treeActionArguments{}, errors.New("INVALID_NODE_ID")
		}
		return treeActionArguments{NodeID: payload.NodeID}, nil
	}
	if action == "insert_between" {
		var payload struct {
			ParentID string `json:"parent_id"`
			NodeID   string `json:"node_id"`
			MiddleID string `json:"middle_id"`
		}
		if err := decodeRawExact(raw, &payload, "parent_id", "node_id", "middle_id"); err != nil ||
			!safeIdentifier(payload.ParentID) || !safeIdentifier(payload.NodeID) || !safeIdentifier(payload.MiddleID) {
			return treeActionArguments{}, errors.New("INVALID_REQUEST_FIELDS")
		}
		return treeActionArguments{
			ParentID: payload.ParentID, ParentSet: true, NodeID: payload.NodeID, MiddleID: payload.MiddleID,
		}, nil
	}
	var payload struct {
		NodeID   string `json:"node_id"`
		ParentID string `json:"parent_id"`
	}
	if err := decodeRawExact(raw, &payload, "node_id", "parent_id"); err != nil ||
		!safeIdentifier(payload.NodeID) || !safeIdentifier(payload.ParentID) {
		return treeActionArguments{}, errors.New("INVALID_REQUEST_FIELDS")
	}
	return treeActionArguments{NodeID: payload.NodeID, ParentID: payload.ParentID, ParentSet: true}, nil
}

func decodeRawExact(raw json.RawMessage, target any, fields ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil || len(object) != len(fields) {
		return errors.New("INVALID_REQUEST_FIELDS")
	}
	for _, field := range fields {
		if _, ok := object[field]; !ok {
			return errors.New("INVALID_REQUEST_FIELDS")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("INVALID_REQUEST_FIELDS")
	}
	return nil
}

func validTreeAction(action string) bool {
	return action == "attach" || action == "create" || action == "detach" ||
		action == "insert_between" || action == "move"
}

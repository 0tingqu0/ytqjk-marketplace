package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sort"

	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
	"github.com/0tingqu0/ytqjk-marketplace/internal/tree"
)

var peerErrorPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,127}$`)

type peerUpsertPayload struct {
	ExpectedRevision int64
	PeerID           string
	Title            string
	ProjectID        string
	Endpoint         string
	Secret           *string
	RemoteNodeID     *string
	ExportNodeIDs    []string
	AllowInsecure    bool
	Enabled          bool
}

type peerMount struct{ NodeID, MountID string }

func siblingPeerMounts(value *tree.Tree, projectID string) ([]peerMount, error) {
	node, ok := value.Node(projectID)
	if !ok || node.Kind != "project" {
		return nil, errors.New("CURRENT_PROJECT_TREE_NODE_MISSING")
	}
	parent, hasParent := value.Parent(projectID)
	if !hasParent {
		return []peerMount{}, nil
	}
	result := make([]peerMount, 0)
	for _, candidate := range value.Nodes() {
		candidateParent, ok := value.Parent(candidate.NodeID)
		if !ok || candidateParent != parent || candidate.NodeID == projectID {
			continue
		}
		if candidate.Kind == "mounted" && candidate.Capability == "query-v1" && candidate.MountID != "" {
			result = append(result, peerMount{NodeID: candidate.NodeID, MountID: candidate.MountID})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].NodeID < result[j].NodeID })
	return result, nil
}

func peerSettingsPayload(status string, settings peer.Settings, runtime PeerRuntimeStatus) map[string]any {
	return map[string]any{
		"ok": true, "status": status, "peer_service": settings.Public(), "runtime": runtime,
	}
}

func resolvePeerSecret(settings peer.Settings, peerID string, supplied *string) (string, error) {
	if supplied != nil {
		return *supplied, nil
	}
	record, ok := settings.Peer(peerID)
	if !ok {
		return "", errors.New("PEER_SECRET_REQUIRED")
	}
	return record.Secret, nil
}

func readPeerUpsert(request *http.Request) (peerUpsertPayload, error) {
	data, err := readPeerBody(request)
	if err != nil {
		return peerUpsertPayload{}, err
	}
	type payload struct {
		ExpectedRevision int64    `json:"expected_revision"`
		PeerID           string   `json:"peer_id"`
		Title            string   `json:"title"`
		ProjectID        string   `json:"project_id"`
		Endpoint         string   `json:"endpoint"`
		Secret           *string  `json:"secret"`
		RemoteNodeID     *string  `json:"remote_node_id"`
		ExportNodeIDs    []string `json:"export_node_ids"`
		ExportNodeID     string   `json:"export_node_id"`
		AllowInsecure    bool     `json:"allow_insecure"`
		Enabled          bool     `json:"enabled"`
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return peerUpsertPayload{}, errors.New("INVALID_PEER_REQUEST_FIELDS")
	}
	var value payload
	common := []string{"expected_revision", "peer_id", "title", "project_id", "endpoint", "secret", "remote_node_id", "allow_insecure", "enabled"}
	if _, ok := object["export_node_ids"]; ok {
		if err := decodeRawExact(data, &value, append(common, "export_node_ids")...); err != nil {
			return peerUpsertPayload{}, errors.New("INVALID_PEER_REQUEST_FIELDS")
		}
	} else if _, ok := object["export_node_id"]; ok {
		if err := decodeRawExact(data, &value, append(common, "export_node_id")...); err != nil {
			return peerUpsertPayload{}, errors.New("INVALID_PEER_REQUEST_FIELDS")
		}
		value.ExportNodeIDs = []string{value.ExportNodeID}
	} else {
		return peerUpsertPayload{}, errors.New("INVALID_PEER_REQUEST_FIELDS")
	}
	return peerUpsertPayload{
		ExpectedRevision: value.ExpectedRevision, PeerID: value.PeerID, Title: value.Title,
		ProjectID: value.ProjectID, Endpoint: value.Endpoint, Secret: value.Secret,
		RemoteNodeID: value.RemoteNodeID, ExportNodeIDs: value.ExportNodeIDs,
		AllowInsecure: value.AllowInsecure, Enabled: value.Enabled,
	}, nil
}

func readPeerExact(request *http.Request, target any, fields ...string) error {
	data, err := readPeerBody(request)
	if err != nil {
		return err
	}
	if err := decodeRawExact(data, target, fields...); err != nil {
		return errors.New("INVALID_PEER_REQUEST_FIELDS")
	}
	return nil
}

func readPeerBody(request *http.Request) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(request.Body, peer.MaxBodyBytes+1))
	if err != nil || len(data) > peer.MaxBodyBytes {
		return nil, errors.New("INVALID_PEER_REQUEST_FIELDS")
	}
	return data, nil
}

func writePeerOperationError(writer http.ResponseWriter, err error) int {
	code := safePeerCode(err)
	status := http.StatusBadRequest
	if code == "PEER_REVISION_CONFLICT" {
		status = http.StatusConflict
	} else if code == "PEER_CONFIG_NOT_CONFIGURED" || code == "PEER_NOT_CONFIGURED" || code == "PEER_UNAVAILABLE" || code == "PEER_RESPONSE_INVALID" {
		status = http.StatusServiceUnavailable
	} else if code == "UNKNOWN_PEER" || code == "PEER_MATERIAL_NOT_FOUND" {
		status = http.StatusNotFound
	}
	writeError(writer, status, code, code)
	return status
}

func safePeerCode(err error) string {
	if err != nil && peerErrorPattern.MatchString(err.Error()) {
		return err.Error()
	}
	return "PEER_OPERATION_FAILED"
}

package dashboard

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/peer"
)

func (s *Server) peerSnapshot(writer http.ResponseWriter, request *http.Request) int {
	if err := s.ensureStores(); err != nil {
		return writePeerOperationError(writer, err)
	}
	settings, err := s.peerStore.Load(request.Context())
	if errors.Is(err, peer.ErrNotConfigured) {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "status": "NOT_CONFIGURED", "peer_service": nil,
			"runtime": s.peerRuntimeStatus(),
		})
		return http.StatusOK
	}
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, peerSettingsPayload("CONFIGURED", settings, s.peerRuntimeStatus()))
	return http.StatusOK
}

func (s *Server) peerAction(writer http.ResponseWriter, request *http.Request, action string) int {
	if err := s.ensureStores(); err != nil {
		return writePeerOperationError(writer, err)
	}
	switch action {
	case "bootstrap":
		if err := readPeerExact(request, &struct{}{}); err != nil {
			return writePeerOperationError(writer, err)
		}
		settings, err := s.peerStore.Bootstrap(request.Context())
		if err != nil {
			return writePeerOperationError(writer, err)
		}
		writeJSON(writer, http.StatusOK, peerSettingsPayload("CONFIGURED", settings, s.peerRuntimeStatus()))
		return http.StatusOK
	case "secret":
		if err := readPeerExact(request, &struct{}{}); err != nil {
			return writePeerOperationError(writer, err)
		}
		settings, err := s.peerStore.Bootstrap(request.Context())
		if err != nil {
			return writePeerOperationError(writer, err)
		}
		secret, err := peer.NewSecret()
		if err != nil {
			return writePeerOperationError(writer, err)
		}
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "local_peer_id": settings.LocalPeerID,
			"secret": secret, "one_time_display": true,
		})
		return http.StatusOK
	case "configure":
		return s.configurePeer(writer, request)
	case "upsert":
		return s.upsertPeer(writer, request)
	case "discover":
		return s.discoverPeer(writer, request)
	case "remove":
		return s.removePeer(writer, request)
	case "health":
		return s.healthPeer(writer, request)
	case "dispatch":
		return s.dispatchPeers(writer, request)
	case "material":
		return s.peerMaterial(writer, request)
	default:
		writeError(writer, http.StatusNotFound, "NOT_FOUND", "API not found")
		return http.StatusNotFound
	}
}

func (s *Server) configurePeer(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		ExpectedRevision int64  `json:"expected_revision"`
		Enabled          bool   `json:"enabled"`
		BindHost         string `json:"bind_host"`
		Port             int    `json:"port"`
		AllowInsecureLAN bool   `json:"allow_insecure_lan"`
	}
	if err := readPeerExact(request, &payload, "expected_revision", "enabled", "bind_host", "port", "allow_insecure_lan"); err != nil || payload.ExpectedRevision < 0 {
		return writePeerOperationError(writer, errors.New("INVALID_PEER_REQUEST_FIELDS"))
	}
	settings, err := s.peerStore.Configure(request.Context(), payload.ExpectedRevision, payload.Enabled, payload.BindHost, payload.Port, payload.AllowInsecureLAN)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, peerSettingsPayload("RESTART_REQUIRED", settings, s.peerRuntimeStatus()))
	return http.StatusOK
}

func (s *Server) upsertPeer(writer http.ResponseWriter, request *http.Request) int {
	payload, err := readPeerUpsert(request)
	if err != nil || payload.ExpectedRevision < 0 {
		return writePeerOperationError(writer, errors.New("INVALID_PEER_REQUEST_FIELDS"))
	}
	settings, err := s.peerStore.Load(request.Context())
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	secret, err := resolvePeerSecret(settings, payload.PeerID, payload.Secret)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	remoteNode := ""
	if payload.RemoteNodeID != nil {
		remoteNode = *payload.RemoteNodeID
	}
	record := peer.Record{
		PeerID: payload.PeerID, Title: payload.Title, ProjectID: payload.ProjectID,
		Endpoint: payload.Endpoint, Secret: secret, RemoteNodeID: remoteNode,
		ExportNodeIDs: payload.ExportNodeIDs, AllowInsecure: payload.AllowInsecure,
		Enabled: payload.Enabled,
	}
	value, err := s.treeStore.Load(request.Context())
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	if _, _, err := peer.ExportCatalog(s.KnowledgeRoot, record.ProjectID, record.ExportNodeIDs, value); err != nil {
		return writePeerOperationError(writer, err)
	}
	settings, err = s.peerStore.Upsert(request.Context(), payload.ExpectedRevision, record)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, peerSettingsPayload("PEER_SAVED", settings, s.peerRuntimeStatus()))
	return http.StatusOK
}

func (s *Server) discoverPeer(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		PeerID        string  `json:"peer_id"`
		ProjectID     string  `json:"project_id"`
		Endpoint      string  `json:"endpoint"`
		Secret        *string `json:"secret"`
		AllowInsecure bool    `json:"allow_insecure"`
	}
	if err := readPeerExact(request, &payload, "peer_id", "project_id", "endpoint", "secret", "allow_insecure"); err != nil {
		return writePeerOperationError(writer, err)
	}
	settings, err := s.peerStore.Load(request.Context())
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	if payload.PeerID == settings.LocalPeerID {
		return writePeerOperationError(writer, errors.New("SELF_PEER_FORBIDDEN"))
	}
	secret, err := resolvePeerSecret(settings, payload.PeerID, payload.Secret)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	draft := peer.Record{
		PeerID: payload.PeerID, Title: "Peer discovery", ProjectID: payload.ProjectID,
		Endpoint: payload.Endpoint, Secret: secret, ExportNodeIDs: []string{payload.ProjectID},
		AllowInsecure: payload.AllowInsecure, Enabled: true,
	}
	result, err := peer.NewClient(s.peerStore).Discover(request.Context(), draft)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "status": "PEER_DISCOVERED", "peer": result})
	return http.StatusOK
}

func (s *Server) removePeer(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		ExpectedRevision int64  `json:"expected_revision"`
		PeerID           string `json:"peer_id"`
	}
	if err := readPeerExact(request, &payload, "expected_revision", "peer_id"); err != nil || payload.ExpectedRevision < 0 {
		return writePeerOperationError(writer, errors.New("INVALID_PEER_REQUEST_FIELDS"))
	}
	settings, err := s.peerStore.Remove(request.Context(), payload.ExpectedRevision, payload.PeerID)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, peerSettingsPayload("PEER_REMOVED", settings, s.peerRuntimeStatus()))
	return http.StatusOK
}

func (s *Server) healthPeer(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		MountID   string `json:"mount_id"`
		ProjectID string `json:"project_id"`
	}
	if err := readPeerExact(request, &payload, "mount_id", "project_id"); err != nil {
		return writePeerOperationError(writer, err)
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	result, err := peer.NewClient(s.peerStore).Health(ctx, payload.MountID, payload.ProjectID)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "peer": result})
	return http.StatusOK
}

type peerDispatchRow struct {
	peer.QueryRow
	PeerID    string `json:"peer_id"`
	MountNode string `json:"mount_node"`
}

type peerDispatchStatus struct {
	NodeID      string `json:"node_id"`
	MountID     string `json:"mount_id"`
	Status      string `json:"status"`
	Reason      string `json:"reason,omitempty"`
	ResultCount int    `json:"result_count"`
}

func (s *Server) dispatchPeers(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		ProjectID string `json:"project_id"`
		Query     string `json:"query"`
		Limit     int    `json:"limit"`
	}
	if err := readPeerExact(request, &payload, "project_id", "query", "limit"); err != nil || payload.Limit < 1 || payload.Limit > 20 {
		return writePeerOperationError(writer, errors.New("INVALID_PEER_REQUEST_FIELDS"))
	}
	value, err := s.treeStore.Load(request.Context())
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	mounts, err := siblingPeerMounts(value, payload.ProjectID)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	type outcome struct {
		mount peerMount
		value peer.RemoteQueryResponse
		err   error
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	resultsChannel := make(chan outcome, len(mounts))
	semaphore := make(chan struct{}, 8)
	client := peer.NewClient(s.peerStore)
	for _, mount := range mounts {
		mount := mount
		go func() {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				resultsChannel <- outcome{mount: mount, err: errors.New("PEER_UNAVAILABLE")}
				return
			}
			value, err := client.Query(ctx, mount.MountID, payload.ProjectID, payload.Query, payload.Limit)
			resultsChannel <- outcome{mount: mount, value: value, err: err}
		}()
	}
	outcomes := make([]outcome, 0, len(mounts))
	for range mounts {
		outcomes = append(outcomes, <-resultsChannel)
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].mount.NodeID < outcomes[j].mount.NodeID })
	rows := make([]peerDispatchRow, 0)
	statuses := make([]peerDispatchStatus, 0, len(outcomes))
	available := 0
	for _, item := range outcomes {
		if item.err != nil {
			statuses = append(statuses, peerDispatchStatus{
				NodeID: item.mount.NodeID, MountID: item.mount.MountID,
				Status: "UNAVAILABLE", Reason: safePeerCode(item.err),
			})
			continue
		}
		available++
		statuses = append(statuses, peerDispatchStatus{
			NodeID: item.mount.NodeID, MountID: item.mount.MountID,
			Status: item.value.Status, ResultCount: len(item.value.Results),
		})
		for _, row := range item.value.Results {
			if len(rows) >= payload.Limit {
				break
			}
			rows = append(rows, peerDispatchRow{QueryRow: row, PeerID: item.value.PeerID, MountNode: item.mount.NodeID})
		}
	}
	status := "PEER_DISPATCH_MISS"
	if len(rows) > 0 {
		status = "PEER_DISPATCH_HIT"
	} else if available == 0 && len(mounts) > 0 {
		status = "PEER_DISPATCH_UNAVAILABLE"
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "status": status, "project_id": payload.ProjectID,
		"scope": "explicit-same-parent-peer-dispatch", "result_count": len(rows),
		"results": rows, "peers": statuses,
	})
	return http.StatusOK
}

func (s *Server) peerMaterial(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		ProjectID         string `json:"project_id"`
		NodeID            string `json:"node_id"`
		RemoteLibraryNode string `json:"remote_library_node"`
		MaterialID        string `json:"material_id"`
	}
	if err := readPeerExact(request, &payload, "project_id", "node_id", "remote_library_node", "material_id"); err != nil {
		return writePeerOperationError(writer, err)
	}
	value, err := s.treeStore.Load(request.Context())
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	mounts, err := siblingPeerMounts(value, payload.ProjectID)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	mountID := ""
	for _, mount := range mounts {
		if mount.NodeID == payload.NodeID {
			mountID = mount.MountID
			break
		}
	}
	if mountID == "" {
		return writePeerOperationError(writer, errors.New("PEER_LIBRARY_NOT_SIBLING"))
	}
	ctx, cancel := context.WithTimeout(request.Context(), 20*time.Second)
	defer cancel()
	material, err := peer.NewClient(s.peerStore).Material(ctx, mountID, payload.ProjectID, payload.MaterialID, payload.RemoteLibraryNode)
	if err != nil {
		return writePeerOperationError(writer, err)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "status": "PEER_MATERIAL_READY", "project_id": payload.ProjectID,
		"mount_node": payload.NodeID, "library_node": material.LibraryNode,
		"remote_library_node": material.LibraryNode, "material": material,
	})
	return http.StatusOK
}

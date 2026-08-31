package peer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

type HealthResponse struct {
	OK           bool     `json:"ok"`
	Status       string   `json:"status"`
	PeerID       string   `json:"peer_id"`
	ProjectID    string   `json:"project_id"`
	ExportNodes  []Export `json:"export_nodes"`
	LibraryCount int      `json:"library_count"`
	Capabilities []string `json:"capabilities"`
}

type RemoteQueryResponse struct {
	OK         bool       `json:"ok"`
	Status     string     `json:"status"`
	PeerID     string     `json:"peer_id"`
	ProjectID  string     `json:"project_id"`
	NodeID     string     `json:"node_id"`
	Generation string     `json:"generation"`
	Results    []QueryRow `json:"results"`
}

type RemoteMaterialResponse struct {
	OK          bool     `json:"ok"`
	Status      string   `json:"status"`
	PeerID      string   `json:"peer_id"`
	ProjectID   string   `json:"project_id"`
	NodeID      string   `json:"node_id"`
	LibraryNode string   `json:"library_node"`
	Material    QueryRow `json:"material"`
}

type Client struct {
	Store      *Store
	HTTPClient *http.Client
	Clock      func() time.Time
}

func NewClient(store *Store) *Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, DisableCompression: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
	}
	return &Client{
		Store: store,
		HTTPClient: &http.Client{
			Transport: transport, Timeout: 15 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

func (c *Client) Health(ctx context.Context, peerID, projectID string) (HealthResponse, error) {
	settings, record, err := c.savedPeer(ctx, peerID, projectID)
	if err != nil {
		return HealthResponse{}, err
	}
	result, err := c.health(ctx, settings.LocalPeerID, record, projectID)
	if err != nil {
		return HealthResponse{}, err
	}
	if record.RemoteNodeID != "" && !containsExport(result.ExportNodes, record.RemoteNodeID) {
		return HealthResponse{}, errors.New("PEER_NODE_MISMATCH")
	}
	return result, nil
}

func (c *Client) Discover(ctx context.Context, draft Record) (HealthResponse, error) {
	if c.Store == nil {
		return HealthResponse{}, errors.New("PEER_CONFIG_NOT_CONFIGURED")
	}
	settings, err := c.Store.Load(ctx)
	if err != nil {
		return HealthResponse{}, err
	}
	record, err := ValidateRecord(draft)
	if err != nil {
		return HealthResponse{}, err
	}
	return c.health(ctx, settings.LocalPeerID, record, record.ProjectID)
}

func (c *Client) Query(ctx context.Context, peerID, projectID, query string, limit int) (RemoteQueryResponse, error) {
	if !utf8.ValidString(query) || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > 2000 || limit < 1 || limit > 20 {
		return RemoteQueryResponse{}, errors.New("INVALID_PEER_QUERY")
	}
	settings, record, err := c.savedPeer(ctx, peerID, projectID)
	if err != nil {
		return RemoteQueryResponse{}, err
	}
	if record.RemoteNodeID == "" {
		return RemoteQueryResponse{}, errors.New("PEER_REMOTE_NODE_REQUIRED")
	}
	body, headers, status, nonce, err := c.post(ctx, settings.LocalPeerID, record, "/v1/query", map[string]any{
		"project_id": projectID, "node_id": record.RemoteNodeID, "query": query, "limit": limit,
	})
	if err != nil {
		return RemoteQueryResponse{}, err
	}
	var result RemoteQueryResponse
	if err := c.verifyPostResponse(record, "/v1/query", nonce, headers, status, body); err != nil {
		return RemoteQueryResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if status != http.StatusOK || decodeExact(body, &result, "ok", "status", "peer_id", "project_id", "node_id", "generation", "results") != nil {
		return RemoteQueryResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if !result.OK || (result.Status != "PEER_HIT" && result.Status != "PEER_MISS") || result.PeerID != record.PeerID || result.ProjectID != projectID || result.NodeID != record.RemoteNodeID || len(result.Generation) > 4096 || len(result.Results) > limit {
		return RemoteQueryResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	for _, row := range result.Results {
		if err := validateQueryRow(row); err != nil {
			return RemoteQueryResponse{}, err
		}
	}
	return result, nil
}

func (c *Client) Material(ctx context.Context, peerID, projectID, materialID, remoteLibraryNode string) (QueryRow, error) {
	if _, _, err := parseMaterialID(materialID); err != nil {
		return QueryRow{}, err
	}
	settings, record, err := c.savedPeer(ctx, peerID, projectID)
	if err != nil {
		return QueryRow{}, err
	}
	if record.RemoteNodeID == "" {
		return QueryRow{}, errors.New("PEER_REMOTE_NODE_REQUIRED")
	}
	if remoteLibraryNode == "" {
		remoteLibraryNode = record.RemoteNodeID
	}
	if !validIdentifier(remoteLibraryNode) {
		return QueryRow{}, errors.New("INVALID_LIBRARY_NODE")
	}
	body, headers, status, nonce, err := c.post(ctx, settings.LocalPeerID, record, "/v1/material", map[string]any{
		"project_id": projectID, "node_id": record.RemoteNodeID,
		"library_node": remoteLibraryNode, "material_id": materialID,
	})
	if err != nil {
		return QueryRow{}, err
	}
	var result RemoteMaterialResponse
	if err := c.verifyPostResponse(record, "/v1/material", nonce, headers, status, body); err != nil {
		return QueryRow{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if status != http.StatusOK || decodeExact(body, &result, "ok", "status", "peer_id", "project_id", "node_id", "library_node", "material") != nil {
		return QueryRow{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if !result.OK || result.Status != "MATERIAL_READY" || result.PeerID != record.PeerID || result.ProjectID != projectID || result.NodeID != record.RemoteNodeID || result.LibraryNode != remoteLibraryNode || result.Material.LibraryNode != remoteLibraryNode {
		return QueryRow{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if err := validateQueryRow(result.Material); err != nil {
		return QueryRow{}, err
	}
	return result.Material, nil
}

func (c *Client) health(ctx context.Context, localPeerID string, record Record, projectID string) (HealthResponse, error) {
	body, headers, status, nonce, err := c.post(ctx, localPeerID, record, "/v1/health", map[string]any{"project_id": projectID})
	if err != nil {
		return HealthResponse{}, err
	}
	var result HealthResponse
	if err := c.verifyPostResponse(record, "/v1/health", nonce, headers, status, body); err != nil {
		return HealthResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if status != http.StatusOK || decodeExact(body, &result, "ok", "status", "peer_id", "project_id", "export_nodes", "library_count", "capabilities") != nil {
		return HealthResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	if !result.OK || result.Status != "READY" || result.PeerID != record.PeerID || result.ProjectID != projectID || result.LibraryCount < len(result.ExportNodes) || len(result.ExportNodes) < 1 || len(result.ExportNodes) > 64 || !equalStrings(result.Capabilities, []string{"query-v1", "material-v1", "response-hmac-v1"}) {
		return HealthResponse{}, errors.New("PEER_RESPONSE_INVALID")
	}
	seen := map[string]bool{}
	for _, export := range result.ExportNodes {
		if !validIdentifier(export.ID) || strings.TrimSpace(export.Title) != export.Title || export.Title == "" || utf8.RuneCountInString(export.Title) > 200 || (export.Type != "global" && export.Type != "group" && export.Type != "project") || seen[export.ID] {
			return HealthResponse{}, errors.New("PEER_RESPONSE_INVALID")
		}
		seen[export.ID] = true
	}
	return result, nil
}

func (c *Client) savedPeer(ctx context.Context, peerID, projectID string) (Settings, Record, error) {
	if c.Store == nil {
		return Settings{}, Record{}, errors.New("PEER_CONFIG_NOT_CONFIGURED")
	}
	settings, err := c.Store.Load(ctx)
	if err != nil {
		return Settings{}, Record{}, err
	}
	record, found := settings.Peer(peerID)
	if !found || !record.Enabled {
		return Settings{}, Record{}, errors.New("PEER_NOT_CONFIGURED")
	}
	if record.ProjectID != projectID {
		return Settings{}, Record{}, errors.New("PEER_PROJECT_MISMATCH")
	}
	return settings, record, nil
}

func (c *Client) post(ctx context.Context, localPeerID string, record Record, path string, payload any) ([]byte, http.Header, int, string, error) {
	content, err := json.Marshal(payload)
	if err != nil || len(content) > MaxBodyBytes {
		return nil, nil, 0, "", errors.New("PEER_REQUEST_TOO_LARGE")
	}
	now := time.Now()
	if c.Clock != nil {
		now = c.Clock()
	}
	headers, err := SignedHeaders(localPeerID, record.Secret, http.MethodPost, path, content, now, "")
	if err != nil {
		return nil, nil, 0, "", err
	}
	nonce := headers.Get(NonceHeader)
	headers.Set("Content-Type", "application/json")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, record.Endpoint+path, bytes.NewReader(content))
	if err != nil {
		return nil, nil, 0, "", errors.New("PEER_UNAVAILABLE")
	}
	request.Header = headers
	client := c.HTTPClient
	if client == nil {
		client = NewClient(c.Store).HTTPClient
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, nil, 0, "", errors.New("PEER_UNAVAILABLE")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil || len(body) > MaxResponseBytes {
		return nil, nil, 0, "", errors.New("PEER_RESPONSE_INVALID")
	}
	return body, response.Header, response.StatusCode, nonce, nil
}

func (c *Client) verifyPostResponse(record Record, path, nonce string, headers http.Header, status int, body []byte) error {
	if err := VerifyResponseHeaders(headers, record.Secret, record.PeerID, status, path, nonce, body); err != nil {
		return errors.New("PEER_RESPONSE_INVALID")
	}
	return nil
}

func validateQueryRow(row QueryRow) error {
	prefix, identifier, err := parseMaterialID(row.MaterialID)
	if err != nil || (prefix != "project" && prefix != "library") || !signaturePattern.MatchString(identifier) || !validIdentifier(row.LibraryNode) || row.Path == "" || len(row.Path) > 4096 || row.LineStart < 1 || row.LineEnd < row.LineStart || !utf8.ValidString(row.Content) || utf8.RuneCountInString(row.Content) > maxPeerContentRunes || !signaturePattern.MatchString(row.SourceSHA) || row.Scope == "" || len(row.Scope) > 128 || math.IsNaN(row.Score) || math.IsInf(row.Score, 0) {
		return errors.New("PEER_RESPONSE_INVALID")
	}
	return nil
}

func containsExport(exports []Export, identifier string) bool {
	index := sort.Search(len(exports), func(index int) bool { return exports[index].ID >= identifier })
	if index < len(exports) && exports[index].ID == identifier {
		return true
	}
	for _, export := range exports {
		if export.ID == identifier {
			return true
		}
	}
	return false
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

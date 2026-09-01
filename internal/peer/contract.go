package peer

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	MaxBodyBytes       = 64 * 1024
	MaxResponseBytes   = 512 * 1024
	MaxClockSkew       = 120 * time.Second
	ReplayWindow       = 240 * time.Second
	PeerHeader         = "X-YTQJK-Peer"
	TimestampHeader    = "X-YTQJK-Timestamp"
	NonceHeader        = "X-YTQJK-Nonce"
	SignatureHeader    = "X-YTQJK-Signature"
	ResponsePeerHeader = "X-YTQJK-Response-Peer"
	ResponseSigHeader  = "X-YTQJK-Response-Signature"
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	noncePattern      = regexp.MustCompile(`^[A-Za-z0-9_-]{22,64}$`)
	signaturePattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Record struct {
	PeerID        string   `json:"peer_id"`
	Title         string   `json:"title"`
	ProjectID     string   `json:"project_id"`
	Endpoint      string   `json:"endpoint"`
	Secret        string   `json:"secret"`
	RemoteNodeID  string   `json:"remote_node_id,omitempty"`
	ExportNodeIDs []string `json:"export_node_ids"`
	AllowInsecure bool     `json:"allow_insecure"`
	Enabled       bool     `json:"enabled"`
}

type PublicRecord struct {
	PeerID         string   `json:"peer_id"`
	Title          string   `json:"title"`
	ProjectID      string   `json:"project_id"`
	Endpoint       string   `json:"endpoint"`
	RemoteNodeID   string   `json:"remote_node_id,omitempty"`
	ExportNodeID   string   `json:"export_node_id"`
	ExportNodeIDs  []string `json:"export_node_ids"`
	AllowInsecure  bool     `json:"allow_insecure"`
	Enabled        bool     `json:"enabled"`
	KeyFingerprint string   `json:"key_fingerprint"`
}

type Auth struct {
	PeerID    string
	Nonce     string
	Timestamp int64
}

func ValidateRecord(record Record) (Record, error) {
	if !validIdentifier(record.PeerID) {
		return Record{}, errors.New("INVALID_PEER_ID")
	}
	if !validIdentifier(record.ProjectID) {
		return Record{}, errors.New("INVALID_PROJECT_ID")
	}
	if record.RemoteNodeID != "" && !validIdentifier(record.RemoteNodeID) {
		return Record{}, errors.New("INVALID_REMOTE_NODE_ID")
	}
	if strings.TrimSpace(record.Title) != record.Title || record.Title == "" || utf8.RuneCountInString(record.Title) > 100 || hasControl(record.Title) {
		return Record{}, errors.New("INVALID_TITLE")
	}
	endpoint, err := ValidateEndpoint(record.Endpoint, record.AllowInsecure)
	if err != nil {
		return Record{}, err
	}
	record.Endpoint = endpoint
	if _, err := SecretBytes(record.Secret); err != nil {
		return Record{}, err
	}
	if len(record.ExportNodeIDs) == 0 {
		record.ExportNodeIDs = []string{record.ProjectID}
	}
	if len(record.ExportNodeIDs) > 64 {
		return Record{}, errors.New("INVALID_EXPORT_NODE_IDS")
	}
	seen := make(map[string]bool, len(record.ExportNodeIDs))
	for _, nodeID := range record.ExportNodeIDs {
		if !validIdentifier(nodeID) {
			return Record{}, errors.New("INVALID_EXPORT_NODE_ID")
		}
		if seen[nodeID] {
			return Record{}, errors.New("DUPLICATE_EXPORT_NODE")
		}
		seen[nodeID] = true
	}
	record.ExportNodeIDs = append([]string(nil), record.ExportNodeIDs...)
	return record, nil
}

func (record Record) Public() PublicRecord {
	secret, _ := SecretBytes(record.Secret)
	digest := sha256.Sum256(secret)
	first := ""
	if len(record.ExportNodeIDs) > 0 {
		first = record.ExportNodeIDs[0]
	}
	return PublicRecord{
		PeerID: record.PeerID, Title: record.Title, ProjectID: record.ProjectID,
		Endpoint: record.Endpoint, RemoteNodeID: record.RemoteNodeID,
		ExportNodeID: first, ExportNodeIDs: append([]string(nil), record.ExportNodeIDs...),
		AllowInsecure: record.AllowInsecure, Enabled: record.Enabled,
		KeyFingerprint: hex.EncodeToString(digest[:])[:16],
	}
}

func ValidateEndpoint(value string, allowInsecure bool) (string, error) {
	if value == "" || strings.TrimSpace(value) != value {
		return "", errors.New("INVALID_PEER_ENDPOINT")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Opaque != "" || parsed.User != nil || parsed.Hostname() == "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.ForceQuery || (parsed.EscapedPath() != "" && parsed.EscapedPath() != "/") {
		return "", errors.New("INVALID_PEER_ENDPOINT")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("INVALID_PEER_ENDPOINT")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("INVALID_PEER_ENDPOINT")
	}
	host := parsed.Hostname()
	loopback := strings.EqualFold(host, "localhost")
	if !loopback {
		address := net.ParseIP(host)
		if address == nil {
			return "", errors.New("PEER_IP_LITERAL_REQUIRED")
		}
		loopback = address.IsLoopback()
		if address.IsUnspecified() || address.IsMulticast() || !(address.IsPrivate() || loopback || address.IsLinkLocalUnicast()) {
			return "", errors.New("PEER_ENDPOINT_NOT_PRIVATE")
		}
	}
	if parsed.Scheme == "http" && !loopback && !allowInsecure {
		return "", errors.New("INSECURE_PEER_ENDPOINT")
	}
	return strings.TrimSuffix(value, "/"), nil
}

func ValidateLocal(enabled bool, bindHost string, port int, allowInsecure bool) error {
	if bindHost == "" || strings.TrimSpace(bindHost) != bindHost || port < 1 || port > 65535 {
		return errors.New("INVALID_PEER_SERVICE_CONFIG")
	}
	address := net.ParseIP(bindHost)
	if address == nil || address.IsUnspecified() {
		if bindHost != "0.0.0.0" && bindHost != "::" {
			return errors.New("INVALID_PEER_BIND_HOST")
		}
	} else if !address.IsLoopback() && !address.IsPrivate() && !address.IsLinkLocalUnicast() {
		return errors.New("PEER_BIND_NOT_PRIVATE")
	}
	loopback := bindHost == "127.0.0.1" || bindHost == "::1"
	if enabled && !loopback && !allowInsecure {
		return errors.New("INSECURE_LAN_CONFIRMATION_REQUIRED")
	}
	return nil
}

func NewSecret() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func SecretBytes(value string) ([]byte, error) {
	if len(value) < 40 || len(value) > 64 {
		return nil, errors.New("INVALID_PEER_SECRET")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return nil, errors.New("INVALID_PEER_SECRET")
	}
	return decoded, nil
}

func SignedHeaders(peerID, secret, method, path string, body []byte, now time.Time, nonce string) (http.Header, error) {
	if !validIdentifier(peerID) || validatePath(path) != nil {
		return nil, errors.New("INVALID_PEER_AUTH")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if nonce == "" {
		value := make([]byte, 18)
		if _, err := rand.Read(value); err != nil {
			return nil, err
		}
		nonce = base64.RawURLEncoding.EncodeToString(value)
	}
	if !noncePattern.MatchString(nonce) {
		return nil, errors.New("INVALID_PEER_AUTH")
	}
	signature, err := requestSignature(secret, peerID, method, path, body, now.Unix(), nonce)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set(PeerHeader, peerID)
	headers.Set(TimestampHeader, strconv.FormatInt(now.Unix(), 10))
	headers.Set(NonceHeader, nonce)
	headers.Set(SignatureHeader, signature)
	return headers, nil
}

func VerifyHeaders(headers http.Header, secret, method, path string, body []byte, now time.Time) (Auth, error) {
	peerID, err := oneHeader(headers, PeerHeader)
	if err != nil || !validIdentifier(peerID) {
		return Auth{}, errors.New("PEER_AUTH_REQUIRED")
	}
	nonce, err := oneHeader(headers, NonceHeader)
	if err != nil || !noncePattern.MatchString(nonce) {
		return Auth{}, errors.New("INVALID_PEER_AUTH")
	}
	rawTimestamp, err := oneHeader(headers, TimestampHeader)
	if err != nil {
		return Auth{}, errors.New("INVALID_PEER_AUTH")
	}
	timestamp, err := strconv.ParseInt(rawTimestamp, 10, 64)
	if err != nil {
		return Auth{}, errors.New("INVALID_PEER_AUTH")
	}
	if now.IsZero() {
		now = time.Now()
	}
	if timestamp < now.Unix()-int64(MaxClockSkew/time.Second) || timestamp > now.Unix()+int64(MaxClockSkew/time.Second) {
		return Auth{}, errors.New("PEER_AUTH_EXPIRED")
	}
	supplied, err := oneHeader(headers, SignatureHeader)
	if err != nil || !signaturePattern.MatchString(supplied) {
		return Auth{}, errors.New("PEER_AUTH_INVALID")
	}
	expected, err := requestSignature(secret, peerID, method, path, body, timestamp, nonce)
	if err != nil || !hmac.Equal([]byte(supplied), []byte(expected)) {
		return Auth{}, errors.New("PEER_AUTH_INVALID")
	}
	return Auth{PeerID: peerID, Nonce: nonce, Timestamp: timestamp}, nil
}

func SignedResponseHeaders(peerID, secret string, status int, path, nonce string, body []byte) (http.Header, error) {
	if !validIdentifier(peerID) || status < 100 || status > 599 || validatePath(path) != nil || !noncePattern.MatchString(nonce) {
		return nil, errors.New("INVALID_PEER_RESPONSE_AUTH")
	}
	signature, err := responseSignature(secret, peerID, status, path, nonce, body)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set(ResponsePeerHeader, peerID)
	headers.Set(ResponseSigHeader, signature)
	return headers, nil
}

func VerifyResponseHeaders(headers http.Header, secret, expectedPeerID string, status int, path, nonce string, body []byte) error {
	peerID, err := oneHeader(headers, ResponsePeerHeader)
	if err != nil || !validIdentifier(peerID) || !validIdentifier(expectedPeerID) || peerID != expectedPeerID {
		return errors.New("PEER_RESPONSE_AUTH_INVALID")
	}
	supplied, err := oneHeader(headers, ResponseSigHeader)
	if err != nil || !signaturePattern.MatchString(supplied) {
		return errors.New("PEER_RESPONSE_AUTH_INVALID")
	}
	expected, err := responseSignature(secret, peerID, status, path, nonce, body)
	if err != nil || !hmac.Equal([]byte(supplied), []byte(expected)) {
		return errors.New("PEER_RESPONSE_AUTH_INVALID")
	}
	return nil
}

func requestSignature(secret, peerID, method, path string, body []byte, timestamp int64, nonce string) (string, error) {
	key, err := SecretBytes(secret)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	message := strings.Join([]string{"ytqjk-peer-v1", peerID, strings.ToUpper(method), path, strconv.FormatInt(timestamp, 10), nonce, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func responseSignature(secret, peerID string, status int, path, nonce string, body []byte) (string, error) {
	key, err := SecretBytes(secret)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(body)
	message := strings.Join([]string{"ytqjk-peer-response-v1", peerID, strconv.Itoa(status), path, nonce, hex.EncodeToString(digest[:])}, "\n")
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func validIdentifier(value string) bool { return identifierPattern.MatchString(value) }

func validatePath(path string) error {
	if path == "" || !strings.HasPrefix(path, "/") || len(path) > 4096 || hasControl(path) {
		return errors.New("INVALID_PEER_PATH")
	}
	return nil
}

func hasControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func oneHeader(headers http.Header, name string) (string, error) {
	values := headers.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", errors.New("invalid header")
	}
	return values[0], nil
}

// Package library defines the language-neutral Library contract.
package library

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	mebibyte         int64 = 1024 * 1024
	MinCapacityBytes       = 64 * mebibyte
	MaxCapacityBytes       = 1024 * 1024 * mebibyte
)

var (
	identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	schemePattern     = regexp.MustCompile(`(?i)(^|[^a-z0-9+.-])[a-z][a-z0-9+.-]*:\S`)
	absolutePath      = regexp.MustCompile(`(^|\s)/\S`)
	traversalPattern  = regexp.MustCompile(`(^|[\\/])\.\.([\\/]|$)`)
	awsKeyPattern     = regexp.MustCompile(`AKIA[A-Z0-9]{16}`)
	githubToken       = regexp.MustCompile(`gh[pousr]_\w{30,}`)
	credentialPattern = regexp.MustCompile(
		`(?i)((api.?key|access.?token|client.?secret)|password|credential)\s*[:=]\s*\S{8,}`,
	)
	basicAuthPattern = regexp.MustCompile(`[^\s:]+:[^\s@]+@[^\s]+`)
)

// Type identifies the behavior adapter used by a Library node.
type Type string

const (
	TypeGlobal  Type = "global"
	TypeGroup   Type = "group"
	TypeMounted Type = "mounted"
	TypeProject Type = "project"
)

// Error is a stable contract validation error.
type Error struct {
	Code        string
	cause       error
	serverFault bool
}

func (e *Error) Error() string {
	return e.Code
}

// Unwrap retains storage diagnostics without exposing them through the contract.
func (e *Error) Unwrap() error {
	return e.cause
}

// IsServerFault distinguishes corrupted storage from rejected client input.
func (e *Error) IsServerFault() bool {
	return e.serverFault
}

// Statistics contains mutable usage data, separate from Library configuration.
type Statistics struct {
	UsedBytes        int64 `json:"used_bytes"`
	IndexedDocuments int64 `json:"indexed_documents"`
	TotalDocuments   int64 `json:"total_documents"`
	IndexedChunks    int64 `json:"indexed_chunks"`
	TotalChunks      int64 `json:"total_chunks"`
}

// Node is the public representation of one mounted or local Library.
type Node struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Type          Type              `json:"type"`
	ParentID      *string           `json:"parent_id"`
	CapacityBytes int64             `json:"capacity_bytes"`
	Metadata      map[string]string `json:"metadata"`
	Stats         Statistics        `json:"stats"`
}

// CreateRequest is the exact v1 request for creating a Library node.
type CreateRequest struct {
	NodeID        string
	Title         string
	Type          Type
	ParentID      *string
	CapacityBytes int64
	Metadata      map[string]string
}

// DecodeCreateRequest decodes and validates one exact v1 request object.
func DecodeCreateRequest(data []byte) (CreateRequest, error) {
	fields, err := decodeObject(data)
	if err != nil {
		return CreateRequest{}, err
	}
	required := map[string]struct{}{
		"node_id": {}, "title": {}, "type": {}, "parent_id": {},
		"capacity_bytes": {}, "metadata": {},
	}
	if !hasExactFields(fields, required) {
		return CreateRequest{}, contractError("INVALID_REQUEST_FIELDS")
	}

	request := CreateRequest{}
	if err := decodeField(fields, "node_id", &request.NodeID); err != nil {
		return CreateRequest{}, contractError("INVALID_NODE_ID")
	}
	if err := decodeField(fields, "title", &request.Title); err != nil {
		return CreateRequest{}, contractError("INVALID_TITLE")
	}
	if err := decodeField(fields, "type", &request.Type); err != nil {
		return CreateRequest{}, contractError("CREATION_TYPE_FORBIDDEN")
	}
	if err := decodeField(fields, "parent_id", &request.ParentID); err != nil {
		return CreateRequest{}, contractError("INVALID_PARENT_ID")
	}
	if err := decodeField(fields, "capacity_bytes", &request.CapacityBytes); err != nil {
		return CreateRequest{}, contractError("INVALID_CAPACITY_BYTES")
	}
	if err := decodeField(fields, "metadata", &request.Metadata); err != nil || request.Metadata == nil {
		return CreateRequest{}, contractError("INVALID_NODE_METADATA")
	}
	if err := request.Validate(); err != nil {
		return CreateRequest{}, err
	}
	return request, nil
}

// Validate enforces behavior-compatible Library creation constraints.
func (r CreateRequest) Validate() error {
	if !identifierPattern.MatchString(r.NodeID) {
		return contractError("INVALID_NODE_ID")
	}
	if err := validateText(r.Title, 200); err != nil {
		return contractError("INVALID_TITLE")
	}
	if r.ParentID != nil && !identifierPattern.MatchString(*r.ParentID) {
		return contractError("INVALID_PARENT_ID")
	}
	if r.CapacityBytes < MinCapacityBytes || r.CapacityBytes > MaxCapacityBytes {
		return contractError("INVALID_CAPACITY_BYTES")
	}
	switch r.Type {
	case TypeGroup:
		if len(r.Metadata) != 0 {
			return contractError("GROUP_METADATA_FORBIDDEN")
		}
	case TypeMounted:
		if err := validateMountMetadata(r); err != nil {
			return err
		}
	default:
		return contractError("CREATION_TYPE_FORBIDDEN")
	}
	return nil
}

// NewNode converts a validated request into the public response model.
func NewNode(request CreateRequest) (Node, error) {
	if err := request.Validate(); err != nil {
		return Node{}, err
	}
	metadata := make(map[string]string, len(request.Metadata))
	for key, value := range request.Metadata {
		metadata[key] = value
	}
	return Node{
		ID:            request.NodeID,
		Title:         request.Title,
		Type:          request.Type,
		ParentID:      request.ParentID,
		CapacityBytes: request.CapacityBytes,
		Metadata:      metadata,
		Stats:         Statistics{},
	}, nil
}

func validateMountMetadata(request CreateRequest) error {
	if len(request.Metadata) != 2 {
		return contractError("INVALID_MOUNT_METADATA")
	}
	mountID, hasMountID := request.Metadata["mount_id"]
	capability, hasCapability := request.Metadata["capability"]
	if !hasMountID || !hasCapability || !identifierPattern.MatchString(mountID) {
		return contractError("INVALID_MOUNT_METADATA")
	}
	if err := validateText(capability, 64); err != nil || !identifierPattern.MatchString(capability) {
		return contractError("INVALID_MOUNT_METADATA")
	}
	for _, value := range []string{request.Title, mountID, capability} {
		if unsafeMountMetadata(value) {
			return contractError("UNSAFE_MOUNT_METADATA")
		}
	}
	return nil
}

func validateText(value string, limit int) error {
	if value == "" || value != strings.TrimSpace(value) || utf8.RuneCountInString(value) > limit {
		return fmt.Errorf("invalid text")
	}
	for _, character := range value {
		if character < 32 || character == 127 {
			return fmt.Errorf("invalid text")
		}
	}
	return nil
}

func unsafeMountMetadata(value string) bool {
	return strings.Contains(value, `\\`) ||
		schemePattern.MatchString(value) ||
		absolutePath.MatchString(value) ||
		traversalPattern.MatchString(value) ||
		awsKeyPattern.MatchString(value) ||
		githubToken.MatchString(value) ||
		credentialPattern.MatchString(value) ||
		basicAuthPattern.MatchString(value)
}

func decodeObject(data []byte) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, contractError("JSON_NOT_UTF8")
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return nil, contractError("UTF8_BOM_FORBIDDEN")
	}
	if err := validateJSONStructure(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, contractError("INVALID_JSON")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, contractError("INVALID_JSON")
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, contractError("INVALID_JSON")
		}
		if _, exists := fields[key]; exists {
			return nil, contractError("DUPLICATE_JSON_KEY")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return nil, contractError("INVALID_JSON")
		}
		fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, contractError("INVALID_JSON")
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, contractError("INVALID_JSON")
	}
	return fields, nil
}

func hasExactFields(fields map[string]json.RawMessage, required map[string]struct{}) bool {
	if len(fields) != len(required) {
		return false
	}
	for name := range required {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

func decodeField(fields map[string]json.RawMessage, name string, destination any) error {
	if err := json.Unmarshal(fields[name], destination); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

func contractError(code string) error {
	return &Error{Code: code}
}

func storeError(code string, cause error) error {
	return &Error{Code: code, cause: cause, serverFault: true}
}

package library

import (
	"encoding/json"
	"errors"
	"testing"
)

const oneGiB int64 = 1024 * 1024 * 1024

func TestDecodeCreateRequestGroup(t *testing.T) {
	payload := []byte(`{
		"node_id":"project.alpha",
		"title":"Project Alpha",
		"type":"group",
		"parent_id":null,
		"capacity_bytes":1073741824,
		"metadata":{}
	}`)

	request, err := DecodeCreateRequest(payload)
	if err != nil {
		t.Fatalf("DecodeCreateRequest() error = %v", err)
	}
	if request.NodeID != "project.alpha" || request.Title != "Project Alpha" {
		t.Fatalf("unexpected identity: %#v", request)
	}
	if request.Type != TypeGroup || request.ParentID != nil {
		t.Fatalf("unexpected topology: %#v", request)
	}
	if request.CapacityBytes != oneGiB || len(request.Metadata) != 0 {
		t.Fatalf("unexpected capacity or metadata: %#v", request)
	}
}

func TestDecodeCreateRequestMounted(t *testing.T) {
	payload := []byte(`{
		"node_id":"mounted-1",
		"title":"External library",
		"type":"mounted",
		"parent_id":"global",
		"capacity_bytes":67108864,
		"metadata":{"mount_id":"connector-17","capability":"READ_ONLY"}
	}`)

	request, err := DecodeCreateRequest(payload)
	if err != nil {
		t.Fatalf("DecodeCreateRequest() error = %v", err)
	}
	if request.ParentID == nil || *request.ParentID != "global" {
		t.Fatalf("unexpected parent: %#v", request.ParentID)
	}
	if request.Metadata["mount_id"] != "connector-17" {
		t.Fatalf("unexpected metadata: %#v", request.Metadata)
	}
}

func TestDecodeCreateRequestRejectsInvalidPayloads(t *testing.T) {
	base := map[string]any{
		"node_id": "group-1", "title": "Group 1", "type": "group",
		"parent_id": nil, "capacity_bytes": oneGiB,
		"metadata": map[string]string{},
	}
	tests := []struct {
		name    string
		mutate  func(map[string]any)
		payload []byte
		code    string
	}{
		{
			name: "missing field",
			mutate: func(value map[string]any) {
				delete(value, "capacity_bytes")
			},
			code: "INVALID_REQUEST_FIELDS",
		},
		{
			name: "extra field",
			mutate: func(value map[string]any) {
				value["extra"] = true
			},
			code: "INVALID_REQUEST_FIELDS",
		},
		{
			name: "forbidden type",
			mutate: func(value map[string]any) {
				value["type"] = "project"
			},
			code: "CREATION_TYPE_FORBIDDEN",
		},
		{
			name: "invalid parent",
			mutate: func(value map[string]any) {
				value["parent_id"] = " padded "
			},
			code: "INVALID_PARENT_ID",
		},
		{
			name: "group metadata",
			mutate: func(value map[string]any) {
				value["metadata"] = map[string]string{"unexpected": "value"}
			},
			code: "GROUP_METADATA_FORBIDDEN",
		},
		{
			name:    "duplicate key",
			payload: []byte(`{"node_id":"a","node_id":"b"}`),
			code:    "DUPLICATE_JSON_KEY",
		},
		{
			name: "nested duplicate mount metadata",
			payload: []byte(`{
				"node_id":"mounted-1","title":"Mounted","type":"mounted",
				"parent_id":"global","capacity_bytes":1073741824,
				"metadata":{"mount_id":"first","mount_id":"second","capability":"READ_ONLY"}
			}`),
			code: "DUPLICATE_JSON_KEY",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := test.payload
			if payload == nil {
				value := cloneMap(base)
				test.mutate(value)
				var err error
				payload, err = json.Marshal(value)
				if err != nil {
					t.Fatalf("json.Marshal() error = %v", err)
				}
			}
			_, err := DecodeCreateRequest(payload)
			assertCode(t, err, test.code)
		})
	}
}

func TestCapacityRangeIsNotTiedToUIPresets(t *testing.T) {
	tests := []struct {
		name     string
		capacity int64
		valid    bool
	}{
		{name: "below minimum", capacity: MinCapacityBytes - 1},
		{name: "minimum", capacity: MinCapacityBytes, valid: true},
		{name: "non preset within range", capacity: MinCapacityBytes + 1, valid: true},
		{name: "maximum", capacity: MaxCapacityBytes, valid: true},
		{name: "above maximum", capacity: MaxCapacityBytes + 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validGroupRequest()
			request.CapacityBytes = test.capacity
			err := request.Validate()
			if test.valid && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !test.valid {
				assertCode(t, err, "INVALID_CAPACITY_BYTES")
			}
		})
	}
}

func TestMountedMetadataPreservesPythonSafetyBoundary(t *testing.T) {
	tests := []struct {
		name     string
		title    string
		metadata map[string]string
		code     string
	}{
		{
			name: "missing capability", title: "External",
			metadata: map[string]string{"mount_id": "mount-1"},
			code:     "INVALID_MOUNT_METADATA",
		},
		{
			name: "url in title", title: "https://private.example",
			metadata: map[string]string{
				"mount_id": "mount-1", "capability": "READ_ONLY",
			},
			code: "UNSAFE_MOUNT_METADATA",
		},
		{
			name: "credential in title", title: "password=not-a-secret-value",
			metadata: map[string]string{
				"mount_id": "mount-1", "capability": "READ_ONLY",
			},
			code: "UNSAFE_MOUNT_METADATA",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := validGroupRequest()
			request.Type = TypeMounted
			request.Title = test.title
			request.Metadata = test.metadata
			assertCode(t, request.Validate(), test.code)
		})
	}
}

func TestNodeJSONSeparatesConfigurationAndStatistics(t *testing.T) {
	request := validGroupRequest()
	node, err := NewNode(request)
	if err != nil {
		t.Fatalf("NewNode() error = %v", err)
	}
	node.Stats = Statistics{
		UsedBytes: 7, IndexedDocuments: 2, TotalDocuments: 3,
		IndexedChunks: 5, TotalChunks: 8,
	}
	payload, err := json.Marshal(node)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if decoded["capacity_bytes"] != float64(oneGiB) {
		t.Fatalf("capacity_bytes = %#v", decoded["capacity_bytes"])
	}
	stats, ok := decoded["stats"].(map[string]any)
	if !ok || stats["used_bytes"] != float64(7) {
		t.Fatalf("stats = %#v", decoded["stats"])
	}
	if _, exists := stats["capacity_bytes"]; exists {
		t.Fatal("capacity_bytes must remain node configuration")
	}
}

func validGroupRequest() CreateRequest {
	return CreateRequest{
		NodeID: "group-1", Title: "Group 1", Type: TypeGroup,
		CapacityBytes: oneGiB, Metadata: map[string]string{},
	}
}

func cloneMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func assertCode(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s", expected)
	}
	var contractErr *Error
	if !errors.As(err, &contractErr) {
		t.Fatalf("error type = %T, want *Error", err)
	}
	if contractErr.Code != expected {
		t.Fatalf("error code = %s, want %s", contractErr.Code, expected)
	}
}

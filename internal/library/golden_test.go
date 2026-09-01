package library

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type createGolden struct {
	Contract string          `json:"contract"`
	Request  json.RawMessage `json:"request"`
}

type treeGolden struct {
	Contract string `json:"contract"`
	Response struct {
		Revision int64    `json:"revision"`
		Nodes    []Node   `json:"nodes"`
		Edges    []Edge   `json:"edges"`
		Roots    []string `json:"roots"`
		Digest   string   `json:"digest"`
	} `json:"response"`
	ConfigurationSummary digestBody `json:"configuration_summary"`
}

type errorGolden struct {
	Contract string `json:"contract"`
	Cases    []struct {
		Name           string `json:"name"`
		ExpectedStatus int    `json:"expected_status"`
		ExpectedCode   string `json:"expected_code"`
	} `json:"cases"`
}

func TestCreateGoldenRequestsDecode(t *testing.T) {
	for _, name := range []string{"create-group.json", "create-mounted.json"} {
		t.Run(name, func(t *testing.T) {
			var fixture createGolden
			readGolden(t, name, &fixture)
			if fixture.Contract != "library.v1.create" {
				t.Fatalf("contract = %q", fixture.Contract)
			}
			request, err := DecodeCreateRequest(fixture.Request)
			if err != nil {
				t.Fatalf("DecodeCreateRequest() error = %v", err)
			}
			if request.CapacityBytes < MinCapacityBytes || request.CapacityBytes > MaxCapacityBytes {
				t.Fatalf("capacity_bytes = %d", request.CapacityBytes)
			}
		})
	}
}

func TestTreeGoldenMatchesSnapshotAndConfigurationSummary(t *testing.T) {
	var fixture treeGolden
	readGolden(t, "tree-snapshot.json", &fixture)
	if fixture.Contract != "library.v1.tree-snapshot" {
		t.Fatalf("contract = %q", fixture.Contract)
	}
	tree, err := NewTree(fixture.Response.Nodes, fixture.Response.Revision)
	if err != nil {
		t.Fatalf("NewTree() error = %v", err)
	}
	snapshot, err := tree.Snapshot()
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if !reflect.DeepEqual(snapshot.Nodes, fixture.Response.Nodes) ||
		!reflect.DeepEqual(snapshot.Edges, fixture.Response.Edges) ||
		!reflect.DeepEqual(snapshot.Roots, fixture.Response.Roots) ||
		snapshot.Digest != fixture.Response.Digest {
		t.Fatalf("snapshot differs from Golden: %#v", snapshot)
	}
	summary := digestBody{
		Edges: snapshot.Edges, Nodes: digestNodes(snapshot.Nodes),
		Revision: snapshot.Revision, Roots: snapshot.Roots,
	}
	if !reflect.DeepEqual(summary, fixture.ConfigurationSummary) {
		t.Fatalf("configuration summary differs from Golden: %#v", summary)
	}
}

func TestErrorGoldenCasesMatchDecoder(t *testing.T) {
	var fixture errorGolden
	readGolden(t, "error-codes.json", &fixture)
	if fixture.Contract != "library.v1.errors" {
		t.Fatalf("contract = %q", fixture.Contract)
	}
	for _, test := range fixture.Cases {
		t.Run(test.Name, func(t *testing.T) {
			if test.ExpectedStatus != 400 {
				t.Fatalf("expected_status = %d", test.ExpectedStatus)
			}
			payload := invalidGoldenPayload(t, test.Name)
			_, err := DecodeCreateRequest(payload)
			assertCode(t, err, test.ExpectedCode)
		})
	}
}

func invalidGoldenPayload(t *testing.T, name string) []byte {
	t.Helper()
	request := map[string]any{
		"node_id": "group-1", "title": "Group 1", "type": "group",
		"parent_id": nil, "capacity_bytes": oneGiB,
		"metadata": map[string]string{},
	}
	switch name {
	case "missing_capacity_bytes":
		delete(request, "capacity_bytes")
	case "extra_request_field":
		request["unexpected"] = true
	case "capacity_not_integer":
		request["capacity_bytes"] = 1.5
	case "capacity_out_of_range":
		request["capacity_bytes"] = MinCapacityBytes - 1
	case "group_metadata_not_empty":
		request["metadata"] = map[string]string{"unexpected": "value"}
	case "mounted_metadata_shape_changed":
		request["type"] = "mounted"
		request["metadata"] = map[string]string{"mount_id": "mount-1"}
	default:
		t.Fatalf("unsupported Golden error case %q", name)
	}
	payload, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	return payload
}

func readGolden(t *testing.T, name string, destination any) {
	t.Helper()
	path := filepath.Join("..", "..", "contracts", "library", "v1", name)
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) error = %v", path, err)
	}
	if err := json.Unmarshal(payload, destination); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
	}
}

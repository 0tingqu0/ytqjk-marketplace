package peer

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestEndpointAndRecordValidation(t *testing.T) {
	secret, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	record, err := ValidateRecord(Record{
		PeerID: "peer-remote", Title: "Remote", ProjectID: "project-a",
		Endpoint: "http://127.0.0.1:8766/", Secret: secret, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.Endpoint != "http://127.0.0.1:8766" || len(record.ExportNodeIDs) != 1 || record.Public().KeyFingerprint == "" {
		t.Fatalf("record = %#v", record)
	}
	for _, endpoint := range []string{"https://example.com:8766", "http://192.168.1.8:8766", "http://127.0.0.1", "file:///tmp/peer"} {
		if _, err := ValidateEndpoint(endpoint, false); err == nil {
			t.Fatalf("unsafe endpoint accepted: %s", endpoint)
		}
	}
	if _, err := ValidateEndpoint("http://192.168.1.8:8766", true); err != nil {
		t.Fatal(err)
	}
}

func TestRequestAndResponseSignaturesBindEveryPart(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Unix(1_800_000_000, 0)
	body := []byte(`{"project_id":"project-a"}`)
	headers, err := SignedHeaders("peer-client", secret, http.MethodPost, "/v1/query", body, now, strings.Repeat("N", 22))
	if err != nil {
		t.Fatal(err)
	}
	auth, err := VerifyHeaders(headers, secret, http.MethodPost, "/v1/query", body, now)
	if err != nil || auth.PeerID != "peer-client" {
		t.Fatalf("auth = %#v, %v", auth, err)
	}
	if _, err := VerifyHeaders(headers, secret, http.MethodPost, "/v1/query", append(body, ' '), now); err == nil {
		t.Fatal("tampered body accepted")
	}
	response := []byte(`{"ok":true}`)
	responseHeaders, err := SignedResponseHeaders("peer-server", secret, 200, "/v1/query", auth.Nonce, response)
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponseHeaders(responseHeaders, secret, "peer-server", 200, "/v1/query", auth.Nonce, response); err != nil {
		t.Fatal(err)
	}
	if err := VerifyResponseHeaders(responseHeaders, secret, "peer-server", 201, "/v1/query", auth.Nonce, response); err == nil {
		t.Fatal("tampered status accepted")
	}
}

func TestDuplicateAuthHeaderFailsClosed(t *testing.T) {
	secret, _ := NewSecret()
	now := time.Unix(1_800_000_000, 0)
	headers, _ := SignedHeaders("peer-client", secret, http.MethodPost, "/v1/health", []byte("{}"), now, strings.Repeat("D", 22))
	headers.Add(PeerHeader, "peer-attacker")
	if _, err := VerifyHeaders(headers, secret, http.MethodPost, "/v1/health", []byte("{}"), now); err == nil {
		t.Fatal("duplicate identity header accepted")
	}
}

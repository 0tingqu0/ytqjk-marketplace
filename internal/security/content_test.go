package security

import "testing"

func TestSecretDetectionRejectsCredentialsAndAllowsPlaceholders(t *testing.T) {
	for _, value := range []string{
		"authorization: bearer real-token",
		"api_key=abcdefghijklmnop",
		"https://user:password@example.test/path",
		"-----BEGIN PRIVATE KEY-----",
	} {
		if !ContainsHighConfidenceSecret(value) {
			t.Fatalf("secret was accepted: %q", value)
		}
	}
	for _, value := range []string{"api_key=${API_KEY}", "token=example-placeholder", "authorization is described here"} {
		if ContainsHighConfidenceSecret(value) {
			t.Fatalf("placeholder was rejected: %q", value)
		}
	}
}

func TestSensitivePathDetection(t *testing.T) {
	for _, value := range []string{".ssh/id_ed25519", "config/secrets.yaml", "node_modules/pkg/index.js", "state.tfstate.backup"} {
		if !IsSensitivePath(value) {
			t.Fatalf("sensitive path was accepted: %q", value)
		}
	}
	if IsSensitivePath("src/token_parser.go") {
		t.Fatal("ordinary source path was rejected")
	}
}

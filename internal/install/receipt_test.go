package install

import "testing"

func TestVectorReceiptDescribesBuiltInGoBackend(t *testing.T) {
	if value := VectorResult("auto", 0, 0, false); value != "go-hybrid" {
		t.Fatalf("auto vector result = %q", value)
	}
	if value := VectorResult("off", 0, 0, false); value != "off" {
		t.Fatalf("off vector result = %q", value)
	}
	if value := VectorResult("on", 0, 0, true); value != "lexical-only" {
		t.Fatalf("failed vector result = %q", value)
	}
	if value := Health(false)["vector"]; value != "BUILTIN_GO" {
		t.Fatalf("vector health = %q", value)
	}
}

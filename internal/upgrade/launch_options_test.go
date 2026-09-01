package upgrade

import (
	"os"
	"strings"
	"testing"
)

func TestMergeHelperEnvironmentReplacesInheritedValue(t *testing.T) {
	const key = "YTQJK_TEST_HELPER_ENVIRONMENT"
	t.Setenv(key, "inherited")
	merged, err := mergeHelperEnvironment([]string{strings.ToLower(key) + "=replacement"})
	if err != nil {
		t.Fatal(err)
	}
	var matches []string
	for _, entry := range merged {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, key) {
			matches = append(matches, entry)
		}
	}
	if len(matches) != 1 || matches[0] != strings.ToLower(key)+"=replacement" {
		t.Fatalf("merged environment matches=%v", matches)
	}
	if os.Getenv(key) != "inherited" {
		t.Fatal("process environment was mutated")
	}
}

func TestMergeHelperEnvironmentRejectsDuplicateAndControlBytes(t *testing.T) {
	for _, overrides := range [][]string{
		{"YTQJK_DUPLICATE=one", "ytqjk_duplicate=two"},
		{"YTQJK_INVALID"},
		{"YTQJK_INVALID=line\nbreak"},
	} {
		if _, err := mergeHelperEnvironment(overrides); err == nil {
			t.Fatalf("overrides=%q were accepted", overrides)
		}
	}
}

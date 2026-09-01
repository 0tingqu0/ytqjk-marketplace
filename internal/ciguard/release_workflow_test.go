package ciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseWorkflowResolvesDraftByID(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, ".github", "workflows", "release.yml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read release workflow: %v", err)
	}
	workflow := string(content)
	forbidden := `releases/tags/${tag}`
	if strings.Contains(workflow, forbidden) {
		t.Fatalf("draft release lookup must not use %q", forbidden)
	}
	required := []string{
		`releases?per_page=100`,
		`select(.tag_name == $tag)`,
		`release_id="$(jq -er '.id | tostring' "${release_json}")"`,
		`release_api="repos/${GITHUB_REPOSITORY}/releases/${release_id}"`,
		`[[ "$(jq -r '.target_commitish' "${release_json}")" != "${RELEASE_COMMIT}" ]]`,
	}
	for _, fragment := range required {
		if !strings.Contains(workflow, fragment) {
			t.Errorf("release workflow is missing %q", fragment)
		}
	}
}

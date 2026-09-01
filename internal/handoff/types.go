package handoff

const Format = "ytqjk-handoff-v1"

type TrackedPayload struct {
	Paths  []string `json:"paths"`
	Patch  string   `json:"patch"`
	Bytes  int64    `json:"bytes"`
	SHA256 string   `json:"sha256"`
}

type FilePayload struct {
	Path   string `json:"path"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Format       string         `json:"format"`
	BaseHead     string         `json:"base_head"`
	Allowlist    []string       `json:"allowlist"`
	Tracked      TrackedPayload `json:"tracked"`
	Untracked    []FilePayload  `json:"untracked"`
	BundleSHA256 string         `json:"bundle_sha256"`
}

type ExportResult struct {
	Bundle       string   `json:"bundle"`
	BundleSHA256 string   `json:"bundle_sha256"`
	BaseHead     string   `json:"base_head"`
	Paths        []string `json:"paths"`
}

type ApplyResult struct {
	BundleSHA256       string   `json:"bundle_sha256"`
	BaseHead           string   `json:"base_head"`
	IntegrationHead    string   `json:"integration_head"`
	StagedPaths        []string `json:"staged_paths"`
	StagedSnapshotHash string   `json:"staged_snapshot_sha256"`
}

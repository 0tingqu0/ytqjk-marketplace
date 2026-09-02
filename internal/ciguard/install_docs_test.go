package ciguard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallerDefaultsSeparateDistributionTargetAndProject(t *testing.T) {
	root := repositoryRoot(t)
	cases := []struct {
		path      string
		required  []string
		forbidden []string
	}{
		{
			path: "install.ps1",
			required: []string{
				"function Resolve-DefaultCodexRoot",
				"function Resolve-DefaultProjectRoot",
				"'--target-root', $defaultTargetRoot",
				"'--project-root', $defaultProjectRoot",
				"'--source-root', $PSScriptRoot",
			},
			forbidden: []string{
				"'--target-root', $PSScriptRoot",
				"'--project-root', $PSScriptRoot",
			},
		},
		{
			path: "install.sh",
			required: []string{
				"codex_root=",
				"project_root=$(pwd -P)",
				"--target-root \"$codex_root\"",
				"--project-root \"$project_root\"",
				"--source-root \"$script_dir\"",
			},
			forbidden: []string{
				"--target-root \"$script_dir\"",
				"--project-root \"$script_dir\"",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			content := readContractFile(t, root, testCase.path)
			requireMarkers(t, testCase.path, content, testCase.required)
			for _, marker := range testCase.forbidden {
				if strings.Contains(content, marker) {
					t.Errorf("%s still contains unsafe default %q", testCase.path, marker)
				}
			}
		})
	}
}

func TestInstallDocumentationCoversOperationalContract(t *testing.T) {
	root := repositoryRoot(t)
	cases := []struct {
		path    string
		markers []string
	}{
		{
			path: "README.md",
			markers: []string{
				"supported stable deployment is [v0.7.0]",
				"release bundle needs no Go toolchain",
				"installer does not mutate the user's",
				"docs/installation.md",
			},
		},
		{
			path: "README.zh-CN.md",
			markers: []string{
				"受支持的正式部署版本是",
				"正式发布包不需要 Go",
				"安装器不会静默修改用户",
				"docs/installation.zh-CN.md",
			},
		},
		{
			path: "docs/installation.md",
			markers: []string{
				"ytqjk-windows-amd64.zip",
				"SHA256SUMS",
				"--project-root",
				"Acceptance checks",
				"Upgrade and rollback",
				"verified external bundle",
				"Troubleshooting",
			},
		},
		{
			path: "docs/installation.zh-CN.md",
			markers: []string{
				"ytqjk-windows-amd64.zip",
				"SHA256SUMS",
				"--project-root",
				"安装验收",
				"升级与回滚",
				"外部发布包",
				"常见故障",
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.path, func(t *testing.T) {
			content := readContractFile(t, root, testCase.path)
			requireMarkers(t, testCase.path, content, testCase.markers)
		})
	}
}

func readContractFile(t *testing.T, root, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func requireMarkers(t *testing.T, path, content string, markers []string) {
	t.Helper()
	for _, marker := range markers {
		if !strings.Contains(content, marker) {
			t.Errorf("%s is missing contract marker %q", path, marker)
		}
	}
}

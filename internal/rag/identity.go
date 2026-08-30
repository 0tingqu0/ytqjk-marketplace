package rag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const SchemaVersion = 6

var safeNamePattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

type ProjectIdentity struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Root   string `json:"root"`
	Remote string `json:"remote"`
}

type CatalogProject struct {
	Name          string   `json:"name"`
	Remote        string   `json:"remote"`
	PathAliases   []string `json:"path_aliases"`
	LastAccessed  string   `json:"last_accessed"`
	TrackingState string   `json:"tracking_state"`
}

type Catalog struct {
	SchemaVersion int                       `json:"schema_version"`
	Projects      map[string]CatalogProject `json:"projects"`
}

func IdentifyProject(project string) (ProjectIdentity, error) {
	absolute, err := filepath.Abs(project)
	if err != nil {
		return ProjectIdentity{}, err
	}
	info, err := os.Stat(absolute)
	if err != nil || !info.IsDir() {
		return ProjectIdentity{}, errors.New("项目工作目录不存在或不是目录")
	}
	root := absolute
	remote := ""
	canonical := absolute
	if output, ok := git(absolute, "rev-parse", "--is-inside-work-tree"); ok && strings.TrimSpace(output) == "true" {
		if output, ok := git(absolute, "rev-parse", "--show-toplevel"); ok && strings.TrimSpace(output) != "" {
			root, _ = filepath.Abs(strings.TrimSpace(output))
		}
		if output, ok := git(root, "remote", "get-url", "origin"); ok {
			remote = normalizeRemote(strings.TrimSpace(output))
		}
		if output, ok := git(root, "rev-parse", "--git-common-dir"); ok {
			common := strings.TrimSpace(output)
			if !filepath.IsAbs(common) {
				common = filepath.Join(root, common)
			}
			common, _ = filepath.Abs(common)
			if strings.EqualFold(filepath.Base(common), ".git") {
				canonical = filepath.Dir(common)
			} else {
				canonical = common
			}
		}
	}
	name := strings.Trim(safeNamePattern.ReplaceAllString(filepath.Base(canonical), "-"), "-_")
	if name == "" {
		name = "project"
	}
	identityValue := remote
	if identityValue == "" {
		identityValue = filepath.Clean(canonical)
		if runtime.GOOS == "windows" {
			identityValue = strings.ToLower(identityValue)
		}
	}
	digest := sha256.Sum256([]byte(identityValue))
	identifier := name + "--" + hex.EncodeToString(digest[:])[:12]
	if strings.EqualFold(name, "p2604_soc") {
		identifier = "p2604_soc"
	}
	return ProjectIdentity{ID: identifier, Name: name, Root: root, Remote: remote}, nil
}

func TrackProject(knowledgeRoot, projectRoot string) (ProjectIdentity, error) {
	identity, err := IdentifyProject(projectRoot)
	if err != nil {
		return ProjectIdentity{}, err
	}
	projectDirectory := filepath.Join(knowledgeRoot, "projects", identity.ID)
	for _, relative := range []string{"", "cache", "handoffs", "errors", "vectors"} {
		path := filepath.Join(projectDirectory, relative)
		if err := rejectLink(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ProjectIdentity{}, err
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return ProjectIdentity{}, err
		}
	}
	catalogPath := filepath.Join(knowledgeRoot, "catalog.json")
	catalog := Catalog{SchemaVersion: SchemaVersion, Projects: map[string]CatalogProject{}}
	if err := safeio.ReadJSON(catalogPath, &catalog); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ProjectIdentity{}, errors.New("knowledge catalog is invalid")
	}
	if catalog.Projects == nil {
		catalog.Projects = map[string]CatalogProject{}
	}
	existing := catalog.Projects[identity.ID]
	aliases := append(existing.PathAliases, identity.Root)
	aliases = uniqueSorted(aliases)
	catalog.SchemaVersion = SchemaVersion
	catalog.Projects[identity.ID] = CatalogProject{
		Name: identity.Name, Remote: identity.Remote, PathAliases: aliases,
		LastAccessed: time.Now().UTC().Format(time.RFC3339Nano), TrackingState: "REGISTERED",
	}
	if err := safeio.WriteJSON(catalogPath, catalog); err != nil {
		return ProjectIdentity{}, err
	}
	return identity, nil
}

func QueryState(projectRoot string) map[string]string {
	state := map[string]string{"head": "NON_GIT", "dirty": "unknown"}
	inside, ok := git(projectRoot, "rev-parse", "--is-inside-work-tree")
	if !ok || strings.TrimSpace(inside) != "true" {
		return state
	}
	head, ok := git(projectRoot, "rev-parse", "--verify", "HEAD")
	if !ok || strings.TrimSpace(head) == "" {
		head = "UNBORN"
	}
	status, _ := git(projectRoot, "status", "--short", "--untracked-files=no")
	materialized, _ := gitBytes(projectRoot, "ls-files", "-t", "-z")
	digest := sha256.Sum256(materialized)
	state["head"] = strings.TrimSpace(head)
	state["dirty"] = map[bool]string{true: "true", false: "false"}[strings.TrimSpace(status) != ""]
	state["materialization"] = hex.EncodeToString(digest[:])
	return state
}

func git(directory string, arguments ...string) (string, bool) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &bytes.Buffer{}
	err := command.Run()
	return output.String(), err == nil
}

func gitBytes(directory string, arguments ...string) ([]byte, bool) {
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.Output()
	return output, err == nil
}

func normalizeRemote(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "git@") && strings.Contains(value, ":") {
		parts := strings.SplitN(strings.TrimPrefix(value, "git@"), ":", 2)
		value = "https://" + strings.ToLower(parts[0]) + "/" + parts[1]
	}
	parsed, err := url.Parse(value)
	if err == nil && parsed.Host != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.Fragment = ""
		parsed.Host = strings.ToLower(parsed.Host)
		parsed.Path = strings.TrimSuffix(parsed.Path, ".git")
		return strings.TrimSuffix(parsed.String(), "/")
	}
	return strings.TrimSuffix(strings.TrimSuffix(value, ".git"), "/")
}

func rejectLink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("UNSAFE_PROJECT_DIRECTORY")
	}
	return nil
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sortStrings(result)
	return result
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

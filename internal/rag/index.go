package rag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	maxFileBytes  = 2 * 1024 * 1024
	maxTotalBytes = 128 * 1024 * 1024
	chunkRunes    = 4000
	chunkOverlap  = 200
)

type Chunk struct {
	ID      string `json:"id"`
	Path    string `json:"path"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Content string `json:"content"`
	Digest  string `json:"digest"`
}

type Stats struct {
	Files     int `json:"files"`
	Chunks    int `json:"chunks"`
	TextBytes int `json:"text_bytes"`
	Skipped   int `json:"skipped"`
}

type Manifest struct {
	SchemaVersion     int               `json:"schema_version"`
	Identity          ProjectIdentity   `json:"identity"`
	IndexedIdentity   map[string]string `json:"indexed_identity"`
	Stats             Stats             `json:"stats"`
	VectorMode        string            `json:"vector_mode"`
	Vector            map[string]any    `json:"vector"`
	SourceFingerprint string            `json:"source_fingerprint"`
	IndexedAt         string            `json:"indexed_at"`
	UpdatedAt         string            `json:"updated_at"`
}

type Index struct {
	SchemaVersion int     `json:"schema_version"`
	ProjectID     string  `json:"project_id"`
	Chunks        []Chunk `json:"chunks"`
}

type IndexResult struct {
	ProjectDir        string         `json:"project_dir"`
	Stats             Stats          `json:"stats"`
	Vector            map[string]any `json:"vector"`
	State             string         `json:"state"`
	SourceFingerprint string         `json:"source_fingerprint"`
}

type BootstrapResult struct {
	ProjectDir string      `json:"project_dir"`
	Project    IndexResult `json:"project"`
	Global     IndexResult `json:"global"`
	VectorMode string      `json:"vector_mode"`
}

func Init(knowledgeRoot, projectRoot string) (Manifest, string, error) {
	identity, err := TrackProject(knowledgeRoot, projectRoot)
	if err != nil {
		return Manifest{}, "", err
	}
	projectDirectory := filepath.Join(knowledgeRoot, "projects", identity.ID)
	manifestPath := filepath.Join(projectDirectory, "manifest.json")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	manifest := Manifest{
		SchemaVersion: SchemaVersion, Identity: identity, IndexedIdentity: nil,
		VectorMode: "off", Vector: map[string]any{"enabled": false, "status": "NOT_BUILT"},
		UpdatedAt: now,
	}
	var existing Manifest
	if safeio.ReadJSON(manifestPath, &existing) == nil {
		manifest = existing
		manifest.Identity = identity
		manifest.UpdatedAt = now
	}
	if err := safeio.WriteJSON(manifestPath, manifest); err != nil {
		return Manifest{}, "", err
	}
	return manifest, projectDirectory, nil
}

func Build(knowledgeRoot, projectRoot, vectorMode string) (IndexResult, error) {
	if vectorMode == "" {
		vectorMode = "auto"
	}
	if vectorMode != "off" && vectorMode != "auto" && vectorMode != "on" {
		return IndexResult{}, errors.New("unsupported vector mode")
	}
	_, projectDirectory, err := Init(knowledgeRoot, projectRoot)
	if err != nil {
		return IndexResult{}, err
	}
	identity, err := IdentifyProject(projectRoot)
	if err != nil {
		return IndexResult{}, err
	}
	chunks, stats, err := scan(identity.Root, knowledgeRoot)
	if err != nil {
		return IndexResult{}, err
	}
	fingerprint := chunksFingerprint(chunks)
	index := Index{SchemaVersion: SchemaVersion, ProjectID: identity.ID, Chunks: chunks}
	if err := safeio.WriteJSON(filepath.Join(projectDirectory, "index.json"), index); err != nil {
		return IndexResult{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	state := QueryState(identity.Root)
	state["source_fingerprint"] = fingerprint
	vector := map[string]any{
		"enabled": false, "status": "LEXICAL_ONLY", "requested_mode": vectorMode,
		"reason": "vector backend is not configured in the Go runtime", "error": nil,
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, Identity: identity, IndexedIdentity: state,
		Stats: stats, VectorMode: vectorMode, Vector: vector, SourceFingerprint: fingerprint,
		IndexedAt: now, UpdatedAt: now,
	}
	if err := safeio.WriteJSON(filepath.Join(projectDirectory, "manifest.json"), manifest); err != nil {
		return IndexResult{}, err
	}
	return IndexResult{ProjectDir: projectDirectory, Stats: stats, Vector: vector, State: "REBUILT", SourceFingerprint: fingerprint}, nil
}

func Bootstrap(knowledgeRoot, projectRoot, vectorMode string) (BootstrapResult, error) {
	if vectorMode == "" {
		vectorMode = "auto"
	}
	project, err := Build(knowledgeRoot, projectRoot, vectorMode)
	if err != nil {
		return BootstrapResult{}, err
	}
	global, err := buildGlobal(knowledgeRoot, vectorMode)
	if err != nil {
		return BootstrapResult{}, err
	}
	return BootstrapResult{ProjectDir: project.ProjectDir, Project: project, Global: global, VectorMode: vectorMode}, nil
}

// BuildGlobal refreshes the global and personal-experience index without
// requiring a project worktree.
func BuildGlobal(knowledgeRoot, vectorMode string) (IndexResult, error) {
	if vectorMode == "" {
		vectorMode = "auto"
	}
	return buildGlobal(knowledgeRoot, vectorMode)
}

func buildGlobal(knowledgeRoot, vectorMode string) (IndexResult, error) {
	directory := filepath.Join(knowledgeRoot, "global-cache")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return IndexResult{}, err
	}
	var chunks []Chunk
	stats := Stats{}
	for _, root := range []string{filepath.Join(knowledgeRoot, "global"), filepath.Join(knowledgeRoot, "personal-experience")} {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			current, currentStats, scanErr := scan(root, filepath.Join(knowledgeRoot, "projects"))
			if scanErr != nil {
				return IndexResult{}, scanErr
			}
			for index := range current {
				current[index].Path = filepath.ToSlash(filepath.Join(filepath.Base(root), current[index].Path))
			}
			chunks = append(chunks, current...)
			stats.Files += currentStats.Files
			stats.Chunks += currentStats.Chunks
			stats.TextBytes += currentStats.TextBytes
			stats.Skipped += currentStats.Skipped
		}
	}
	fingerprint := chunksFingerprint(chunks)
	if err := safeio.WriteJSON(filepath.Join(directory, "index.json"), Index{SchemaVersion: SchemaVersion, ProjectID: "global", Chunks: chunks}); err != nil {
		return IndexResult{}, err
	}
	manifest := Manifest{
		SchemaVersion: SchemaVersion, Identity: ProjectIdentity{ID: "global", Name: "global", Root: knowledgeRoot},
		Stats: stats, VectorMode: vectorMode, Vector: map[string]any{"enabled": false, "status": "LEXICAL_ONLY", "requested_mode": vectorMode},
		SourceFingerprint: fingerprint, IndexedAt: time.Now().UTC().Format(time.RFC3339Nano), UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := safeio.WriteJSON(filepath.Join(directory, "manifest.json"), manifest); err != nil {
		return IndexResult{}, err
	}
	return IndexResult{ProjectDir: directory, Stats: stats, Vector: manifest.Vector, State: "REBUILT", SourceFingerprint: fingerprint}, nil
}

func scan(root, excludedRoot string) ([]Chunk, Stats, error) {
	paths, err := sourcePaths(root)
	if err != nil {
		return nil, Stats{}, err
	}
	var chunks []Chunk
	stats := Stats{}
	for _, relative := range paths {
		path := filepath.Join(root, filepath.FromSlash(relative))
		absolute, _ := filepath.Abs(path)
		excluded, _ := filepath.Abs(excludedRoot)
		if absolute == excluded || strings.HasPrefix(absolute, excluded+string(filepath.Separator)) || sensitive(relative) {
			stats.Skipped++
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxFileBytes {
			stats.Skipped++
			continue
		}
		if stats.TextBytes+int(info.Size()) > maxTotalBytes {
			stats.Skipped++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
			stats.Skipped++
			continue
		}
		content := string(data)
		if strings.TrimSpace(content) == "" {
			stats.Skipped++
			continue
		}
		stats.Files++
		stats.TextBytes += len(data)
		current := chunkText(relative, content)
		chunks = append(chunks, current...)
		stats.Chunks += len(current)
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Path == chunks[j].Path {
			return chunks[i].Start < chunks[j].Start
		}
		return chunks[i].Path < chunks[j].Path
	})
	return chunks, stats, nil
}

func sourcePaths(root string) ([]string, error) {
	if output, ok := gitBytes(root, "ls-files", "-z"); ok {
		var result []string
		for _, part := range bytes.Split(output, []byte{0}) {
			if len(part) > 0 {
				result = append(result, filepath.ToSlash(string(part)))
			}
		}
		sort.Strings(result)
		return result, nil
	}
	var result []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if path == root {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 || sensitive(relative) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			result = append(result, relative)
		}
		return nil
	})
	sort.Strings(result)
	return result, err
}

func chunkText(path, text string) []Chunk {
	runes := []rune(text)
	var result []Chunk
	for start := 0; start < len(runes); {
		end := start + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		content := strings.TrimSpace(string(runes[start:end]))
		if content != "" {
			digest := sha256.Sum256([]byte(content))
			digestText := hex.EncodeToString(digest[:])
			idDigest := sha256.Sum256([]byte(path + ":" + strconv.Itoa(start) + ":" + digestText))
			result = append(result, Chunk{ID: hex.EncodeToString(idDigest[:]), Path: filepath.ToSlash(path), Start: start, End: end, Content: content, Digest: digestText})
		}
		if end == len(runes) {
			break
		}
		start = end - chunkOverlap
	}
	return result
}

func chunksFingerprint(chunks []Chunk) string {
	digest := sha256.New()
	for _, chunk := range chunks {
		digest.Write([]byte(chunk.Path))
		digest.Write([]byte(chunk.Digest))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func sensitive(relative string) bool {
	value := strings.ToLower(filepath.ToSlash(relative))
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == '/' || r == '\\' || r == '.' || r == '-' || r == '_' })
	for _, part := range parts {
		switch part {
		case "git", ".git", "node_modules", "vendor", "dist", "build", "cache", "archive", "archives", "token", "tokens", "secret", "secrets", "credential", "credentials", "auth", "session", "sessions", "__pycache__", "venv", ".venv":
			return true
		}
	}
	base := strings.ToLower(filepath.Base(value))
	return base == ".env" || strings.HasPrefix(base, ".env.") || strings.HasSuffix(base, ".pem") || strings.HasSuffix(base, ".key")
}

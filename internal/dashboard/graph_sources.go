package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
)

const (
	maxGraphApprovedBytes   = 16 * 1024 * 1024
	maxGraphApprovedChunks  = 1200
	maxGraphApprovedEntries = 8192
	maxGraphApprovedFiles   = 2048
	maxGraphFileBytes       = 2 * 1024 * 1024
	maxGraphIndexBytes      = 32 * 1024 * 1024
	maxGraphIndexTotalBytes = 64 * 1024 * 1024
	maxGraphProjectEntries  = 512
	maxGraphProjectIndexes  = 128
	maxGraphProjectBytes    = 16 * 1024 * 1024
	maxGraphProjectChunks   = 1200
	graphLinesPerChunk      = 120
)

var graphApprovedRoots = []string{
	"verified",
	filepath.Join("personal-experience", "approved"),
	filepath.Join("error-experience", "approved"),
}

type graphSourceKind uint8

const (
	graphApprovedSource graphSourceKind = iota
	graphProjectSource
)

type graphSourceFile struct {
	path      string
	relative  string
	kind      graphSourceKind
	projectID string
}

type graphChunkPart struct {
	start   int
	end     int
	content string
}

type graphDocumentBuilder struct {
	document graphDocument
	parts    []graphChunkPart
}

type graphProjectSourceBudget struct {
	bytes int64
	count int
}

func (budget *graphProjectSourceBudget) allow(size int64) bool {
	if size < 1 || size > maxGraphIndexBytes ||
		budget.count >= maxGraphProjectIndexes || budget.bytes+size > maxGraphIndexTotalBytes {
		return false
	}
	budget.bytes += size
	budget.count++
	return true
}

func knowledgeGraphRevision(root string) (string, error) {
	sources, err := graphSourceFiles(root)
	if err != nil {
		return "", err
	}
	return graphRevision(sources), nil
}

func loadGraphDocuments(root string) ([]graphDocument, string, error) {
	sources, err := graphSourceFiles(root)
	if err != nil {
		return nil, "", err
	}
	revision := graphRevision(sources)
	documents := loadApprovedGraphDocuments(root, sources)
	documents = append(documents, loadProjectGraphDocuments(sources)...)
	sort.Slice(documents, func(i, j int) bool {
		if documents[i].Scope == documents[j].Scope {
			return documents[i].Path < documents[j].Path
		}
		return documents[i].Scope < documents[j].Scope
	})
	return documents, revision, nil
}

func graphSourceFiles(root string) ([]graphSourceFile, error) {
	var result []graphSourceFile
	approvedEntries := 0
	approvedFiles := 0
	for _, relativeRoot := range graphApprovedRoots {
		if approvedEntries >= maxGraphApprovedEntries || approvedFiles >= maxGraphApprovedFiles {
			break
		}
		directory := filepath.Join(root, relativeRoot)
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) || err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			continue
		}
		if err != nil {
			continue
		}
		_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			approvedEntries++
			if approvedEntries > maxGraphApprovedEntries {
				return fs.SkipAll
			}
			if walkErr != nil {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || !strings.EqualFold(filepath.Ext(entry.Name()), ".md") {
				return nil
			}
			if approvedFiles >= maxGraphApprovedFiles {
				return fs.SkipAll
			}
			relative, relativeErr := filepath.Rel(root, path)
			if relativeErr == nil {
				result = append(result, graphSourceFile{
					path: path, relative: filepath.ToSlash(relative), kind: graphApprovedSource,
				})
				approvedFiles++
			}
			return nil
		})
	}
	projectsRoot := filepath.Join(root, "projects")
	projectDirectory, err := os.Open(projectsRoot)
	if errors.Is(err, os.ErrNotExist) {
		sort.Slice(result, func(i, j int) bool { return result[i].relative < result[j].relative })
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	defer projectDirectory.Close()
	projects, readErr := projectDirectory.ReadDir(maxGraphProjectEntries)
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].Name() < projects[j].Name() })
	budget := graphProjectSourceBudget{}
	for _, project := range projects {
		if !project.IsDir() || project.Type()&os.ModeSymlink != 0 || !safeIdentifier(project.Name()) {
			continue
		}
		path := filepath.Join(projectsRoot, project.Name(), "index.json")
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!budget.allow(info.Size()) {
			continue
		}
		relative, _ := filepath.Rel(root, path)
		result = append(result, graphSourceFile{
			path: path, relative: filepath.ToSlash(relative), kind: graphProjectSource,
			projectID: project.Name(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].relative < result[j].relative })
	return result, nil
}

func graphRevision(sources []graphSourceFile) string {
	digest := sha256.New()
	for _, source := range sources {
		info, err := os.Lstat(source.path)
		if err != nil {
			continue
		}
		_, _ = fmt.Fprintf(
			digest, "%s\x00%d\x00%d\x00", source.relative, info.Size(), info.ModTime().UnixNano(),
		)
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func loadApprovedGraphDocuments(root string, sources []graphSourceFile) []graphDocument {
	result := make([]graphDocument, 0)
	usedBytes := 0
	usedChunks := 0
	for _, source := range sources {
		if source.kind != graphApprovedSource {
			continue
		}
		info, err := os.Lstat(source.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			info.Size() > maxGraphFileBytes || info.Size() < 1 {
			continue
		}
		data, err := os.ReadFile(source.path)
		if err != nil || !utf8.Valid(data) || containsSecret(string(data)) {
			continue
		}
		if usedBytes+len(data) > maxGraphApprovedBytes {
			continue
		}
		content := strings.ReplaceAll(string(data), "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		lines := strings.Split(content, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) == 0 || strings.TrimSpace(strings.Join(lines, "\n")) == "" {
			continue
		}
		chunks := (len(lines) + graphLinesPerChunk - 1) / graphLinesPerChunk
		remaining := maxGraphApprovedChunks - usedChunks
		if remaining <= 0 {
			break
		}
		if chunks > remaining {
			lines = lines[:remaining*graphLinesPerChunk]
			content = strings.Join(lines, "\n")
			chunks = remaining
		}
		content = strings.Join(lines, "\n")
		key := "global\x00" + source.relative
		result = append(result, graphDocument{
			ID: stableGraphID("document", key), Title: graphDocumentTitle(source.relative, content),
			Path: source.relative, Scope: "global", Content: content,
			LineStart: 1, LineEnd: len(lines), SourceChunks: chunks,
			Tokens: semanticGraphTokens(content),
		})
		usedBytes += len(data)
		usedChunks += chunks
	}
	return result
}

func loadProjectGraphDocuments(sources []graphSourceFile) []graphDocument {
	builders := map[string]*graphDocumentBuilder{}
	usedBytes := 0
	usedChunks := 0
	indexBudget := graphProjectSourceBudget{}
	for _, source := range sources {
		if source.kind != graphProjectSource || usedChunks >= maxGraphProjectChunks {
			continue
		}
		info, err := os.Lstat(source.path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!indexBudget.allow(info.Size()) {
			continue
		}
		data, err := os.ReadFile(source.path)
		if err != nil {
			continue
		}
		var index rag.Index
		if json.Unmarshal(data, &index) != nil {
			continue
		}
		if index.ProjectID != "" && index.ProjectID != source.projectID {
			continue
		}
		projectID := source.projectID
		scope := "project:" + projectID
		chunks := append([]rag.Chunk(nil), index.Chunks...)
		sort.Slice(chunks, func(i, j int) bool {
			if chunks[i].Path == chunks[j].Path {
				return chunks[i].Start < chunks[j].Start
			}
			return chunks[i].Path < chunks[j].Path
		})
		for _, chunk := range chunks {
			if usedChunks >= maxGraphProjectChunks || usedBytes+len(chunk.Content) > maxGraphProjectBytes {
				break
			}
			path, valid := validGraphProjectPath(chunk.Path)
			if !valid || chunk.Start < 0 || chunk.End < chunk.Start ||
				!utf8.ValidString(chunk.Content) || containsSecret(chunk.Content) {
				continue
			}
			key := scope + "\x00" + path
			builder := builders[key]
			if builder == nil {
				builder = &graphDocumentBuilder{document: graphDocument{
					ID: stableGraphID("document", key), Path: path, Scope: scope, ProjectID: projectID,
				}}
				builders[key] = builder
			}
			builder.parts = append(builder.parts, graphChunkPart{chunk.Start, chunk.End, chunk.Content})
			builder.document.SourceChunks++
			usedBytes += len(chunk.Content)
			usedChunks++
		}
	}
	keys := make([]string, 0, len(builders))
	for key := range builders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]graphDocument, 0, len(keys))
	for _, key := range keys {
		builder := builders[key]
		builder.document.Content = mergeGraphChunkParts(builder.parts)
		builder.document.Title = graphDocumentTitle(builder.document.Path, builder.document.Content)
		builder.document.Tokens = semanticGraphTokens(builder.document.Title + "\n" + builder.document.Content)
		result = append(result, builder.document)
	}
	return result
}

func validGraphProjectPath(value string) (string, bool) {
	clean := filepath.Clean(filepath.FromSlash(value))
	if filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(clean), true
}

func mergeGraphChunkParts(parts []graphChunkPart) string {
	sort.Slice(parts, func(i, j int) bool { return parts[i].start < parts[j].start })
	var builder strings.Builder
	lastEnd := -1
	for _, part := range parts {
		runes := []rune(strings.TrimSpace(part.content))
		skip := 0
		if lastEnd > part.start {
			skip = lastEnd - part.start
		}
		if skip >= len(runes) {
			if part.end > lastEnd {
				lastEnd = part.end
			}
			continue
		}
		if builder.Len() > 0 {
			builder.WriteByte('\n')
		}
		builder.WriteString(string(runes[skip:]))
		if part.end > lastEnd {
			lastEnd = part.end
		}
	}
	return builder.String()
}

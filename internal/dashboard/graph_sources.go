package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	maxGraphSources         = 1200
	maxGraphSourceRunes     = 8000
	maxGraphProjectEntries  = 512
	maxGraphProjectIndexes  = 128
	maxGraphIndexBytes      = 32 * 1024 * 1024
	maxGraphIndexTotalBytes = 64 * 1024 * 1024
)

type graphSourceTarget struct {
	directory string
	scope     string
	projectID string
	global    bool
}

type graphSourceBudget struct {
	bytes int64
	count int
}

type graphSourceManifest struct {
	SchemaVersion     int                 `json:"schema_version"`
	Identity          rag.ProjectIdentity `json:"identity"`
	Vector            map[string]any      `json:"vector"`
	IndexedAt         string              `json:"indexed_at"`
	NodeID            string              `json:"node_id"`
	Generation        string              `json:"generation"`
	SourceFingerprint string              `json:"source_fingerprint"`
	MembershipDigest  string              `json:"membership_digest"`
}

func (budget *graphSourceBudget) allow(size int64) bool {
	if size < 1 || size > maxGraphIndexBytes || budget.count >= maxGraphProjectIndexes ||
		budget.bytes+size > maxGraphIndexTotalBytes {
		return false
	}
	budget.bytes += size
	budget.count++
	return true
}

func loadGraphSources(root string) ([]graphSource, string, bool) {
	targets := graphSourceTargets(root)
	sources := make([]graphSource, 0, maxGraphSources)
	vectorAvailable := false
	remaining := maxGraphSources
	budget := graphSourceBudget{}
	for targetIndex, current := range targets {
		if remaining <= 0 {
			break
		}
		indexPath := filepath.Join(current.directory, "index.json")
		info, err := os.Lstat(indexPath)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!budget.allow(info.Size()) {
			continue
		}
		var index rag.Index
		if err := safeio.ReadJSON(indexPath, &index); err != nil || index.SchemaVersion != rag.SchemaVersion {
			continue
		}
		expectedID := current.projectID
		if current.global {
			expectedID = "global"
		}
		if index.ProjectID != expectedID {
			continue
		}
		manifest, err := readGraphSourceManifest(filepath.Join(current.directory, "manifest.json"), expectedID)
		if err != nil {
			continue
		}
		if enabled, ok := manifest.Vector["enabled"].(bool); ok && enabled {
			vectorAvailable = true
		}
		chunks := append([]rag.Chunk(nil), index.Chunks...)
		sort.Slice(chunks, func(i, j int) bool {
			if chunks[i].Path == chunks[j].Path {
				if chunks[i].Start == chunks[j].Start {
					return chunks[i].ID < chunks[j].ID
				}
				return chunks[i].Start < chunks[j].Start
			}
			return chunks[i].Path < chunks[j].Path
		})
		slots := len(targets) - targetIndex
		allowance := max(1, remaining/slots)
		used := 0
		for _, chunk := range chunks {
			if used >= allowance || remaining <= 0 {
				break
			}
			if !validLibraryChunk(chunk, current.global) {
				continue
			}
			if (chunk.LineStart == 0) != (chunk.LineEnd == 0) ||
				(chunk.LineStart > 0 && chunk.LineEnd < chunk.LineStart) {
				continue
			}
			sources = append(sources, graphSource{
				Scope: current.scope, ProjectID: current.projectID, Path: chunk.Path,
				Start: chunk.Start, End: chunk.End, LineStart: chunk.LineStart, LineEnd: chunk.LineEnd,
				Content: truncateRunes(chunk.Content, maxGraphSourceRunes), Digest: chunk.Digest,
				IndexedAt: manifest.IndexedAt,
			})
			used++
			remaining--
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		if sources[i].Scope != sources[j].Scope {
			return sources[i].Scope < sources[j].Scope
		}
		if sources[i].Path != sources[j].Path {
			return sources[i].Path < sources[j].Path
		}
		if sources[i].Start != sources[j].Start {
			return sources[i].Start < sources[j].Start
		}
		return sources[i].Digest < sources[j].Digest
	})
	unique := sources[:0]
	seen := map[string]struct{}{}
	for _, source := range sources {
		key := source.Scope + "\x00" + source.Path + "\x00" + strconv.Itoa(source.Start) + "\x00" + source.Digest
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, source)
	}
	digest := sha256.New()
	writeGraphDigest(digest, strconv.FormatBool(vectorAvailable))
	for _, source := range unique {
		writeGraphDigest(
			digest, source.Scope, source.ProjectID, source.Path, strconv.Itoa(source.Start), strconv.Itoa(source.End),
			strconv.Itoa(source.LineStart), strconv.Itoa(source.LineEnd), source.Digest, source.IndexedAt,
		)
	}
	return unique, hex.EncodeToString(digest.Sum(nil)), vectorAvailable
}

func readGraphSourceManifest(path, expectedID string) (graphSourceManifest, error) {
	var manifest graphSourceManifest
	if err := safeio.ReadJSON(path, &manifest); err != nil {
		return graphSourceManifest{}, err
	}
	if manifest.SchemaVersion != rag.SchemaVersion {
		return graphSourceManifest{}, os.ErrInvalid
	}
	if manifest.Identity.ID == expectedID {
		return manifest, nil
	}
	if manifest.NodeID != expectedID || !validVersion(manifest.Generation) ||
		manifest.SourceFingerprint != manifest.Generation || manifest.MembershipDigest != manifest.Generation {
		return graphSourceManifest{}, os.ErrInvalid
	}
	return manifest, nil
}

func graphSourceTargets(root string) []graphSourceTarget {
	targets := []graphSourceTarget{{
		directory: filepath.Join(root, "global-cache"), scope: "global", global: true,
	}}
	entries, err := os.ReadDir(filepath.Join(root, "projects"))
	if err != nil {
		return targets
	}
	if len(entries) > maxGraphProjectEntries {
		entries = entries[:maxGraphProjectEntries]
	}
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && safeIdentifier(entry.Name()) {
			targets = append(targets, graphSourceTarget{
				directory: filepath.Join(root, "projects", entry.Name()),
				scope:     "project:" + entry.Name(), projectID: entry.Name(),
			})
		}
	}
	return targets
}

func writeGraphDigest(digest hash.Hash, values ...string) {
	for _, value := range values {
		_, _ = digest.Write([]byte(value))
		_, _ = digest.Write([]byte{0})
	}
}

func groupGraphDocuments(sources []graphSource) []graphDocument {
	type partial struct {
		graphDocument
		parts []graphSource
	}
	grouped := map[string]*partial{}
	for _, source := range sources {
		key := source.Scope + "\x00" + source.Path
		row := grouped[key]
		if row == nil {
			row = &partial{graphDocument: graphDocument{
				ID: graphDocumentID(source.Scope, source.Path), Scope: source.Scope,
				ProjectID: source.ProjectID, Path: source.Path, IndexedAt: source.IndexedAt,
				LineStart: source.LineStart, LineEnd: source.LineEnd,
			}}
			grouped[key] = row
		}
		row.parts = append(row.parts, source)
		if source.LineStart < row.LineStart {
			row.LineStart = source.LineStart
		}
		if source.LineEnd > row.LineEnd {
			row.LineEnd = source.LineEnd
		}
	}
	documents := make([]graphDocument, 0, len(grouped))
	for _, row := range grouped {
		row.Content = mergeGraphSourceParts(row.parts)
		row.Title = graphDocumentTitle(row.Path, row.Content)
		row.Tokens = semanticGraphTokens(row.Content)
		documents = append(documents, row.graphDocument)
	}
	sort.Slice(documents, func(i, j int) bool {
		if documents[i].Scope == documents[j].Scope {
			return documents[i].Path < documents[j].Path
		}
		return documents[i].Scope < documents[j].Scope
	})
	return documents
}

func mergeGraphSourceParts(parts []graphSource) string {
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].Start != parts[j].Start {
			return parts[i].Start < parts[j].Start
		}
		return parts[i].End < parts[j].End
	})
	merged := make([]rune, 0)
	lastEnd := -1
	for _, part := range parts {
		content := []rune(part.Content)
		overlap := 0
		if lastEnd > part.Start {
			overlap = min(lastEnd-part.Start, len(content), len(merged))
			for overlap > 0 && string(merged[len(merged)-overlap:]) != string(content[:overlap]) {
				overlap--
			}
		}
		if len(merged) > 0 && overlap == 0 {
			merged = append(merged, '\n')
		}
		merged = append(merged, content[overlap:]...)
		lastEnd = max(lastEnd, part.End)
	}
	return string(merged)
}

func graphDocumentTitle(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		if match := graphHeadingPattern.FindStringSubmatch(line); len(match) > 1 {
			if title := canonicalGraphLabel(match[1]); title != "" {
				return title
			}
		}
	}
	base := filepath.Base(filepath.FromSlash(path))
	base = strings.TrimSuffix(base, filepath.Ext(base))
	if title := truncateRunes(strings.TrimSpace(base), 80); title != "" {
		return title
	}
	return "知识文档"
}

func graphDocumentID(scope, path string) string {
	return stableGraphID("doc", scope+"\x00"+path)
}

func graphEntityID(label string) string {
	return stableGraphID("entity", strings.ToLower(canonicalGraphLabel(label)))
}

func stableGraphID(prefix, value string) string {
	digest := sha256.Sum256([]byte(value))
	return prefix + ":" + hex.EncodeToString(digest[:8])
}

func graphGeneratedAt() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

package rag

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

func scanApprovedSources(knowledgeRoot string) ([]Chunk, []scannedGroupDocument, Stats, error) {
	root, err := filepath.Abs(knowledgeRoot)
	if err != nil {
		return nil, nil, Stats{}, groupFailure("UNSAFE_GROUP_SOURCE")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, Stats{}, groupFailure("UNSAFE_GROUP_SOURCE")
	}
	var paths []string
	stats := Stats{}
	for _, approvedRoot := range approvedRoots {
		directory := filepath.Join(root, filepath.FromSlash(approvedRoot))
		info, statErr := os.Lstat(directory)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, nil, Stats{}, groupFailure("UNSAFE_GROUP_SOURCE")
		}
		if _, err := safeio.Contained(root, directory); err != nil {
			return nil, nil, Stats{}, groupFailure("UNSAFE_GROUP_SOURCE")
		}
		err = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				stats.Skipped++
				return nil
			}
			if path == directory {
				return nil
			}
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				stats.Skipped++
				return nil
			}
			relative = filepath.ToSlash(relative)
			if entry.Type()&os.ModeSymlink != 0 || securitycheck.IsSensitivePath(relative) {
				stats.Skipped++
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if !entry.IsDir() {
				paths = append(paths, relative)
			}
			return nil
		})
		if err != nil {
			return nil, nil, Stats{}, groupFailure("GROUP_SOURCE_SCAN_FAILED")
		}
	}
	sort.Strings(paths)
	chunks := make([]Chunk, 0)
	documents := make([]scannedGroupDocument, 0)
	for _, relative := range paths {
		path, err := safeio.Contained(root, filepath.Join(root, filepath.FromSlash(relative)))
		if err != nil {
			return nil, nil, Stats{}, groupFailure("UNSAFE_GROUP_SOURCE")
		}
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Size() > maxFileBytes || before.Size() < 1 {
			stats.Skipped++
			continue
		}
		if stats.TextBytes+int(before.Size()) > maxTotalBytes {
			stats.Skipped++
			continue
		}
		data, err := os.ReadFile(path)
		after, afterErr := os.Lstat(path)
		if err != nil || afterErr != nil || !os.SameFile(before, after) || int64(len(data)) != before.Size() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
			return nil, nil, Stats{}, groupFailure("SOURCE_CHANGED_DURING_BUILD")
		}
		if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) || strings.TrimSpace(string(data)) == "" || securitycheck.ContainsHighConfidenceSecret(string(data)) {
			stats.Skipped++
			continue
		}
		digest := sha256.Sum256(data)
		documentID := sha256.Sum256([]byte(relative))
		current := chunkText(relative, string(data))
		chunks = append(chunks, current...)
		documents = append(documents, scannedGroupDocument{
			GroupDocument: GroupDocument{
				DocumentID: hex.EncodeToString(documentID[:]), Path: relative,
				SourceSHA256: hex.EncodeToString(digest[:]),
			},
			Bytes: len(data),
		})
		stats.Files++
		stats.TextBytes += len(data)
		stats.Chunks += len(current)
	}
	sort.Slice(chunks, func(i, j int) bool {
		if chunks[i].Path == chunks[j].Path {
			return chunks[i].Start < chunks[j].Start
		}
		return chunks[i].Path < chunks[j].Path
	})
	return chunks, documents, stats, nil
}

func validateGroupSelection(values []string) (map[string]bool, error) {
	if len(values) == 0 {
		return nil, nil
	}
	selected := make(map[string]bool, len(values))
	for _, value := range values {
		if len(value) != 64 || !lowerHex(value) {
			return nil, groupFailure("INVALID_DOCUMENT_IDS")
		}
		if selected[value] {
			return nil, groupFailure("DUPLICATE_DOCUMENT_ID")
		}
		selected[value] = true
	}
	return selected, nil
}

func selectGroupSources(chunks []Chunk, documents []scannedGroupDocument, stats Stats, selected map[string]bool) ([]Chunk, []scannedGroupDocument, Stats, error) {
	if selected == nil {
		return chunks, documents, stats, nil
	}
	known := make(map[string]bool, len(documents))
	paths := map[string]bool{}
	filteredDocuments := make([]scannedGroupDocument, 0, len(selected))
	filteredStats := Stats{Skipped: stats.Skipped}
	for _, document := range documents {
		known[document.DocumentID] = true
		if selected[document.DocumentID] {
			paths[document.Path] = true
			filteredDocuments = append(filteredDocuments, document)
			filteredStats.Files++
			filteredStats.TextBytes += document.Bytes
		}
	}
	for identifier := range selected {
		if !known[identifier] {
			return nil, nil, Stats{}, groupFailure("UNKNOWN_DOCUMENT_ID")
		}
	}
	filteredChunks := make([]Chunk, 0)
	for _, chunk := range chunks {
		if paths[chunk.Path] {
			filteredChunks = append(filteredChunks, chunk)
		}
	}
	filteredStats.Chunks = len(filteredChunks)
	return filteredChunks, filteredDocuments, filteredStats, nil
}

func publicGroupDocuments(documents []scannedGroupDocument) []GroupDocument {
	result := make([]GroupDocument, 0, len(documents))
	for _, document := range documents {
		result = append(result, document.GroupDocument)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func groupMembershipDigest(chunks []Chunk, documents []GroupDocument) string {
	type membership struct {
		Chunks    []Chunk         `json:"chunks"`
		Documents []GroupDocument `json:"documents"`
	}
	payload, _ := json.Marshal(membership{Chunks: chunks, Documents: documents})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

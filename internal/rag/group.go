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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

const groupManifestSchema = 1

var groupIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var approvedRoots = []string{
	"verified",
	filepath.ToSlash(filepath.Join("personal-experience", "approved")),
	filepath.ToSlash(filepath.Join("error-experience", "approved")),
}

type GroupDocument struct {
	DocumentID   string `json:"document_id"`
	Path         string `json:"path"`
	SourceSHA256 string `json:"source_sha256"`
}

type GroupManifest struct {
	SchemaVersion     int             `json:"schema_version"`
	NodeID            string          `json:"node_id"`
	Generation        string          `json:"generation"`
	SourceFingerprint string          `json:"source_fingerprint"`
	MembershipDigest  string          `json:"membership_digest"`
	VectorMode        string          `json:"vector_mode"`
	Vector            map[string]any  `json:"vector"`
	Documents         []GroupDocument `json:"documents"`
	Stats             Stats           `json:"stats"`
	IndexedAt         string          `json:"indexed_at"`
}

type GroupStatus struct {
	Status     string `json:"status"`
	Generation string `json:"generation,omitempty"`
	Documents  int    `json:"documents"`
	Chunks     int    `json:"chunks"`
	IndexedAt  string `json:"indexed_at,omitempty"`
}

type GroupReceipt struct {
	Status      string `json:"status"`
	NodeID      string `json:"node_id"`
	Generation  string `json:"generation"`
	Documents   int    `json:"documents"`
	Chunks      int    `json:"chunks"`
	IndexedAt   string `json:"indexed_at"`
	SourceScope string `json:"source_scope"`
}

type GroupError struct{ Code string }

func (e *GroupError) Error() string { return e.Code }

type scannedGroupDocument struct {
	GroupDocument
	Bytes int
}

// BuildGroup materializes a governed, immutable-generation index for one
// group node. Only verified and explicitly approved local sources are read.
func BuildGroup(knowledgeRoot, nodeID string, documentIDs []string) (GroupReceipt, error) {
	if !groupIdentifierPattern.MatchString(nodeID) {
		return GroupReceipt{}, groupFailure("INVALID_NODE_ID")
	}
	selected, err := validateGroupSelection(documentIDs)
	if err != nil {
		return GroupReceipt{}, err
	}
	chunks, documents, stats, err := scanApprovedSources(knowledgeRoot)
	if err != nil {
		return GroupReceipt{}, err
	}
	chunks, documents, stats, err = selectGroupSources(chunks, documents, stats, selected)
	if err != nil {
		return GroupReceipt{}, err
	}
	generation := groupMembershipDigest(chunks, publicGroupDocuments(documents))
	current := ReadGroupStatus(knowledgeRoot, nodeID)
	if current.Status == "READY" && current.Generation == generation {
		return groupReceipt("REUSED", nodeID, current), nil
	}
	active, staging, err := groupLocations(knowledgeRoot, nodeID, true)
	if err != nil {
		return GroupReceipt{}, err
	}
	token, err := safeio.RandomHex(16)
	if err != nil {
		return GroupReceipt{}, groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	stage := filepath.Join(staging, nodeID+"-"+token)
	if err := os.Mkdir(stage, 0o700); err != nil {
		return GroupReceipt{}, groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	defer func() { _ = removeGroupDirectory(staging, stage) }()
	index := Index{SchemaVersion: SchemaVersion, ProjectID: nodeID, Chunks: chunks}
	if err := safeio.WriteJSON(filepath.Join(stage, "index.json"), index); err != nil {
		return GroupReceipt{}, groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	vector, err := writeVectors(stage, chunks, generation, "auto")
	if err != nil {
		return GroupReceipt{}, groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	manifest := GroupManifest{
		SchemaVersion: groupManifestSchema, NodeID: nodeID, Generation: generation,
		SourceFingerprint: generation, MembershipDigest: generation,
		VectorMode: "auto", Vector: vector, Documents: publicGroupDocuments(documents),
		Stats: stats, IndexedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := safeio.WriteJSON(filepath.Join(stage, "manifest.json"), manifest); err != nil {
		return GroupReceipt{}, groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	if _, _, err := readGroupIndex(stage, nodeID); err != nil {
		return GroupReceipt{}, err
	}
	latestChunks, latestDocuments, latestStats, err := scanApprovedSources(knowledgeRoot)
	if err != nil {
		return GroupReceipt{}, err
	}
	latestChunks, latestDocuments, _, err = selectGroupSources(latestChunks, latestDocuments, latestStats, selected)
	if err != nil || groupMembershipDigest(latestChunks, publicGroupDocuments(latestDocuments)) != generation {
		return GroupReceipt{}, groupFailure("SOURCE_CHANGED_DURING_BUILD")
	}
	if err := switchGroupIndex(active, stage, staging, nodeID); err != nil {
		return GroupReceipt{}, err
	}
	status := ReadGroupStatus(knowledgeRoot, nodeID)
	if status.Status != "READY" {
		return GroupReceipt{}, groupFailure("ACTIVE_INDEX_READBACK_FAILED")
	}
	return groupReceipt("REBUILT", nodeID, status), nil
}

// ReadGroupStatus validates the active artifacts and checks that their source
// documents are still the current approved or verified versions.
func ReadGroupStatus(knowledgeRoot, nodeID string) GroupStatus {
	if !groupIdentifierPattern.MatchString(nodeID) {
		return GroupStatus{Status: "CORRUPT"}
	}
	active, _, err := groupLocations(knowledgeRoot, nodeID, false)
	if errors.Is(err, os.ErrNotExist) {
		return GroupStatus{Status: "NOT_CONFIGURED"}
	}
	if err != nil {
		return GroupStatus{Status: "CORRUPT"}
	}
	manifest, _, err := readGroupIndex(active, nodeID)
	if err != nil {
		return GroupStatus{Status: "CORRUPT"}
	}
	status := GroupStatus{
		Status: "READY", Generation: manifest.Generation,
		Documents: len(manifest.Documents), Chunks: manifest.Stats.Chunks, IndexedAt: manifest.IndexedAt,
	}
	selection := make(map[string]bool, len(manifest.Documents))
	for _, document := range manifest.Documents {
		selection[document.DocumentID] = true
	}
	chunks, documents, stats, err := scanApprovedSources(knowledgeRoot)
	if err != nil {
		status.Status = "STALE"
		return status
	}
	chunks, documents, _, err = selectGroupSources(chunks, documents, stats, selection)
	if err != nil || groupMembershipDigest(chunks, publicGroupDocuments(documents)) != manifest.Generation {
		status.Status = "STALE"
	}
	return status
}

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

func readGroupIndex(directory, nodeID string) (GroupManifest, Index, error) {
	var manifest GroupManifest
	if err := safeio.ReadJSON(filepath.Join(directory, "manifest.json"), &manifest); err != nil {
		return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_MANIFEST_INVALID")
	}
	var index Index
	if err := safeio.ReadJSON(filepath.Join(directory, "index.json"), &index); err != nil {
		return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_INVALID")
	}
	if manifest.SchemaVersion != groupManifestSchema || manifest.NodeID != nodeID || index.SchemaVersion != SchemaVersion || index.ProjectID != nodeID ||
		len(manifest.Generation) != 64 || !lowerHex(manifest.Generation) || manifest.SourceFingerprint != manifest.Generation || manifest.MembershipDigest != manifest.Generation ||
		manifest.Stats.Files != len(manifest.Documents) || manifest.Stats.Chunks != len(index.Chunks) || manifest.IndexedAt == "" {
		return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_MANIFEST_INVALID")
	}
	paths := make(map[string]bool, len(manifest.Documents))
	previousPath := ""
	for index, document := range manifest.Documents {
		if !approvedSourcePath(document.Path) || document.DocumentID != sha256Text(document.Path) || len(document.SourceSHA256) != 64 || !lowerHex(document.SourceSHA256) || paths[document.Path] || index > 0 && document.Path <= previousPath {
			return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_PROVENANCE_MISMATCH")
		}
		paths[document.Path], previousPath = true, document.Path
	}
	chunkPaths := map[string]bool{}
	for _, chunk := range index.Chunks {
		if !validGroupChunk(chunk) {
			return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_MEMBERSHIP_MISMATCH")
		}
		chunkPaths[chunk.Path] = true
	}
	if len(paths) != len(chunkPaths) {
		return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_PROVENANCE_MISMATCH")
	}
	for path := range paths {
		if !chunkPaths[path] {
			return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_PROVENANCE_MISMATCH")
		}
	}
	if groupMembershipDigest(index.Chunks, manifest.Documents) != manifest.Generation {
		return GroupManifest{}, Index{}, groupFailure("GROUP_INDEX_MEMBERSHIP_MISMATCH")
	}
	if _, ready := readVectors(directory, manifest.Generation); !ready {
		return GroupManifest{}, Index{}, groupFailure("GROUP_VECTOR_INDEX_INVALID")
	}
	return manifest, index, nil
}

func validGroupChunk(chunk Chunk) bool {
	if !approvedSourcePath(chunk.Path) || chunk.Start < 0 || chunk.End <= chunk.Start || strings.TrimSpace(chunk.Content) == "" {
		return false
	}
	digest := sha256Text(chunk.Content)
	return chunk.Digest == digest && chunk.ID == sha256Text(chunk.Path+":"+itoa(chunk.Start)+":"+digest)
}

func groupLocations(knowledgeRoot, nodeID string, create bool) (string, string, error) {
	root, err := filepath.Abs(knowledgeRoot)
	if err != nil || !groupIdentifierPattern.MatchString(nodeID) {
		return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
	}
	libraries := filepath.Join(root, "libraries")
	staging := filepath.Join(libraries, ".staging")
	if create {
		if err := os.MkdirAll(staging, 0o700); err != nil {
			return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
	} else {
		if _, err := os.Lstat(libraries); errors.Is(err, os.ErrNotExist) {
			return "", "", os.ErrNotExist
		}
	}
	for _, directory := range []string{libraries, staging} {
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) && !create && directory == staging {
			continue
		}
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
		if _, err := safeio.Contained(root, directory); err != nil {
			return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
	}
	active := filepath.Join(libraries, nodeID)
	if info, err := os.Lstat(active); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
		if _, err := safeio.Contained(libraries, active); err != nil {
			return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", groupFailure("UNSAFE_GROUP_INDEX_PATH")
	} else if !create {
		return "", "", os.ErrNotExist
	}
	return active, staging, nil
}

func switchGroupIndex(active, stage, staging, nodeID string) error {
	token, err := safeio.RandomHex(16)
	if err != nil {
		return groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	backup := filepath.Join(staging, "backup-"+nodeID+"-"+token)
	hadActive := false
	if _, err := os.Lstat(active); err == nil {
		hadActive = true
		if err := os.Rename(active, backup); err != nil {
			return groupFailure("GROUP_INDEX_SWITCH_FAILED")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return groupFailure("GROUP_INDEX_SWITCH_FAILED")
	}
	if err := os.Rename(stage, active); err != nil {
		if hadActive {
			_ = os.Rename(backup, active)
		}
		return groupFailure("GROUP_INDEX_SWITCH_FAILED")
	}
	if _, _, err := readGroupIndex(active, nodeID); err != nil {
		_ = removeGroupDirectory(filepath.Dir(active), active)
		if hadActive {
			_ = os.Rename(backup, active)
		}
		return groupFailure("ACTIVE_INDEX_READBACK_FAILED")
	}
	if hadActive {
		_ = removeGroupDirectory(staging, backup)
	}
	return nil
}

func removeGroupDirectory(parent, target string) error {
	info, err := os.Lstat(target)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return groupFailure("UNSAFE_GROUP_INDEX_PATH")
	}
	parentAbs, _ := filepath.Abs(parent)
	targetAbs, _ := filepath.Abs(target)
	relative, err := filepath.Rel(parentAbs, targetAbs)
	if err != nil || relative == "." || relative == ".." || strings.Contains(relative, string(filepath.Separator)) {
		return groupFailure("UNSAFE_GROUP_INDEX_PATH")
	}
	err = filepath.WalkDir(targetAbs, func(_ string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return groupFailure("UNSAFE_GROUP_INDEX_PATH")
		}
		return nil
	})
	if err != nil {
		return err
	}
	return os.RemoveAll(targetAbs)
}

func approvedSourcePath(value string) bool {
	if value == "" || value != filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) || filepath.IsAbs(filepath.FromSlash(value)) || strings.HasPrefix(value, "../") {
		return false
	}
	for _, root := range approvedRoots {
		if value == root || strings.HasPrefix(value, root+"/") {
			return true
		}
	}
	return false
}

func lowerHex(value string) bool {
	for _, character := range value {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func groupReceipt(status, nodeID string, current GroupStatus) GroupReceipt {
	return GroupReceipt{
		Status: status, NodeID: nodeID, Generation: current.Generation,
		Documents: current.Documents, Chunks: current.Chunks, IndexedAt: current.IndexedAt,
		SourceScope: "approved-verified-only",
	}
}

func groupFailure(code string) error { return &GroupError{Code: code} }

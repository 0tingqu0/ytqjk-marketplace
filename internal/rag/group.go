package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
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

// ValidateGroupSelection applies the same document membership contract used by BuildGroup.
func ValidateGroupSelection(documentIDs []string) error {
	_, err := validateGroupSelection(documentIDs)
	return err
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
	commitStatus, err := switchGroupIndex(active, stage, staging, nodeID)
	if err != nil {
		return GroupReceipt{}, err
	}
	committed := GroupStatus{
		Status: "READY", Generation: manifest.Generation,
		Documents: len(manifest.Documents), Chunks: manifest.Stats.Chunks, IndexedAt: manifest.IndexedAt,
	}
	return groupReceipt(commitStatus, nodeID, committed), nil
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

func switchGroupIndex(active, stage, staging, nodeID string) (string, error) {
	token, err := safeio.RandomHex(16)
	if err != nil {
		return "", groupFailure("GROUP_INDEX_BUILD_FAILED")
	}
	backup := filepath.Join(staging, "backup-"+nodeID+"-"+token)
	hadActive := false
	if _, err := os.Lstat(active); err == nil {
		hadActive = true
		if err := os.Rename(active, backup); err != nil {
			return "", groupFailure("GROUP_INDEX_SWITCH_FAILED")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", groupFailure("GROUP_INDEX_SWITCH_FAILED")
	}
	if err := os.Rename(stage, active); err != nil {
		if hadActive && restoreGroupIndex(active, backup, nodeID) != nil {
			return "", groupFailure("GROUP_INDEX_ROLLBACK_FAILED")
		}
		return "", groupFailure("GROUP_INDEX_SWITCH_FAILED")
	}
	if _, _, err := readGroupIndex(active, nodeID); err != nil {
		if rollbackGroupIndex(active, backup, nodeID, hadActive) != nil {
			return "", groupFailure("GROUP_INDEX_ROLLBACK_FAILED")
		}
		return "", groupFailure("ACTIVE_INDEX_READBACK_FAILED")
	}
	if hadActive && removeGroupDirectory(staging, backup) != nil {
		return "REBUILT_CLEANUP_PENDING", nil
	}
	return "REBUILT", nil
}

func rollbackGroupIndex(active, backup, nodeID string, hadActive bool) error {
	if err := removeGroupDirectory(filepath.Dir(active), active); err != nil {
		return err
	}
	if !hadActive {
		return nil
	}
	return restoreGroupIndex(active, backup, nodeID)
}

func restoreGroupIndex(active, backup, nodeID string) error {
	if err := os.Rename(backup, active); err != nil {
		return err
	}
	_, _, err := readGroupIndex(active, nodeID)
	return err
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

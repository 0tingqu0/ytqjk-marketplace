package rag

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

const maxQueryIndexChunks = 100000

func validateIndexForQuery(index Index) error {
	if index.SchemaVersion != SchemaVersion {
		return errors.New("index schema is unsupported")
	}
	if !validIndexProjectID(index.ProjectID) {
		return errors.New("index project identity is invalid")
	}
	if len(index.Chunks) > maxQueryIndexChunks {
		return errors.New("index chunk count exceeds the safety limit")
	}
	global := index.ProjectID == "global"
	seenIDs := make(map[string]struct{}, len(index.Chunks))
	seenPositions := make(map[string]struct{}, len(index.Chunks))
	totalBytes := 0
	for _, chunk := range index.Chunks {
		if err := validateChunkForQuery(chunk, global); err != nil {
			return err
		}
		if _, duplicate := seenIDs[chunk.ID]; duplicate {
			return errors.New("index contains a duplicate chunk ID")
		}
		seenIDs[chunk.ID] = struct{}{}
		position := chunk.Path + "\x00" + fmt.Sprint(chunk.Start)
		if _, duplicate := seenPositions[position]; duplicate {
			return errors.New("index contains a duplicate chunk position")
		}
		seenPositions[position] = struct{}{}
		totalBytes += len(chunk.Content)
		if totalBytes > maxTotalBytes*2 {
			return errors.New("index content exceeds the safety limit")
		}
	}
	return nil
}

func readQueryManifest(path, projectID string) (Manifest, error) {
	var value struct {
		SchemaVersion     int               `json:"schema_version"`
		Identity          ProjectIdentity   `json:"identity"`
		IndexedIdentity   map[string]string `json:"indexed_identity"`
		Stats             Stats             `json:"stats"`
		VectorMode        string            `json:"vector_mode"`
		Vector            map[string]any    `json:"vector"`
		SourceFingerprint string            `json:"source_fingerprint"`
		IndexedAt         string            `json:"indexed_at"`
		UpdatedAt         string            `json:"updated_at"`
		NodeID            string            `json:"node_id"`
		Generation        string            `json:"generation"`
		MembershipDigest  string            `json:"membership_digest"`
	}
	if err := safeio.ReadJSON(path, &value); err != nil {
		return Manifest{}, err
	}
	switch value.SchemaVersion {
	case SchemaVersion:
		if value.Identity.ID != "" && value.Identity.ID != projectID {
			return Manifest{}, errors.New("manifest project identity does not match index")
		}
	case groupManifestSchema:
		if value.NodeID != projectID || !validIndexDigest(value.Generation) ||
			value.SourceFingerprint != value.Generation || value.MembershipDigest != value.Generation {
			return Manifest{}, errors.New("group manifest identity or generation is invalid")
		}
	default:
		return Manifest{}, errors.New("manifest schema is unsupported")
	}
	return Manifest{
		SchemaVersion: SchemaVersion, Identity: value.Identity,
		IndexedIdentity: value.IndexedIdentity, Stats: value.Stats,
		VectorMode: value.VectorMode, Vector: value.Vector,
		SourceFingerprint: value.SourceFingerprint,
		IndexedAt:         value.IndexedAt, UpdatedAt: value.UpdatedAt,
	}, nil
}

func validateChunkForQuery(chunk Chunk, global bool) error {
	path := filepath.ToSlash(filepath.Clean(filepath.FromSlash(chunk.Path)))
	if chunk.Path == "" || len(chunk.Path) > 4096 || path != chunk.Path ||
		filepath.IsAbs(filepath.FromSlash(chunk.Path)) || path == "." || path == ".." || strings.HasPrefix(path, "../") {
		return errors.New("index contains an invalid chunk path")
	}
	if global && !governedGlobalIndexPath(path) {
		return errors.New("global index contains an ungoverned path")
	}
	if securitycheck.IsSensitivePath(path) {
		return errors.New("index contains a sensitive path")
	}
	if chunk.Start < 0 || chunk.End <= chunk.Start || chunk.End-chunk.Start > chunkRunes ||
		strings.TrimSpace(chunk.Content) == "" || len(chunk.Content) > maxFileBytes || !utf8.ValidString(chunk.Content) {
		return errors.New("index contains invalid chunk bounds or content")
	}
	if (chunk.LineStart == 0) != (chunk.LineEnd == 0) ||
		(chunk.LineStart != 0 && (chunk.LineStart < 1 || chunk.LineEnd < chunk.LineStart)) {
		return errors.New("index contains an invalid line range")
	}
	if !validIndexDigest(chunk.ID) || !validIndexDigest(chunk.Digest) || safeio.SHA256([]byte(chunk.Content)) != chunk.Digest {
		return errors.New("index chunk digest is invalid")
	}
	if securitycheck.ContainsHighConfidenceSecret(chunk.Content) {
		return errors.New("index contains high-confidence secret material")
	}
	return nil
}

func validIndexDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validIndexProjectID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

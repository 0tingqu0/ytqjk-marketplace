package rag

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const MaxMemoryChars = 24000

type Anchor struct {
	SchemaVersion      int     `json:"schema_version"`
	SessionKey         string  `json:"session_key"`
	ProjectID          string  `json:"project_id"`
	CreatedAt          string  `json:"created_at"`
	LastActivityAt     string  `json:"last_activity_at"`
	ArchivedAt         *string `json:"archived_at"`
	ArchivePreparedAt  *string `json:"archive_prepared_at"`
	Memory             string  `json:"memory"`
	ExportedMemoryHash string  `json:"exported_memory_hash"`
}

func SessionKey(sessionID string) string {
	digest := sha256.Sum256([]byte(sessionID))
	return hex.EncodeToString(digest[:])[:24]
}

func EnsureAnchor(root, sessionID, projectID string) (Anchor, bool, error) {
	if err := validateSessionID(sessionID); err != nil {
		return Anchor{}, false, err
	}
	if err := requireTracked(root, projectID); err != nil {
		return Anchor{}, false, err
	}
	path := anchorPath(root, sessionID)
	existing, found, err := readAnchor(path)
	if err != nil {
		return Anchor{}, false, err
	}
	if found && existing.ProjectID != projectID {
		return Anchor{}, false, errors.New("会话已绑定其他项目，禁止访问其他项目子库")
	}
	if found && (existing.ArchivedAt != nil || existing.ArchivePreparedAt != nil) {
		return Anchor{}, false, errors.New("会话已归档或正在等待归档")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	anchor := existing
	if !found {
		anchor = Anchor{SchemaVersion: 1, SessionKey: SessionKey(sessionID), ProjectID: projectID, CreatedAt: now}
	}
	anchor.LastActivityAt = now
	if err := safeio.WriteJSON(path, anchor); err != nil {
		return Anchor{}, false, err
	}
	return anchor, !found, nil
}

func Checkpoint(root, sessionID, projectID, memory string) (Anchor, error) {
	if err := validateMemory(memory); err != nil {
		return Anchor{}, err
	}
	anchor, _, err := EnsureAnchor(root, sessionID, projectID)
	if err != nil {
		return Anchor{}, err
	}
	anchor.Memory = strings.TrimSpace(memory)
	anchor.ArchivePreparedAt = nil
	anchor.LastActivityAt = time.Now().UTC().Format(time.RFC3339Nano)
	err = safeio.WriteJSON(anchorPath(root, sessionID), anchor)
	return anchor, err
}

func PrepareArchive(root, sessionID string) (Anchor, error) {
	path := anchorPath(root, sessionID)
	anchor, found, err := readAnchor(path)
	if err != nil || !found {
		return Anchor{}, errors.New("未找到会话锚点")
	}
	if strings.TrimSpace(anchor.Memory) == "" {
		return Anchor{}, errors.New("归档前必须先保存会话摘要")
	}
	if anchor.ArchivedAt != nil {
		return Anchor{}, errors.New("会话已归档")
	}
	if anchor.ArchivePreparedAt == nil {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		anchor.ArchivePreparedAt = &now
	}
	err = safeio.WriteJSON(path, anchor)
	return anchor, err
}

func FinalizeArchive(root, sessionID string) (Anchor, error) {
	path := anchorPath(root, sessionID)
	anchor, found, err := readAnchor(path)
	if err != nil || !found {
		return Anchor{}, errors.New("未找到会话锚点")
	}
	if anchor.ArchivedAt != nil {
		return anchor, nil
	}
	if anchor.ArchivePreparedAt == nil {
		return Anchor{}, errors.New("会话尚未进入待归档状态")
	}
	if anchor.Memory != "" {
		digest := safeio.SHA256([]byte(anchor.Memory))
		if digest != anchor.ExportedMemoryHash {
			if err := writeExperience(root, anchor); err != nil {
				return Anchor{}, err
			}
			anchor.ExportedMemoryHash = digest
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	anchor.ArchivedAt = &now
	anchor.ArchivePreparedAt = nil
	err = safeio.WriteJSON(path, anchor)
	return anchor, err
}

func InspectAnchor(root, sessionID, projectID string) (map[string]any, error) {
	anchor, found, err := readAnchor(anchorPath(root, sessionID))
	if err != nil {
		return nil, err
	}
	if !found {
		return map[string]any{"state": "ABSENT", "session_key": SessionKey(sessionID)}, nil
	}
	if anchor.ProjectID != projectID {
		return nil, errors.New("会话已绑定其他项目")
	}
	state := "ACTIVE"
	if anchor.ArchivedAt != nil {
		state = "ARCHIVED"
	} else if anchor.ArchivePreparedAt != nil {
		state = "ARCHIVE_PREPARED"
	}
	return map[string]any{"state": state, "session_key": anchor.SessionKey, "project_id": anchor.ProjectID, "has_memory": strings.TrimSpace(anchor.Memory) != ""}, nil
}

func anchorPath(root, sessionID string) string {
	return filepath.Join(root, "sessions", SessionKey(sessionID), "anchor.json")
}

func readAnchor(path string) (Anchor, bool, error) {
	var anchor Anchor
	err := safeio.ReadJSON(path, &anchor)
	if errors.Is(err, os.ErrNotExist) {
		return Anchor{}, false, nil
	}
	if err != nil {
		return Anchor{}, false, errors.New("会话锚点已损坏，已拒绝覆盖")
	}
	return anchor, true, nil
}

func requireTracked(root, projectID string) error {
	var catalog Catalog
	if err := safeio.ReadJSON(filepath.Join(root, "catalog.json"), &catalog); err != nil {
		return errors.New("PROJECT_REMOVED")
	}
	if _, ok := catalog.Projects[projectID]; !ok {
		return errors.New("PROJECT_REMOVED")
	}
	return nil
}

func validateSessionID(value string) error {
	if strings.TrimSpace(value) == "" || len(value) > 512 || strings.ContainsRune(value, 0) {
		return errors.New("会话标识无效")
	}
	return nil
}

func validateMemory(value string) error {
	if strings.TrimSpace(value) == "" || len([]rune(value)) > MaxMemoryChars || strings.ContainsRune(value, 0) {
		return errors.New("会话摘要必须非空且不超过 24000 字符")
	}
	lower := strings.ToLower(value)
	for _, marker := range []string{"-----begin private key-----", "sk-proj-", "ghp_", "authorization: bearer "} {
		if strings.Contains(lower, marker) {
			return errors.New("会话摘要可能包含敏感信息，未保存")
		}
	}
	return nil
}

func writeExperience(root string, anchor Anchor) error {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	path := filepath.Join(root, "personal-experience", "candidates", stamp+"-session-"+anchor.SessionKey+".md")
	content := "---\nstatus: CANDIDATE\nsource: session-anchor\nsession_key: " + anchor.SessionKey +
		"\nproject_id: " + anchor.ProjectID + "\narchived_at: " + time.Now().UTC().Format(time.RFC3339Nano) +
		"\n---\n\n# 会话经验\n\n" + strings.TrimSpace(anchor.Memory) + "\n"
	return safeio.AtomicWrite(path, []byte(content), 0o600)
}

package dashboard

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/buildinfo"
	"github.com/0tingqu0/ytqjk-marketplace/internal/rag"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const maxSnapshotDocumentBytes = 128 * 1024 * 1024

type documentSection struct {
	path  string
	label string
	state string
}

var dashboardDocumentSections = []documentSection{
	{path: "verified", label: "已验证", state: "verified"},
	{path: "personal-experience/approved", label: "个人经验", state: "approved"},
	{path: "error-experience/approved", label: "错误经验", state: "approved"},
	{path: "personal-experience/candidates", label: "个人候选", state: "candidate"},
	{path: "error-experience/candidates", label: "错误候选", state: "candidate"},
}

func (s *Server) writeSnapshot(writer http.ResponseWriter) int {
	documents := snapshotDocuments(s.KnowledgeRoot)
	sessions := snapshotSessions(s.KnowledgeRoot)
	projects := snapshotProjects(s.KnowledgeRoot)
	global := readObject(filepath.Join(s.KnowledgeRoot, "global-cache", "manifest.json"))
	stats, _ := global["stats"].(map[string]any)
	counts := map[string]int{"verified": 0, "approved": 0, "candidate": 0, "sessions": len(sessions)}
	for _, document := range documents {
		if state, ok := document["state"].(string); ok {
			counts[state]++
		}
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "version": buildinfo.Version,
		"generated_at": time.Now().UTC().Format(time.RFC3339Nano),
		"root":         s.KnowledgeRoot,
		"config":       map[string]any{},
		"global":       global,
		"global_library": map[string]any{
			"path": s.KnowledgeRoot, "indexed_at": global["indexed_at"],
			"files": numberFromMap(stats, "files"), "chunks": numberFromMap(stats, "chunks"),
			"verified": counts["verified"], "approved": counts["approved"], "candidate": counts["candidate"],
		},
		"projects": projects, "sessions": sessions, "documents": documents, "counts": counts,
	})
	return http.StatusOK
}

func snapshotDocuments(root string) []map[string]any {
	result := make([]map[string]any, 0)
	total := int64(0)
	for _, section := range dashboardDocumentSections {
		directory := filepath.Join(root, filepath.FromSlash(section.path))
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		_ = filepath.WalkDir(directory, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if path == directory {
				return nil
			}
			if entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
				return nil
			}
			relative, err := filepath.Rel(root, path)
			if err != nil {
				return nil
			}
			relative = filepath.ToSlash(relative)
			if section.state == "candidate" {
				if _, err := candidateRelative(relative); err != nil {
					return nil
				}
			} else if _, err := markdownRelative(relative); err != nil {
				return nil
			}
			snapshot, err := readStableRelative(root, relative, maxCandidateBytes)
			if err != nil || bytes.IndexByte(snapshot.content, 0) >= 0 || !utf8.Valid(snapshot.content) || containsSecret(string(snapshot.content)) || total+int64(len(snapshot.content)) > maxSnapshotDocumentBytes {
				return nil
			}
			total += int64(len(snapshot.content))
			result = append(result, map[string]any{
				"path": relative, "label": section.label, "state": section.state,
				"bytes": len(snapshot.content), "modified": float64(snapshot.info.ModTime().UnixNano()) / 1e9,
			})
			return nil
		})
	}
	sort.Slice(result, func(i, j int) bool {
		leftState, rightState := result[i]["state"].(string), result[j]["state"].(string)
		if leftState == rightState {
			return result[i]["path"].(string) < result[j]["path"].(string)
		}
		return stateOrder(leftState) < stateOrder(rightState)
	})
	return result
}

func snapshotSessions(root string) []map[string]any {
	directory := filepath.Join(root, "sessions")
	entries, err := os.ReadDir(directory)
	if err != nil {
		return []map[string]any{}
	}
	result := make([]map[string]any, 0)
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		relative := filepath.ToSlash(filepath.Join("sessions", entry.Name(), "anchor.json"))
		snapshot, err := readStableRelative(root, relative, 1024*1024)
		if err != nil {
			continue
		}
		var anchor struct {
			SessionKey     string          `json:"session_key"`
			ProjectID      string          `json:"project_id"`
			CreatedAt      string          `json:"created_at"`
			LastActivityAt string          `json:"last_activity_at"`
			ArchivedAt     *string         `json:"archived_at"`
			Memory         json.RawMessage `json:"memory"`
		}
		if json.Unmarshal(snapshot.content, &anchor) != nil || anchor.SessionKey == "" || anchor.ProjectID == "" {
			continue
		}
		key := anchor.SessionKey
		if len(key) > 12 {
			key = key[:12]
		}
		result = append(result, map[string]any{
			"key": key, "project": anchor.ProjectID, "created_at": anchor.CreatedAt,
			"last_activity_at": anchor.LastActivityAt, "archived_at": anchor.ArchivedAt,
			"has_memory": hasJSONValue(anchor.Memory),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i]["last_activity_at"].(string) > result[j]["last_activity_at"].(string)
	})
	return result
}

func snapshotProjects(root string) []map[string]any {
	var catalog rag.Catalog
	_ = safeio.ReadJSON(filepath.Join(root, "catalog.json"), &catalog)
	identifiers := map[string]bool{}
	for identifier := range catalog.Projects {
		if safeIdentifier(identifier) {
			identifiers[identifier] = true
		}
	}
	projectRoot := filepath.Join(root, "projects")
	entries, _ := os.ReadDir(projectRoot)
	for _, entry := range entries {
		if entry.IsDir() && entry.Type()&os.ModeSymlink == 0 && safeIdentifier(entry.Name()) {
			identifiers[entry.Name()] = true
		}
	}
	ordered := make([]string, 0, len(identifiers))
	for identifier := range identifiers {
		ordered = append(ordered, identifier)
	}
	sort.Strings(ordered)
	result := make([]map[string]any, 0, len(ordered))
	for _, identifier := range ordered {
		var manifest rag.Manifest
		_ = safeio.ReadJSON(filepath.Join(projectRoot, identifier, "manifest.json"), &manifest)
		catalogRow := catalog.Projects[identifier]
		name := manifest.Identity.Name
		if name == "" {
			name = catalogRow.Name
		}
		if name == "" {
			name = identifier
		}
		tracking := catalogRow.TrackingState
		if tracking == "" && manifest.IndexedAt != "" {
			tracking = "INDEXED"
		} else if tracking == "" {
			tracking = "REGISTERED"
		}
		vector := "NOT_BUILT"
		if status, ok := manifest.Vector["status"].(string); ok && status != "" {
			vector = status
		}
		result = append(result, map[string]any{
			"id": identifier, "name": name, "remote": catalogRow.Remote,
			"head": "未索引", "dirty": "unknown", "indexed_at": nullableString(manifest.IndexedAt),
			"files": manifest.Stats.Files, "chunks": manifest.Stats.Chunks, "text_bytes": manifest.Stats.TextBytes,
			"vector": vector, "tracking": tracking,
			"cache": map[string]any{"entries": 0, "used_bytes": 0, "capacity_bytes": 1024 * 1024 * 1024, "policy": "LFU_LRU"},
		})
	}
	return result
}

func readObject(path string) map[string]any {
	result := map[string]any{}
	if err := safeio.ReadJSON(path, &result); err != nil {
		return map[string]any{}
	}
	return result
}

func numberFromMap(value map[string]any, key string) int {
	if number, ok := value[key].(float64); ok && number >= 0 {
		return int(number)
	}
	return 0
}

func hasJSONValue(value json.RawMessage) bool {
	trimmed := bytes.TrimSpace(value)
	if len(trimmed) == 0 {
		return false
	}
	var decoded any
	if json.Unmarshal(trimmed, &decoded) != nil {
		return false
	}
	switch current := decoded.(type) {
	case nil:
		return false
	case bool:
		return current
	case string:
		return current != ""
	case float64:
		return current != 0
	case []any:
		return len(current) > 0
	case map[string]any:
		return len(current) > 0
	default:
		return true
	}
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func stateOrder(value string) int {
	switch value {
	case "verified":
		return 0
	case "approved":
		return 1
	case "candidate":
		return 2
	default:
		return 3
	}
}

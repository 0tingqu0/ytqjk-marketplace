package importer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/0tingqu0/ytqjk-marketplace/internal/knowledge"
	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const maxCandidateBytes = 10 * 1024 * 1024

type Receipt struct {
	Status             string `json:"status"`
	DiscoveredCount    int    `json:"discovered_count"`
	ImportedCount      int    `json:"imported_count"`
	DeduplicatedCount  int    `json:"deduplicated_count"`
	ProvenanceAdded    int    `json:"provenance_added"`
	NotConfiguredCount int    `json:"not_configured_count"`
	ParseFailedCount   int    `json:"parse_failed_count"`
	FailureStage       any    `json:"failure_stage"`
	FailureCode        any    `json:"failure_code"`
}

func Empty(status string) Receipt { return Receipt{Status: status} }

func Import(codexRoot, knowledgeRoot, mode string) (Receipt, error) {
	if mode == "off" {
		return Empty("SKIPPED_OFF"), nil
	}
	info, err := os.Stat(codexRoot)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.IsDir()) {
		return Empty("SKIPPED_ABSENT"), nil
	}
	if err != nil {
		return Empty("FAILED"), err
	}
	paths, notConfigured, err := discover(codexRoot)
	if err != nil {
		return Empty("FAILED"), err
	}
	receipt := Receipt{Status: "SUCCEEDED", DiscoveredCount: len(paths), NotConfiguredCount: notConfigured}
	var candidates []knowledge.CandidateImport
	for _, path := range paths {
		data, readErr := os.ReadFile(path)
		if readErr != nil || len(data) > maxCandidateBytes || !utf8.Valid(data) || strings.IndexByte(string(data), 0) >= 0 {
			if mode == "force" {
				return Empty("FAILED"), errors.New("candidate parse failed")
			}
			receipt.ParseFailedCount++
			continue
		}
		if containsSecret(string(data)) {
			return Empty("FAILED"), errors.New("candidate contains high-confidence secret")
		}
		relative, _ := filepath.Rel(codexRoot, path)
		digest := sha256.Sum256(data)
		refDigest := sha256.Sum256([]byte(filepath.ToSlash(relative)))
		candidates = append(candidates, knowledge.CandidateImport{
			Title: filepath.Base(path), Content: string(data), SourceKind: "codex-bootstrap",
			SourceRef: "source-" + hex.EncodeToString(refDigest[:])[:16], SourceSHA: hex.EncodeToString(digest[:]),
		})
	}
	database := filepath.Join(knowledgeRoot, "service", "knowledge.sqlite3")
	service, err := knowledge.Open(database)
	if err != nil {
		return Empty("FAILED"), err
	}
	defer service.Close()
	rootDigest := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(codexRoot))))
	marker := "codex-bootstrap:" + hex.EncodeToString(rootDigest[:])
	if mode != "force" {
		if prior, found, receiptErr := service.ImportReceipt(marker); receiptErr != nil {
			return Empty("FAILED"), receiptErr
		} else if found {
			receipt.Status = "SKIPPED_MARKER"
			receipt.ImportedCount = prior.CreatedDocuments
			receipt.DeduplicatedCount = prior.DeduplicatedDocuments
			return receipt, nil
		}
	}
	result, err := service.ImportCandidates("global", "global-candidates", marker, candidates, mode == "force")
	if err != nil {
		return Empty("FAILED"), err
	}
	receipt.ImportedCount = result.CreatedDocuments
	receipt.DeduplicatedCount = result.DeduplicatedDocuments
	receipt.ProvenanceAdded = result.ProvenanceAdded
	if receipt.ParseFailedCount > 0 {
		receipt.Status = "SUCCEEDED_WITH_WARNINGS"
		receipt.FailureStage = "PARSING"
		receipt.FailureCode = "PARSE_FAILED"
	}
	return receipt, nil
}

func discover(root string) ([]string, int, error) {
	var candidates []string
	if path := filepath.Join(root, "mem.md"); regular(path) {
		candidates = append(candidates, path)
	}
	notConfigured := 0
	for _, name := range []string{"memories", "knowledge", "attachments"} {
		directory := filepath.Join(root, name)
		if _, err := os.Stat(directory); errors.Is(err, os.ErrNotExist) {
			continue
		}
		err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, _ := filepath.Rel(root, path)
			if excluded(relative) || entry.Type()&os.ModeSymlink != 0 {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			extension := strings.ToLower(filepath.Ext(path))
			switch extension {
			case "", ".md", ".txt", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".csv", ".tsv":
				candidates = append(candidates, path)
			case ".doc", ".docx", ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".wav", ".mp3", ".m4a":
				notConfigured++
			}
			return nil
		})
		if err != nil {
			return nil, notConfigured, err
		}
	}
	sort.Strings(candidates)
	return candidates, notConfigured, nil
}

func excluded(relative string) bool {
	parts := strings.FieldsFunc(strings.ToLower(filepath.ToSlash(relative)), func(r rune) bool {
		return r == '/' || r == '\\' || r == '.' || r == '-' || r == '_'
	})
	for _, part := range parts {
		switch part {
		case "credential", "credentials", "token", "tokens", "secret", "secrets", "auth", "config", "session", "sessions", "log", "logs", "cache", "plugin", "plugins", "skill", "skills", "worktree", "worktrees", "archive", "archives":
			return true
		}
	}
	return false
}

func containsSecret(value string) bool {
	lower := strings.ToLower(value)
	for _, marker := range []string{"-----begin private key-----", "authorization: bearer ", "sk-proj-", "ghp_", "xoxb-"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func regular(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func MarkerFor(root string) string {
	digest := safeio.SHA256([]byte(strings.ToLower(filepath.Clean(root))))
	return fmt.Sprintf("codex-bootstrap:%s", digest)
}

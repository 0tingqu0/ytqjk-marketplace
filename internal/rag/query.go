package rag

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

var queryTokenPattern = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

type QueryResult struct {
	ID      string  `json:"id"`
	Path    string  `json:"path"`
	Start   int     `json:"start"`
	End     int     `json:"end"`
	Content string  `json:"content"`
	Score   float64 `json:"score"`
	Scope   string  `json:"scope"`
}

type Receipt struct {
	OK              bool           `json:"ok"`
	Status          string         `json:"status"`
	Scope           string         `json:"scope"`
	ProjectID       string         `json:"project_id"`
	ProjectTracking string         `json:"project_tracking"`
	KnowledgeRoot   string         `json:"knowledge_root"`
	IndexedAt       any            `json:"indexed_at"`
	Stale           bool           `json:"stale"`
	ResultCount     int            `json:"result_count"`
	Results         []QueryResult  `json:"results"`
	AnchorKey       string         `json:"anchor_key"`
	AnchorCreated   bool           `json:"anchor_created"`
	Cache           map[string]any `json:"cache"`
	NextAction      string         `json:"next_action,omitempty"`
	IntakeInterface string         `json:"intake_interface,omitempty"`
}

func Query(knowledgeRoot, projectRoot, query, sessionID, expectedProjectID string, limit int) (Receipt, error) {
	if strings.TrimSpace(query) == "" {
		return Receipt{}, errors.New("知识检索问题不能为空")
	}
	if strings.TrimSpace(sessionID) == "" {
		return Receipt{}, errors.New("宿主未提供稳定会话标识，无法创建会话锚点")
	}
	identity, err := TrackProject(knowledgeRoot, projectRoot)
	if err != nil {
		return Receipt{}, err
	}
	if expectedProjectID != "" && identity.ID != expectedProjectID {
		return Receipt{}, errors.New("请求方项目标识与工作目录不匹配")
	}
	anchor, created, err := EnsureAnchor(knowledgeRoot, sessionID, identity.ID)
	if err != nil {
		return Receipt{}, err
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 20 {
		limit = 20
	}
	projectDirectory := filepath.Join(knowledgeRoot, "projects", identity.ID)
	projectIndex := filepath.Join(projectDirectory, "index.json")
	if _, err := os.Stat(projectIndex); errors.Is(err, os.ErrNotExist) {
		if _, buildErr := Build(knowledgeRoot, projectRoot, "auto"); buildErr != nil {
			return Receipt{}, buildErr
		}
	}
	results, manifest, err := queryIndex(projectIndex, filepath.Join(projectDirectory, "manifest.json"), query, limit, "current-project-cache-only")
	if err != nil {
		return Receipt{}, err
	}
	status, scope := "PROJECT_CACHE_HIT", "current-project-cache-only"
	if len(results) == 0 {
		results, _, err = queryIndex(filepath.Join(knowledgeRoot, "global-cache", "index.json"), filepath.Join(knowledgeRoot, "global-cache", "manifest.json"), query, limit, "global-fallback")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return Receipt{}, err
		}
		if len(results) > 0 {
			status, scope = "GLOBAL_FALLBACK_HIT", "global-fallback"
		} else {
			status, scope = "KNOWLEDGE_MISS", "no-knowledge"
		}
	}
	stale := true
	if manifest.IndexedIdentity != nil {
		current := QueryState(identity.Root)
		stale = current["head"] != manifest.IndexedIdentity["head"] || current["dirty"] != manifest.IndexedIdentity["dirty"]
	}
	receipt := Receipt{
		OK: true, Status: status, Scope: scope, ProjectID: identity.ID, ProjectTracking: "REGISTERED",
		KnowledgeRoot: knowledgeRoot, IndexedAt: manifest.IndexedAt, Stale: stale,
		ResultCount: len(results), Results: results, AnchorKey: anchor.SessionKey, AnchorCreated: created,
		Cache: map[string]any{"state": "READY", "entries": len(results), "generation": manifest.SourceFingerprint},
	}
	if status == "KNOWLEDGE_MISS" {
		receipt.NextAction = "SEARCH_EXTERNAL_THEN_SUBMIT_CANDIDATE"
		receipt.IntakeInterface = "ytqjk knowledge intake"
	}
	return receipt, nil
}

func queryIndex(indexPath, manifestPath, query string, limit int, scope string) ([]QueryResult, Manifest, error) {
	var index Index
	if err := safeio.ReadJSON(indexPath, &index); err != nil {
		return nil, Manifest{}, err
	}
	var manifest Manifest
	_ = safeio.ReadJSON(manifestPath, &manifest)
	queryTerms := termFrequency(query)
	var results []QueryResult
	for _, chunk := range index.Chunks {
		score := scoreTerms(queryTerms, termFrequency(chunk.Path+" "+chunk.Content))
		if score <= 0 {
			continue
		}
		results = append(results, QueryResult{ID: chunk.ID, Path: chunk.Path, Start: chunk.Start, End: chunk.End, Content: chunk.Content, Score: score, Scope: scope})
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].ID < results[j].ID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, manifest, nil
}

func termFrequency(value string) map[string]int {
	result := map[string]int{}
	for _, token := range queryTokenPattern.FindAllString(strings.ToLower(value), -1) {
		result[token]++
	}
	return result
}

func scoreTerms(query, document map[string]int) float64 {
	if len(query) == 0 {
		return 0
	}
	matched, score := 0, 0.0
	for term, count := range query {
		frequency := document[term]
		if frequency == 0 {
			continue
		}
		matched++
		score += float64(count) * (1 + math.Log(float64(frequency)))
	}
	coverage := float64(matched) / float64(len(query))
	return math.Round(score*coverage*1e6) / 1e6
}

func BootstrapReceipt(result BootstrapResult) map[string]any {
	return map[string]any{
		"status": "SUCCEEDED", "project_state": result.Project.State, "project_files": result.Project.Stats.Files,
		"global_state": result.Global.State, "global_files": result.Global.Stats.Files, "vector_mode": result.VectorMode,
		"failure_stage": nil, "failure_code": nil,
	}
}

func EmptyBootstrapReceipt(status string) map[string]any {
	return map[string]any{
		"status": status, "project_state": nil, "project_files": 0, "global_state": nil,
		"global_files": 0, "vector_mode": nil, "failure_stage": nil, "failure_code": nil,
	}
}

func nowText() string { return time.Now().UTC().Format(time.RFC3339Nano) }

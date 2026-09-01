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
	ID           string  `json:"id"`
	Path         string  `json:"path"`
	Start        int     `json:"start"`
	End          int     `json:"end"`
	LineStart    int     `json:"line_start,omitempty"`
	LineEnd      int     `json:"line_end,omitempty"`
	Content      string  `json:"content"`
	Score        float64 `json:"score"`
	LexicalScore float64 `json:"lexical_score"`
	VectorScore  float64 `json:"vector_score"`
	Mode         string  `json:"mode"`
	Scope        string  `json:"scope"`
	Digest       string  `json:"-"`
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
	indexedAt := manifest.IndexedAt
	var globalManifest Manifest
	_ = safeio.ReadJSON(filepath.Join(knowledgeRoot, "global-cache", "manifest.json"), &globalManifest)
	cacheStats := emptyPrefetchStats(projectDirectory)
	if len(results) == 0 {
		prefetched, currentStats, prefetchErr := QueryPrefetch(
			projectDirectory, knowledgeRoot, query, globalManifest.SourceFingerprint, limit,
		)
		if prefetchErr == nil {
			cacheStats = currentStats
		}
		if len(prefetched) > 0 {
			results = prefetched
			status, scope, indexedAt = "PROJECT_CACHE_HIT", "project-prefetch-cache", globalManifest.IndexedAt
		} else {
			results, _, err = queryIndex(filepath.Join(knowledgeRoot, "global-cache", "index.json"), filepath.Join(knowledgeRoot, "global-cache", "manifest.json"), query, limit, "global-fallback")
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return Receipt{}, err
			}
			if len(results) > 0 {
				status, scope, indexedAt = "GLOBAL_FALLBACK_HIT", "global-fallback", globalManifest.IndexedAt
				if updated, updateErr := UpdatePrefetch(
					projectDirectory, knowledgeRoot, query, globalManifest.SourceFingerprint, results,
				); updateErr == nil {
					cacheStats = updated
				}
			} else {
				status, scope = "KNOWLEDGE_MISS", "no-knowledge"
			}
		}
	} else if _, currentStats, cacheErr := ListPrefetch(projectDirectory, knowledgeRoot, 1); cacheErr == nil {
		cacheStats = currentStats
	}
	stale := true
	if manifest.IndexedIdentity != nil {
		current := QueryState(identity.Root)
		stale = current["head"] != manifest.IndexedIdentity["head"] || current["dirty"] != manifest.IndexedIdentity["dirty"]
	}
	receipt := Receipt{
		OK: true, Status: status, Scope: scope, ProjectID: identity.ID, ProjectTracking: "REGISTERED",
		KnowledgeRoot: knowledgeRoot, IndexedAt: indexedAt, Stale: stale,
		ResultCount: len(results), Results: results, AnchorKey: anchor.SessionKey, AnchorCreated: created,
		Cache: prefetchStatsMap(cacheStats, globalManifest.SourceFingerprint),
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
	if err := validateIndexForQuery(index); err != nil {
		return nil, Manifest{}, err
	}
	manifest, err := readQueryManifest(manifestPath, index.ProjectID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, Manifest{}, err
	}
	queryTerms := termFrequency(query)
	queryVector := vectorize(query)
	vectors, vectorReady := readVectors(filepath.Dir(indexPath), manifest.SourceFingerprint)
	mode := "LEXICAL"
	if vectorReady && len(queryVector) > 0 {
		mode = "HYBRID"
	}
	var results []QueryResult
	maxLexical := 0.0
	for _, chunk := range index.Chunks {
		if candidatePath(chunk.Path) {
			continue
		}
		lexical := scoreTerms(queryTerms, termFrequency(chunk.Path+" "+chunk.Content))
		vectorScore := 0.0
		if vectorReady {
			vectorScore = cosine(queryVector, vectors[chunk.ID])
		}
		if lexical <= 0 && vectorScore <= 0 {
			continue
		}
		if lexical > maxLexical {
			maxLexical = lexical
		}
		results = append(results, QueryResult{
			ID: chunk.ID, Path: chunk.Path, Start: chunk.Start, End: chunk.End,
			LineStart: chunk.LineStart, LineEnd: chunk.LineEnd, Content: chunk.Content,
			LexicalScore: lexical, VectorScore: vectorScore, Mode: mode, Scope: scope, Digest: chunk.Digest,
		})
	}
	for index := range results {
		lexical := 0.0
		if maxLexical > 0 {
			lexical = results[index].LexicalScore / maxLexical
		}
		if vectorReady {
			results[index].Score = math.Round((0.6*lexical+0.4*results[index].VectorScore)*1e6) / 1e6
		} else {
			results[index].Score = math.Round(lexical*1e6) / 1e6
		}
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

// SearchIndex exposes the governed read-only index query used by the local
// dashboard and authenticated peer server. Callers remain responsible for
// choosing an index that is inside an already-authorized scope.
func SearchIndex(indexPath, query string, limit int, scope string) ([]QueryResult, Manifest, error) {
	if strings.TrimSpace(query) == "" {
		return nil, Manifest{}, errors.New("query text is required")
	}
	if limit < 1 || limit > 20 {
		return nil, Manifest{}, errors.New("query limit must be 1..20")
	}
	return queryIndex(indexPath, filepath.Join(filepath.Dir(indexPath), "manifest.json"), query, limit, scope)
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

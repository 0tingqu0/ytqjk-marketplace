package knowledge

import (
	"database/sql"
	"math"
	"regexp"
	"sort"
	"strings"
)

var tokenPattern = regexp.MustCompile(`[\p{L}\p{N}_-]+`)

type SearchResult struct {
	DocumentID string  `json:"document_id"`
	VersionID  int64   `json:"version_id"`
	Title      string  `json:"title"`
	Content    string  `json:"content"`
	Source     string  `json:"source"`
	State      string  `json:"state"`
	Score      float64 `json:"score"`
}

func (s *Service) Search(projectID, query string, limit int) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return []SearchResult{}, nil
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.database.Query(`SELECT d.id, v.id, d.title, c.content, COALESCE(src.locator,''), v.state
FROM documents d JOIN versions v ON v.document_id=d.id JOIN chunks c ON c.version_id=v.id
LEFT JOIN sources src ON src.version_id=v.id
WHERE d.project_id=? AND d.deleted_at IS NULL
AND v.ordinal=(SELECT MAX(latest.ordinal) FROM versions latest WHERE latest.document_id=d.id)
AND v.state!='tombstone' ORDER BY d.id, c.ordinal`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	queryTokens := frequencies(query)
	var results []SearchResult
	for rows.Next() {
		var item SearchResult
		if err := rows.Scan(&item.DocumentID, &item.VersionID, &item.Title, &item.Content, &item.Source, &item.State); err != nil {
			return nil, err
		}
		item.Score = lexicalScore(queryTokens, frequencies(item.Title+" "+item.Content))
		if item.Score > 0 {
			results = append(results, item)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].DocumentID < results[j].DocumentID
		}
		return results[i].Score > results[j].Score
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

func (s *Service) DocumentContent(documentID string) (title, content, state string, err error) {
	err = s.database.QueryRow(`SELECT d.title, CAST(o.content AS TEXT), v.state
FROM documents d JOIN versions v ON v.document_id=d.id JOIN originals o ON o.sha256=v.original_sha256
WHERE d.id=? ORDER BY v.ordinal DESC LIMIT 1`, documentID).Scan(&title, &content, &state)
	return
}

func (s *Service) FeedbackStatus(documentID string) (map[string]any, error) {
	var invocation, state, created string
	var correct bool
	var score int
	err := s.database.QueryRow(`SELECT invocation_id, correct, score, state, created_at
FROM feedback_events WHERE document_id=? ORDER BY id DESC LIMIT 1`, documentID).
		Scan(&invocation, &correct, &score, &state, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"invocation_id": invocation, "correct": correct, "score": score, "state": state, "created_at": created}, nil
}

func frequencies(value string) map[string]int {
	result := map[string]int{}
	for _, token := range tokenPattern.FindAllString(strings.ToLower(value), -1) {
		result[token]++
	}
	return result
}

func lexicalScore(query, document map[string]int) float64 {
	if len(query) == 0 || len(document) == 0 {
		return 0
	}
	score := 0.0
	matched := 0
	for token, queryFrequency := range query {
		if frequency := document[token]; frequency > 0 {
			matched++
			score += (1 + math.Log(float64(frequency))) * float64(queryFrequency)
		}
	}
	coverage := float64(matched) / float64(len(query))
	return math.Round((score*coverage)*1e6) / 1e6
}

package dashboard

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"
)

type semanticSearchRequest struct {
	Query string `json:"query"`
	Limit *int   `json:"limit,omitempty"`
}

type semanticSearchResult struct {
	NodeID       string   `json:"node_id"`
	Title        string   `json:"title"`
	Path         string   `json:"path"`
	Scope        string   `json:"scope"`
	ProjectID    string   `json:"project_id,omitempty"`
	LineStart    int      `json:"line_start"`
	LineEnd      int      `json:"line_end"`
	Snippet      string   `json:"snippet"`
	Score        float64  `json:"score"`
	MatchedTerms []string `json:"matched_terms"`
}

func (s *Server) semanticSearchHTTP(writer http.ResponseWriter, request *http.Request) int {
	var payload semanticSearchRequest
	if err := readJSON(request, &payload); err != nil {
		return writeGraphRequestError(writer, "INVALID_REQUEST_FIELDS")
	}
	query := strings.TrimSpace(payload.Query)
	if query == "" {
		return writeGraphRequestError(writer, "EMPTY_QUERY")
	}
	if utf8.RuneCountInString(query) > 2000 {
		return writeGraphRequestError(writer, "QUERY_TOO_LONG")
	}
	limit := 8
	if payload.Limit != nil {
		limit = *payload.Limit
	}
	if limit < 1 || limit > 20 {
		return writeGraphRequestError(writer, "INVALID_LIMIT")
	}
	sources, _, _ := loadGraphSources(s.KnowledgeRoot)
	documents := groupGraphDocuments(sources)
	rawResults, err := searchAll(s.KnowledgeRoot, query, 20)
	if err != nil {
		writeError(writer, http.StatusServiceUnavailable, "GRAPH_UNAVAILABLE", "知识检索暂时不可用，请稍后重试。")
		return http.StatusServiceUnavailable
	}
	retrievalScores := map[string]float64{}
	vectorScores := map[string]float64{}
	for _, result := range rawResults {
		identifier := graphDocumentID(result.Scope, result.Path)
		if result.Score > retrievalScores[identifier] {
			retrievalScores[identifier] = result.Score
		}
		if result.VectorScore > vectorScores[identifier] {
			vectorScores[identifier] = result.VectorScore
		}
	}
	queryTokens := semanticGraphTokens(query)
	terms := make(map[string]struct{}, len(queryTokens))
	for term := range queryTokens {
		terms[term] = struct{}{}
	}
	results := make([]semanticSearchResult, 0, len(documents))
	for _, document := range documents {
		concept := cosineGraphTokens(queryTokens, document.Tokens)
		matchedWeight := 0
		for token, count := range queryTokens {
			if document.Tokens[token] > 0 {
				matchedWeight += count
			}
		}
		totalWeight := 0
		for _, count := range queryTokens {
			totalWeight += count
		}
		coverage := float64(matchedWeight) / float64(max(1, totalWeight))
		titleTokens := semanticGraphTokens(document.Title)
		titleScore := cosineGraphTokens(queryTokens, titleTokens)
		titlePhrase := 0.0
		foldedQuery := strings.ToLower(query)
		for token := range titleTokens {
			if utf8.RuneCountInString(token) >= 2 && strings.Contains(foldedQuery, token) {
				titlePhrase = 1
				break
			}
		}
		exact := 0.0
		if strings.Contains(strings.ToLower(document.Content), foldedQuery) {
			exact = 1
		}
		base := math.Min(1, coverage*0.42+concept*0.14+titleScore*0.16+titlePhrase*0.28+exact*0.16)
		vectorScore := vectorScores[document.ID]
		score := base
		if vectorScore > 0 {
			score = base*0.72 + vectorScore*0.28
		}
		if lexical := retrievalScores[document.ID]; lexical > 0 {
			score = math.Max(score, math.Min(1, base*0.7+lexical*0.3))
		}
		if score <= 0 {
			continue
		}
		matched := make([]string, 0, len(terms))
		for term := range terms {
			if document.Tokens[term] > 0 {
				matched = append(matched, term)
			}
		}
		sort.Slice(matched, func(i, j int) bool {
			left, right := utf8.RuneCountInString(matched[i]), utf8.RuneCountInString(matched[j])
			if left != right {
				return left > right
			}
			return matched[i] < matched[j]
		})
		if len(matched) > 8 {
			matched = matched[:8]
		}
		snippet := graphSnippet(document.Content, terms)
		if snippet == "" {
			snippet = graphSnippet(document.Content, nil)
		}
		results = append(results, semanticSearchResult{
			NodeID: document.ID, Title: document.Title, Path: document.Path,
			Scope: document.Scope, ProjectID: document.ProjectID,
			LineStart: document.LineStart, LineEnd: document.LineEnd,
			Snippet: snippet, Score: math.Round(score*10000) / 10000,
			MatchedTerms: matched,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		if results[i].Scope != results[j].Scope {
			return results[i].Scope < results[j].Scope
		}
		return results[i].Path < results[j].Path
	})
	if len(results) > limit {
		results = results[:limit]
	}
	mode := "concept-hybrid"
	if len(vectorScores) > 0 {
		mode = "hybrid-vector"
	}
	suggestions := make([]string, 0, 2)
	if len(results) == 0 {
		suggestions = append(suggestions, "尝试实体名称", "缩短检索词")
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "query": query, "mode": mode,
		"results": results, "suggestions": suggestions,
	})
	return http.StatusOK
}

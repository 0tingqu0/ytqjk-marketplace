package dashboard

import (
	"math"
	"net/http"
	"sort"
	"strconv"
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

type graphRecommendationRequest struct {
	NodeID string `json:"node_id"`
	Limit  *int   `json:"limit,omitempty"`
}

type graphPathRequest struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	MaxDepth *int   `json:"max_depth,omitempty"`
}

func (s *Server) graphHTTP(writer http.ResponseWriter, rawLimit string) int {
	limit := 100
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 20 || parsed > 160 {
			return writeGraphRequestError(writer, "INVALID_LIMIT")
		}
		limit = parsed
	}
	graph, generatedAt := s.currentKnowledgeGraph(limit)
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "generated_at": generatedAt, "graph": graph,
	})
	return http.StatusOK
}

func (s *Server) currentKnowledgeGraph(limit int) (knowledgeGraph, string) {
	sources, signature, vectorAvailable := loadGraphSources(s.KnowledgeRoot)
	limit = clamp(limit, 20, 160)
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if s.graphCache.Signature != signature {
		s.graphCache = graphCacheEntry{
			Signature: signature, GeneratedAt: graphGeneratedAt(),
			Graphs: make(map[int]knowledgeGraph),
		}
	}
	if graph, found := s.graphCache.Graphs[limit]; found {
		return graph, s.graphCache.GeneratedAt
	}
	graph := buildKnowledgeGraph(sources, limit, vectorAvailable)
	s.graphCache.Graphs[limit] = graph
	return graph, s.graphCache.GeneratedAt
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

func (s *Server) recommendationsHTTP(writer http.ResponseWriter, request *http.Request) int {
	var payload graphRecommendationRequest
	if err := readJSON(request, &payload); err != nil {
		return writeGraphRequestError(writer, "INVALID_REQUEST_FIELDS")
	}
	if !validGraphNodeRequestID(payload.NodeID) {
		return writeGraphRequestError(writer, "INVALID_NODE_ID")
	}
	limit := 6
	if payload.Limit != nil {
		limit = *payload.Limit
	}
	if limit < 1 || limit > 20 {
		return writeGraphRequestError(writer, "INVALID_LIMIT")
	}
	graph, _ := s.currentKnowledgeGraph(160)
	nodes, adjacency := indexKnowledgeGraph(graph)
	if _, found := nodes[payload.NodeID]; !found {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "node_id": payload.NodeID, "results": []map[string]any{},
		})
		return http.StatusOK
	}
	scores := map[string]float64{}
	reasons := map[string]map[string]struct{}{}
	addReason := func(identifier, reason string) {
		if reasons[identifier] == nil {
			reasons[identifier] = map[string]struct{}{}
		}
		reasons[identifier][reason] = struct{}{}
	}
	for _, edgeIndex := range adjacency[payload.NodeID] {
		edge := graph.Edges[edgeIndex]
		neighbor := graphEdgeNeighbor(edge, payload.NodeID)
		if neighbor == payload.NodeID {
			continue
		}
		if edge.Confidence > scores[neighbor] {
			scores[neighbor] = edge.Confidence
		}
		addReason(neighbor, edge.Label)
		for _, secondIndex := range adjacency[neighbor] {
			second := graph.Edges[secondIndex]
			candidate := graphEdgeNeighbor(second, neighbor)
			if candidate == payload.NodeID {
				continue
			}
			score := edge.Confidence * second.Confidence * 0.78
			if score > scores[candidate] {
				scores[candidate] = score
			}
			via := "关联实体"
			if node, found := nodes[neighbor]; found {
				via = node.Label
			}
			addReason(candidate, "经由 "+via)
		}
	}
	identifiers := make([]string, 0, len(scores))
	for identifier := range scores {
		if _, found := nodes[identifier]; found && identifier != payload.NodeID {
			identifiers = append(identifiers, identifier)
		}
	}
	sort.Slice(identifiers, func(i, j int) bool {
		if scores[identifiers[i]] != scores[identifiers[j]] {
			return scores[identifiers[i]] > scores[identifiers[j]]
		}
		return nodes[identifiers[i]].Label < nodes[identifiers[j]].Label
	})
	if len(identifiers) > limit {
		identifiers = identifiers[:limit]
	}
	results := make([]map[string]any, 0, len(identifiers))
	for _, identifier := range identifiers {
		row := graphNodeResponse(nodes[identifier])
		row["score"] = math.Round(scores[identifier]*10000) / 10000
		rowReasons := make([]string, 0, len(reasons[identifier]))
		for reason := range reasons[identifier] {
			rowReasons = append(rowReasons, reason)
		}
		sort.Strings(rowReasons)
		if len(rowReasons) > 3 {
			rowReasons = rowReasons[:3]
		}
		row["reasons"] = rowReasons
		results = append(results, row)
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "node_id": payload.NodeID, "results": results,
	})
	return http.StatusOK
}

func (s *Server) graphPathHTTP(writer http.ResponseWriter, request *http.Request) int {
	var payload graphPathRequest
	if err := readJSON(request, &payload); err != nil {
		return writeGraphRequestError(writer, "INVALID_REQUEST_FIELDS")
	}
	if !validGraphNodeRequestID(payload.Source) || !validGraphNodeRequestID(payload.Target) {
		return writeGraphRequestError(writer, "INVALID_NODE_ID")
	}
	maxDepth := 5
	if payload.MaxDepth != nil {
		maxDepth = *payload.MaxDepth
	}
	if maxDepth < 1 || maxDepth > 6 {
		return writeGraphRequestError(writer, "INVALID_MAX_DEPTH")
	}
	graph, _ := s.currentKnowledgeGraph(160)
	nodes, adjacency := indexKnowledgeGraph(graph)
	sourceNode, sourceFound := nodes[payload.Source]
	_, targetFound := nodes[payload.Target]
	if !sourceFound || !targetFound {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "found": false, "reason": "UNKNOWN_NODE",
			"nodes": []graphNode{}, "edges": []graphEdge{},
		})
		return http.StatusOK
	}
	if payload.Source == payload.Target {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "found": true, "nodes": []graphNode{sourceNode},
			"edges": []graphEdge{}, "hops": 0,
		})
		return http.StatusOK
	}
	queue := []string{payload.Source}
	depth := map[string]int{payload.Source: 0}
	parentNode := map[string]string{}
	parentEdge := map[string]int{}
	found := false
searchLoop:
	for head := 0; head < len(queue); head++ {
		current := queue[head]
		if depth[current] >= maxDepth {
			continue
		}
		for _, edgeIndex := range adjacency[current] {
			neighbor := graphEdgeNeighbor(graph.Edges[edgeIndex], current)
			if _, visited := depth[neighbor]; visited {
				continue
			}
			depth[neighbor] = depth[current] + 1
			parentNode[neighbor] = current
			parentEdge[neighbor] = edgeIndex
			if neighbor == payload.Target {
				found = true
				break searchLoop
			}
			queue = append(queue, neighbor)
		}
	}
	if !found {
		writeJSON(writer, http.StatusOK, map[string]any{
			"ok": true, "found": false, "reason": "NO_PATH",
			"nodes": []graphNode{}, "edges": []graphEdge{},
		})
		return http.StatusOK
	}
	reversedNodes := []string{payload.Target}
	reversedEdges := make([]int, 0, depth[payload.Target])
	for current := payload.Target; current != payload.Source; {
		reversedEdges = append(reversedEdges, parentEdge[current])
		current = parentNode[current]
		reversedNodes = append(reversedNodes, current)
	}
	pathNodes := make([]graphNode, len(reversedNodes))
	for index := range reversedNodes {
		pathNodes[len(reversedNodes)-1-index] = nodes[reversedNodes[index]]
	}
	pathEdges := make([]graphEdge, len(reversedEdges))
	for index := range reversedEdges {
		pathEdges[len(reversedEdges)-1-index] = graph.Edges[reversedEdges[index]]
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "found": true, "nodes": pathNodes,
		"edges": pathEdges, "hops": len(pathEdges),
	})
	return http.StatusOK
}

func indexKnowledgeGraph(graph knowledgeGraph) (map[string]graphNode, map[string][]int) {
	nodes := make(map[string]graphNode, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	adjacency := make(map[string][]int, len(nodes))
	for index, edge := range graph.Edges {
		if _, sourceFound := nodes[edge.Source]; !sourceFound {
			continue
		}
		if _, targetFound := nodes[edge.Target]; !targetFound {
			continue
		}
		adjacency[edge.Source] = append(adjacency[edge.Source], index)
		adjacency[edge.Target] = append(adjacency[edge.Target], index)
	}
	return nodes, adjacency
}

func graphEdgeNeighbor(edge graphEdge, identifier string) string {
	if edge.Source == identifier {
		return edge.Target
	}
	return edge.Source
}

func graphNodeResponse(node graphNode) map[string]any {
	row := map[string]any{
		"id": node.ID, "label": node.Label, "type": node.Type, "kind": node.Kind,
	}
	if node.Path != "" {
		row["path"] = node.Path
	}
	if node.Scope != "" {
		row["scope"] = node.Scope
	}
	if node.ProjectID != "" {
		row["project_id"] = node.ProjectID
	}
	if node.Snippet != "" {
		row["snippet"] = node.Snippet
	}
	if node.LineStart > 0 {
		row["line_start"] = node.LineStart
		row["line_end"] = node.LineEnd
	}
	if node.Mentions > 0 {
		row["mentions"] = node.Mentions
	}
	if node.DocumentCount > 0 {
		row["document_count"] = node.DocumentCount
	}
	if len(node.Evidence) > 0 {
		row["evidence"] = node.Evidence
	}
	return row
}

func validGraphNodeRequestID(value string) bool {
	length := utf8.RuneCountInString(value)
	return length >= 1 && length <= 96
}

func writeGraphRequestError(writer http.ResponseWriter, code string) int {
	messages := map[string]string{
		"EMPTY_QUERY":            "请输入要检索的概念或问题。",
		"QUERY_TOO_LONG":         "检索内容过长，请缩短后重试。",
		"INVALID_LIMIT":          "结果数量超出允许范围。",
		"INVALID_NODE_ID":        "知识节点标识无效。",
		"INVALID_MAX_DEPTH":      "路径深度必须在 1 到 6 之间。",
		"INVALID_REQUEST_FIELDS": "请求字段无效。",
	}
	writeError(writer, http.StatusBadRequest, code, messages[code])
	return http.StatusBadRequest
}

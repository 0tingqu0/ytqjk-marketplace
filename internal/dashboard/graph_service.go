package dashboard

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
)

type graphEnvelope struct {
	GeneratedAt string         `json:"generated_at"`
	Revision    string         `json:"revision"`
	Graph       knowledgeGraph `json:"graph"`
}

type graphStep struct {
	node string
	edge graphEdge
}

func buildKnowledgeGraph(root string, limit int) (graphEnvelope, error) {
	documents, revision, err := loadGraphDocuments(root)
	if err != nil {
		return graphEnvelope{}, err
	}
	return graphEnvelope{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Revision:    revision,
		Graph:       buildSemanticGraph(documents, limit),
	}, nil
}

func semanticGraphSearch(root, query string, limit int) (map[string]any, error) {
	normalized := strings.TrimSpace(query)
	if normalized == "" {
		return nil, errors.New("EMPTY_QUERY")
	}
	if len([]rune(normalized)) > 2000 {
		return nil, errors.New("QUERY_TOO_LONG")
	}
	documents, _, err := loadGraphDocuments(root)
	if err != nil {
		return nil, err
	}
	queryTokens := semanticGraphTokens(normalized)
	results := make([]map[string]any, 0)
	for _, document := range documents {
		concept := cosineGraphTokens(queryTokens, document.Tokens)
		matchedWeight := 0
		matchedTerms := make([]string, 0)
		for token, count := range queryTokens {
			if document.Tokens[token] > 0 {
				matchedWeight += count
				matchedTerms = append(matchedTerms, token)
			}
		}
		totalWeight := 0
		for _, count := range queryTokens {
			totalWeight += count
		}
		coverage := float64(matchedWeight) / float64(maximum(totalWeight, 1))
		titleScore := cosineGraphTokens(queryTokens, semanticGraphTokens(document.Title))
		exact := 0.0
		if strings.Contains(strings.ToLower(document.Content), strings.ToLower(normalized)) {
			exact = 1
		}
		score := coverage*0.52 + concept*0.18 + titleScore*0.20 + exact*0.10
		if score <= 0 {
			continue
		}
		sort.Slice(matchedTerms, func(i, j int) bool {
			if len(matchedTerms[i]) == len(matchedTerms[j]) {
				return matchedTerms[i] < matchedTerms[j]
			}
			return len(matchedTerms[i]) > len(matchedTerms[j])
		})
		if len(matchedTerms) > 8 {
			matchedTerms = matchedTerms[:8]
		}
		row := map[string]any{
			"node_id": document.ID, "title": document.Title, "path": document.Path,
			"scope": document.Scope, "project_id": document.ProjectID,
			"snippet": graphSearchSnippet(document.Content, queryTokens),
			"score":   roundGraphScore(score), "matched_terms": matchedTerms,
		}
		if document.LineStart > 0 {
			row["line_start"] = document.LineStart
			row["line_end"] = document.LineEnd
		}
		results = append(results, row)
	}
	sort.Slice(results, func(i, j int) bool {
		left, right := results[i]["score"].(float64), results[j]["score"].(float64)
		if left == right {
			return results[i]["path"].(string) < results[j]["path"].(string)
		}
		return left > right
	})
	limit = clamp(limit, 1, 20)
	if len(results) > limit {
		results = results[:limit]
	}
	suggestions := []string{}
	if len(results) == 0 {
		suggestions = []string{"尝试实体名称", "缩短检索词"}
	}
	return map[string]any{
		"query": normalized, "mode": "concept-hybrid",
		"results": results, "suggestions": suggestions,
	}, nil
}

func recommendGraphKnowledge(root, nodeID string, limit int) (map[string]any, error) {
	envelope, err := buildKnowledgeGraph(root, 160)
	if err != nil {
		return nil, err
	}
	nodes, adjacency := graphIndexes(envelope.Graph)
	if _, exists := nodes[nodeID]; !exists {
		return map[string]any{"node_id": nodeID, "results": []any{}}, nil
	}
	scores := map[string]float64{}
	reasons := map[string]map[string]bool{}
	for _, first := range adjacency[nodeID] {
		scores[first.node] = maxFloat(scores[first.node], first.edge.Confidence)
		addGraphReason(reasons, first.node, first.edge.Label)
		for _, second := range adjacency[first.node] {
			if second.node == nodeID {
				continue
			}
			score := first.edge.Confidence * second.edge.Confidence * 0.78
			scores[second.node] = maxFloat(scores[second.node], score)
			addGraphReason(reasons, second.node, "经由 "+nodes[first.node].Label)
		}
	}
	identifiers := make([]string, 0, len(scores))
	for identifier := range scores {
		if _, exists := nodes[identifier]; exists {
			identifiers = append(identifiers, identifier)
		}
	}
	sort.Slice(identifiers, func(i, j int) bool {
		if scores[identifiers[i]] == scores[identifiers[j]] {
			return nodes[identifiers[i]].Label < nodes[identifiers[j]].Label
		}
		return scores[identifiers[i]] > scores[identifiers[j]]
	})
	limit = clamp(limit, 1, 20)
	if len(identifiers) > limit {
		identifiers = identifiers[:limit]
	}
	results := make([]map[string]any, 0, len(identifiers))
	for _, identifier := range identifiers {
		row := graphNodeMap(nodes[identifier])
		row["score"] = roundGraphScore(scores[identifier])
		row["reasons"] = sortedGraphReasons(reasons[identifier], 3)
		results = append(results, row)
	}
	return map[string]any{"node_id": nodeID, "results": results}, nil
}

func exploreGraphPath(root, source, target string, maxDepth int) (map[string]any, error) {
	envelope, err := buildKnowledgeGraph(root, 160)
	if err != nil {
		return nil, err
	}
	nodes, adjacency := graphIndexes(envelope.Graph)
	if _, sourceOK := nodes[source]; !sourceOK {
		return map[string]any{"found": false, "reason": "UNKNOWN_NODE", "nodes": []any{}, "edges": []any{}}, nil
	}
	if _, targetOK := nodes[target]; !targetOK {
		return map[string]any{"found": false, "reason": "UNKNOWN_NODE", "nodes": []any{}, "edges": []any{}}, nil
	}
	queue := []string{source}
	depth := map[string]int{source: 0}
	previous := map[string]graphStep{}
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		if current == target || depth[current] >= maxDepth {
			continue
		}
		for _, next := range adjacency[current] {
			if _, visited := depth[next.node]; visited {
				continue
			}
			depth[next.node] = depth[current] + 1
			previous[next.node] = graphStep{node: current, edge: next.edge}
			queue = append(queue, next.node)
		}
	}
	if _, found := depth[target]; !found {
		return map[string]any{"found": false, "reason": "NO_PATH", "nodes": []any{}, "edges": []any{}}, nil
	}
	orderedIDs := []string{target}
	edges := []graphEdge{}
	for current := target; current != source; {
		step := previous[current]
		edges = append(edges, step.edge)
		current = step.node
		orderedIDs = append(orderedIDs, current)
	}
	reverseStrings(orderedIDs)
	reverseEdges(edges)
	orderedNodes := make([]graphNode, 0, len(orderedIDs))
	for _, identifier := range orderedIDs {
		orderedNodes = append(orderedNodes, nodes[identifier])
	}
	return map[string]any{"found": true, "nodes": orderedNodes, "edges": edges, "hops": len(edges)}, nil
}

func graphIndexes(graph knowledgeGraph) (map[string]graphNode, map[string][]graphStep) {
	nodes := make(map[string]graphNode, len(graph.Nodes))
	adjacency := map[string][]graphStep{}
	for _, node := range graph.Nodes {
		nodes[node.ID] = node
	}
	for _, edge := range graph.Edges {
		adjacency[edge.Source] = append(adjacency[edge.Source], graphStep{edge.Target, edge})
		adjacency[edge.Target] = append(adjacency[edge.Target], graphStep{edge.Source, edge})
	}
	return nodes, adjacency
}

func graphSearchSnippet(content string, terms map[string]int) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		for term := range terms {
			if strings.Contains(strings.ToLower(line), term) {
				return truncateGraphText(line, 260)
			}
		}
	}
	return truncateGraphText(strings.TrimSpace(content), 260)
}

func graphNodeMap(node graphNode) map[string]any {
	data, _ := json.Marshal(node)
	result := map[string]any{}
	_ = json.Unmarshal(data, &result)
	return result
}

func addGraphReason(reasons map[string]map[string]bool, identifier, reason string) {
	if reasons[identifier] == nil {
		reasons[identifier] = map[string]bool{}
	}
	reasons[identifier][reason] = true
}

func sortedGraphReasons(values map[string]bool, limit int) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseEdges(values []graphEdge) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func maximum(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

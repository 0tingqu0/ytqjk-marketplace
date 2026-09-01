package dashboard

import (
	"math"
	"net/http"
	"sort"
	"unicode/utf8"
)

type graphRecommendationRequest struct {
	NodeID string `json:"node_id"`
	Limit  *int   `json:"limit,omitempty"`
}

type graphPathRequest struct {
	Source   string `json:"source"`
	Target   string `json:"target"`
	MaxDepth *int   `json:"max_depth,omitempty"`
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
	graph, _, _ := s.currentKnowledgeGraph(160)
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
	graph, _, _ := s.currentKnowledgeGraph(160)
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

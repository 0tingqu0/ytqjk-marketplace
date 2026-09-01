package dashboard

import (
	"net/http"
	"strconv"
)

func (s *Server) graphHTTP(writer http.ResponseWriter, rawLimit string) int {
	limit := 100
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 20 || parsed > 160 {
			return writeGraphRequestError(writer, "INVALID_LIMIT")
		}
		limit = parsed
	}
	graph, generatedAt, revision := s.currentKnowledgeGraph(limit)
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "generated_at": generatedAt,
		"revision": revision, "graph": graph,
	})
	return http.StatusOK
}

func (s *Server) graphRevisionHTTP(writer http.ResponseWriter) int {
	_, revision, _ := loadGraphSources(s.KnowledgeRoot)
	writeJSON(writer, http.StatusOK, map[string]any{
		"ok": true, "revision": revision,
	})
	return http.StatusOK
}

func (s *Server) currentKnowledgeGraph(limit int) (knowledgeGraph, string, string) {
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
		return graph, s.graphCache.GeneratedAt, signature
	}
	graph := buildKnowledgeGraph(sources, limit, vectorAvailable)
	s.graphCache.Graphs[limit] = graph
	return graph, s.graphCache.GeneratedAt, signature
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

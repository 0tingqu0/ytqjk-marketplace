package dashboard

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	graphPath         = "/api/knowledge-graph"
	graphRevisionPath = "/api/knowledge-graph-revision"
)

func (s *Server) handleKnowledgeGraphAPI(
	writer http.ResponseWriter,
	request *http.Request,
) (int, bool) {
	path := request.URL.Path
	if request.Method == http.MethodGet {
		switch path {
		case graphRevisionPath:
			revision, err := knowledgeGraphRevision(s.KnowledgeRoot)
			if err != nil {
				return writeGraphError(writer, http.StatusServiceUnavailable, "GRAPH_UNAVAILABLE"), true
			}
			writeJSON(writer, http.StatusOK, map[string]any{"ok": true, "revision": revision})
			return http.StatusOK, true
		case graphPath:
			limit, err := graphQueryLimit(request.URL.Query().Get("limit"))
			if err != nil {
				return writeGraphError(writer, http.StatusBadRequest, "INVALID_LIMIT"), true
			}
			envelope, err := buildKnowledgeGraph(s.KnowledgeRoot, limit)
			if err != nil {
				return writeGraphError(writer, http.StatusServiceUnavailable, "GRAPH_UNAVAILABLE"), true
			}
			writeJSON(writer, http.StatusOK, map[string]any{
				"ok": true, "generated_at": envelope.GeneratedAt,
				"revision": envelope.Revision, "graph": envelope.Graph,
			})
			return http.StatusOK, true
		}
	}
	if request.Method != http.MethodPost {
		return 0, false
	}
	switch path {
	case "/api/knowledge-search":
		return s.handleGraphSearch(writer, request), true
	case "/api/knowledge-recommendations":
		return s.handleGraphRecommendations(writer, request), true
	case "/api/knowledge-path":
		return s.handleGraphPath(writer, request), true
	default:
		return 0, false
	}
}

func (s *Server) handleGraphSearch(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Query string `json:"query"`
		Limit *int   `json:"limit"`
	}
	if err := readJSON(request, &payload); err != nil {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_REQUEST_FIELDS")
	}
	query := strings.TrimSpace(payload.Query)
	if query == "" {
		return writeGraphError(writer, http.StatusBadRequest, "EMPTY_QUERY")
	}
	if utf8.RuneCountInString(query) > 2000 {
		return writeGraphError(writer, http.StatusBadRequest, "QUERY_TOO_LONG")
	}
	limit, valid := optionalGraphLimit(payload.Limit, 8, 20)
	if !valid {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_LIMIT")
	}
	result, err := semanticGraphSearch(s.KnowledgeRoot, query, limit)
	if err != nil {
		return writeGraphServiceError(writer, err)
	}
	return writeGraphResult(writer, result)
}

func (s *Server) handleGraphRecommendations(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		NodeID string `json:"node_id"`
		Limit  *int   `json:"limit"`
	}
	if err := readJSON(request, &payload); err != nil {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_REQUEST_FIELDS")
	}
	if utf8.RuneCountInString(payload.NodeID) < 1 || utf8.RuneCountInString(payload.NodeID) > 96 {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_NODE_ID")
	}
	limit, valid := optionalGraphLimit(payload.Limit, 6, 20)
	if !valid {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_LIMIT")
	}
	result, err := recommendGraphKnowledge(s.KnowledgeRoot, payload.NodeID, limit)
	if err != nil {
		return writeGraphServiceError(writer, err)
	}
	return writeGraphResult(writer, result)
}

func (s *Server) handleGraphPath(writer http.ResponseWriter, request *http.Request) int {
	var payload struct {
		Source   string `json:"source"`
		Target   string `json:"target"`
		MaxDepth *int   `json:"max_depth"`
	}
	if err := readJSON(request, &payload); err != nil {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_REQUEST_FIELDS")
	}
	if !validGraphNodeID(payload.Source) || !validGraphNodeID(payload.Target) {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_NODE_ID")
	}
	depth, valid := optionalGraphLimit(payload.MaxDepth, 5, 6)
	if !valid {
		return writeGraphError(writer, http.StatusBadRequest, "INVALID_MAX_DEPTH")
	}
	result, err := exploreGraphPath(s.KnowledgeRoot, payload.Source, payload.Target, depth)
	if err != nil {
		return writeGraphServiceError(writer, err)
	}
	return writeGraphResult(writer, result)
}

func graphQueryLimit(raw string) (int, error) {
	if raw == "" {
		return 100, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 20 || limit > 160 {
		return 0, errors.New("INVALID_LIMIT")
	}
	return limit, nil
}

func optionalGraphLimit(value *int, fallback, maximum int) (int, bool) {
	if value == nil {
		return fallback, true
	}
	if *value < 1 || *value > maximum {
		return 0, false
	}
	return *value, true
}

func validGraphNodeID(value string) bool {
	length := utf8.RuneCountInString(value)
	return length >= 1 && length <= 96
}

func writeGraphResult(writer http.ResponseWriter, result map[string]any) int {
	result["ok"] = true
	writeJSON(writer, http.StatusOK, result)
	return http.StatusOK
}

func writeGraphServiceError(writer http.ResponseWriter, err error) int {
	code := err.Error()
	if code == "EMPTY_QUERY" || code == "QUERY_TOO_LONG" {
		return writeGraphError(writer, http.StatusBadRequest, code)
	}
	return writeGraphError(writer, http.StatusServiceUnavailable, "GRAPH_UNAVAILABLE")
}

func writeGraphError(writer http.ResponseWriter, status int, code string) int {
	messages := map[string]string{
		"EMPTY_QUERY":            "请输入要检索的概念或问题。",
		"QUERY_TOO_LONG":         "检索内容过长，请缩短后重试。",
		"INVALID_LIMIT":          "结果数量必须在允许范围内。",
		"INVALID_NODE_ID":        "知识节点标识无效。",
		"INVALID_MAX_DEPTH":      "路径深度必须在 1 到 6 之间。",
		"INVALID_REQUEST_FIELDS": "请求字段无效。",
		"GRAPH_UNAVAILABLE":      "知识图谱暂时不可用，请稍后重试。",
	}
	writeError(writer, status, code, messages[code])
	return status
}

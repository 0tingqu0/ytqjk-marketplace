package dashboard

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type graphMention struct {
	Label      string
	Kind       string
	Confidence float64
	Line       int
	Start      int
	End        int
}

type graphRelation struct {
	Source     string
	Target     string
	Type       string
	Label      string
	Confidence float64
	Line       int
	Excerpt    string
}

var (
	wikiTermPattern    = regexp.MustCompile(`\[\[([^\]|#]{2,80})(?:[|#][^\]]*)?\]\]`)
	codeTermPattern    = regexp.MustCompile("`([^`\\n]{2,80})`")
	englishTermPattern = regexp.MustCompile(
		`\b(?:[A-Z][A-Za-z0-9]+|[a-z]+[A-Z][A-Za-z0-9]*)(?:[ ._-](?:[A-Z][A-Za-z0-9]+|[0-9]+)){0,3}\b`,
	)
	techTermPattern = regexp.MustCompile(
		`知识图谱|知识库|语义搜索|语义检索|实体关系抽取|关系抽取|向量索引|全文索引|` +
			`相似知识推荐|知识推荐|路径探索|本地知识工作台|知识工作台|工作台|知识服务|语义模型|视觉模型`,
	)
	numberedTopicPattern = regexp.MustCompile(`^(?:[0-9]+(?:\.[0-9]+)*[.)]?|[IVXivx]+[.)])\s+`)
	spacePattern         = regexp.MustCompile(`\s+`)
	typeFragmentPattern  = regexp.MustCompile(`[][{},;:=<>]`)
	noisyCodePattern     = regexp.MustCompile(`(?i)(?:error|exception|tests?|testcase)$`)
)

var graphGenericTerms = termSet(
	"http", "https", "true", "false", "none", "null", "string", "number",
	"object", "array", "markdown", "json", "python", "path", "valueerror",
	"runtimeerror", "oserror", "error", "exception", "return", "callable",
	"connection", "row", "text", "where", "from", "select", "values", "join",
	"read", "run", "use", "active", "blocked", "done", "failed", "running",
	"succeeded", "status", "update", "approved", "verified", "candidate",
	"testcase", "temporarydirectory", "content-type", "not_configured", "installed",
	"接口", "服务", "模型", "算法", "模块", "数据库",
)

var graphLowInformationTerms = termSet(
	"if", "in", "node", "end", "exists", "claim", "textcontent", "text not null",
	"argumentparser", "pureposixpath", "scanresult", "scanstate", "zipfile", "uuid",
	"this", "every", "local", "software", "systemexit", "ids", "join-path",
	"stopped", "queued", "unknown", "integer not null", "text primary key",
)

var graphRelationWords = []struct {
	word  string
	kind  string
	label string
}{
	{"使用", "uses", "使用"}, {"依赖", "depends_on", "依赖"},
	{"属于", "belongs_to", "属于"}, {"包含", "contains", "包含"},
	{"支持", "supports", "支持"}, {"关联", "related_to", "关联"},
	{"连接", "connects_to", "连接"}, {"引用", "references", "引用"},
	{"导致", "causes", "导致"}, {"生成", "produces", "生成"},
	{"基于", "based_on", "基于"}, {"调用", "calls", "调用"},
}

func termSet(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[strings.ToLower(value)] = true
	}
	return result
}

func canonicalGraphLabel(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimLeftFunc(value, func(r rune) bool {
		return r == '#' || r == '*' || r == '-' || unicode.IsSpace(r)
	})
	value = strings.TrimRight(value, "，。；：:,.!?！？ \t\r\n")
	value = spacePattern.ReplaceAllString(value, " ")
	if utf8.RuneCountInString(value) > 80 {
		value = string([]rune(value)[:80])
	}
	return value
}

func validGraphLabel(label, source string) bool {
	length := utf8.RuneCountInString(label)
	folded := strings.ToLower(label)
	if length < 2 || length > 80 || graphGenericTerms[folded] || graphLowInformationTerms[folded] {
		return false
	}
	if typeFragmentPattern.MatchString(label) {
		return false
	}
	if (source == "code" || source == "english") && noisyCodePattern.MatchString(label) {
		return false
	}
	if source == "tech" {
		chinese := 0
		for _, r := range label {
			if unicode.Is(unicode.Han, r) {
				chinese++
			}
		}
		if chinese > 12 {
			return false
		}
	}
	return true
}

func extractGraphKnowledge(content string) ([]graphMention, []graphRelation) {
	var mentions []graphMention
	var relations []graphRelation
	frontmatter := false
	for lineIndex, line := range strings.Split(content, "\n") {
		lineNumber := lineIndex + 1
		if lineNumber == 1 && strings.TrimSpace(line) == "---" {
			frontmatter = true
			continue
		}
		if frontmatter {
			if strings.TrimSpace(line) == "---" {
				frontmatter = false
			}
			continue
		}
		spans := graphLineMentions(line, lineNumber)
		mentions = append(mentions, spans...)
		relations = append(relations, graphLineRelations(line, lineNumber, spans)...)
	}
	return mentions, relations
}

func graphLineMentions(line string, lineNumber int) []graphMention {
	spans := make([]graphMention, 0)
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		topic := strings.TrimSpace(strings.TrimLeft(trimmed, "#"))
		topic = numberedTopicPattern.ReplaceAllString(canonicalGraphLabel(topic), "")
		if validGraphLabel(topic, "heading") {
			start := strings.Index(line, topic)
			spans = append(spans, graphMention{topic, "topic", 0.94, lineNumber, start, start + len(topic)})
		}
	}
	patterns := []struct {
		pattern    *regexp.Regexp
		kind       string
		confidence float64
		source     string
		capture    bool
	}{
		{wikiTermPattern, "concept", 0.98, "wiki", true},
		{codeTermPattern, "term", 0.90, "code", true},
		{techTermPattern, "concept", 0.78, "tech", false},
		{englishTermPattern, "term", 0.72, "english", false},
	}
	for _, item := range patterns {
		for _, match := range item.pattern.FindAllStringSubmatchIndex(line, -1) {
			start, end := match[0], match[1]
			if item.capture && len(match) >= 4 {
				start, end = match[2], match[3]
			}
			label := canonicalGraphLabel(line[start:end])
			if validGraphLabel(label, item.source) {
				spans = append(spans, graphMention{label, item.kind, item.confidence, lineNumber, start, end})
			}
		}
	}
	deduped := map[string]graphMention{}
	for _, span := range spans {
		key := strings.ToLower(span.Label)
		current, exists := deduped[key]
		if !exists || span.Confidence > current.Confidence {
			deduped[key] = span
		}
	}
	result := make([]graphMention, 0, len(deduped))
	for _, span := range deduped {
		result = append(result, span)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Start < result[j].Start })
	return result
}

func graphLineRelations(line string, lineNumber int, spans []graphMention) []graphRelation {
	var result []graphRelation
	for _, relation := range graphRelationWords {
		for offset := 0; ; {
			index := strings.Index(line[offset:], relation.word)
			if index < 0 {
				break
			}
			index += offset
			var left, right *graphMention
			for spanIndex := range spans {
				span := &spans[spanIndex]
				if span.End <= index {
					left = span
				}
				if right == nil && span.Start >= index+len(relation.word) {
					right = span
				}
			}
			if left != nil && right != nil && left.Label != right.Label {
				confidence := left.Confidence
				if right.Confidence < confidence {
					confidence = right.Confidence
				}
				result = append(result, graphRelation{
					left.Label, right.Label, relation.kind, relation.label,
					confidence, lineNumber, truncateGraphText(strings.TrimSpace(line), 240),
				})
			}
			offset = index + len(relation.word)
		}
	}
	return result
}

func truncateGraphText(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) > maximum {
		return string(runes[:maximum])
	}
	return value
}

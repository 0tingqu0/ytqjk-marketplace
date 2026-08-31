package dashboard

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

var (
	graphWikiPattern    = regexp.MustCompile(`\[\[([^\]|#]{2,80})([|#][^\]]*)?\]\]`)
	graphCodePattern    = regexp.MustCompile("`([^`\\n]{2,80})`")
	graphHeadingPattern = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.{2,100}?)\s*$`)
	graphEnglishPattern = regexp.MustCompile(`\b([A-Z][A-Za-z0-9]+|[a-z]+[A-Z][A-Za-z0-9]*)([ ._-]([A-Z][A-Za-z0-9]+|[0-9]+)){0,3}\b`)
	graphTechPattern    = regexp.MustCompile(`[一-龥A-Za-z0-9]{0,12}(知识图谱|知识库|语义搜索|语义检索|实体关系抽取|关系抽取|向量索引|全文索引|相似知识推荐|知识推荐|路径探索|工作台|接口|服务|模型|算法|模块|数据库)`)
	graphTokenPattern   = regexp.MustCompile(`[a-z][a-z0-9_.+\-]{1,}|[\p{Han}]+`)
)

var graphGenericTerms = stringSet(
	"http", "https", "true", "false", "none", "null", "string", "number", "object", "array",
	"markdown", "json", "python", "path", "valueerror", "runtimeerror", "oserror", "error", "exception",
	"return", "any", "callable", "connection", "row", "text", "bytesio", "where", "from", "select",
	"values", "insert into", "join", "order by", "and", "or", "on", "set", "read", "run", "use",
	"before", "after", "active", "blocked", "done", "failed", "running", "succeeded", "id", "head",
	"status", "update", "approved", "verified", "candidate", "applied", "all", "testcase", "assertequal",
	"asserttrue", "assertfalse", "assertin", "assertnotin", "assertraisesregex", "temporarydirectory", "the",
	"never", "users", "scripts", "license", "e402", "utf-8", "content-type", "gib", "mib", "localappdata",
	"not_configured", "installed",
)

type graphPattern struct {
	expression *regexp.Regexp
	kind       string
	confidence float64
	capture    int
}

var graphEntityPatterns = []graphPattern{
	{expression: graphWikiPattern, kind: "concept", confidence: 0.98, capture: 1},
	{expression: graphCodePattern, kind: "term", confidence: 0.90, capture: 1},
	{expression: graphTechPattern, kind: "concept", confidence: 0.78},
	{expression: graphEnglishPattern, kind: "term", confidence: 0.72},
}

type graphRelationWord struct {
	word  string
	type_ string
	label string
}

var graphRelationWords = []graphRelationWord{
	{word: "使用", type_: "uses", label: "使用"},
	{word: "依赖", type_: "depends_on", label: "依赖"},
	{word: "属于", type_: "belongs_to", label: "属于"},
	{word: "包含", type_: "contains", label: "包含"},
	{word: "支持", type_: "supports", label: "支持"},
	{word: "关联", type_: "related_to", label: "关联"},
	{word: "连接", type_: "connects_to", label: "连接"},
	{word: "引用", type_: "references", label: "引用"},
	{word: "导致", type_: "causes", label: "导致"},
	{word: "生成", type_: "produces", label: "生成"},
	{word: "基于", type_: "based_on", label: "基于"},
	{word: "调用", type_: "calls", label: "调用"},
}

type graphSpan struct {
	Label      string
	Kind       string
	Confidence float64
	Start      int
	End        int
	Line       int
	Excerpt    string
}

type extractedGraphRelation struct {
	Source     string
	Target     string
	Type       string
	Label      string
	Confidence float64
	Line       int
	Excerpt    string
}

func canonicalGraphLabel(value string) string {
	value = norm.NFKC.String(strings.TrimSpace(value))
	value = strings.TrimLeftFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || character == '#' || character == '*' || character == '-'
	})
	value = strings.TrimRightFunc(value, func(character rune) bool {
		return unicode.IsSpace(character) || strings.ContainsRune("，。；：:,.!?！？", character)
	})
	return truncateRunes(strings.Join(strings.Fields(value), " "), 80)
}

func semanticGraphTokens(value string) map[string]int {
	value = strings.ToLower(norm.NFKC.String(value))
	result := map[string]int{}
	for _, token := range graphTokenPattern.FindAllString(value, -1) {
		runes := []rune(token)
		if allHan(runes) {
			if len(runes) >= 2 && len(runes) <= 8 {
				result[token] += 2
			}
			for size := 2; size <= 3; size++ {
				for index := 0; index+size <= len(runes); index++ {
					result[string(runes[index:index+size])]++
				}
			}
			continue
		}
		if _, generic := graphGenericTerms[token]; !generic {
			result[token]++
		}
	}
	return result
}

func graphLineSpans(line string, lineNumber int) []graphSpan {
	rows := make([]graphSpan, 0, 8)
	if match := graphHeadingPattern.FindStringSubmatchIndex(line); len(match) >= 4 {
		label := canonicalGraphLabel(line[match[2]:match[3]])
		if validGraphLabel(label) {
			rows = append(rows, graphSpan{
				Label: label, Kind: "topic", Confidence: 0.94,
				Start: match[2], End: match[3], Line: lineNumber, Excerpt: strings.TrimSpace(line),
			})
		}
	}
	for _, pattern := range graphEntityPatterns {
		for _, match := range pattern.expression.FindAllStringSubmatchIndex(line, -1) {
			start, end := match[0], match[1]
			if pattern.capture > 0 {
				position := pattern.capture * 2
				if position+1 >= len(match) || match[position] < 0 {
					continue
				}
				start, end = match[position], match[position+1]
			}
			label := canonicalGraphLabel(line[start:end])
			if !validGraphLabel(label) {
				continue
			}
			rows = append(rows, graphSpan{
				Label: label, Kind: pattern.kind, Confidence: pattern.confidence,
				Start: start, End: end, Line: lineNumber, Excerpt: strings.TrimSpace(line),
			})
		}
	}
	deduplicated := map[string]graphSpan{}
	for _, row := range rows {
		key := strings.ToLower(norm.NFKC.String(row.Label))
		if current, exists := deduplicated[key]; !exists || row.Confidence > current.Confidence {
			deduplicated[key] = row
		}
	}
	rows = rows[:0]
	for _, row := range deduplicated {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Start == rows[j].Start {
			return rows[i].Label < rows[j].Label
		}
		return rows[i].Start < rows[j].Start
	})
	return rows
}

func extractGraphKnowledge(content string, lineOffset int) ([]graphSpan, []extractedGraphRelation) {
	entities := make([]graphSpan, 0)
	relations := make([]extractedGraphRelation, 0)
	frontmatter := false
	for index, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSuffix(rawLine, "\r")
		lineNumber := lineOffset + index + 1
		if lineOffset == 0 && index == 0 && strings.TrimSpace(line) == "---" {
			frontmatter = true
			continue
		}
		if frontmatter {
			if strings.TrimSpace(line) == "---" {
				frontmatter = false
			}
			continue
		}
		spans := graphLineSpans(line, lineNumber)
		entities = append(entities, spans...)
		for _, relation := range graphRelationWords {
			for offset := 0; offset < len(line); {
				relative := strings.Index(line[offset:], relation.word)
				if relative < 0 {
					break
				}
				start := offset + relative
				end := start + len(relation.word)
				var left, right *graphSpan
				for spanIndex := range spans {
					span := &spans[spanIndex]
					if span.End <= start {
						left = span
					}
					if right == nil && span.Start >= end {
						right = span
					}
				}
				if left != nil && right != nil && left.Label != right.Label {
					confidence := left.Confidence
					if right.Confidence < confidence {
						confidence = right.Confidence
					}
					relations = append(relations, extractedGraphRelation{
						Source: left.Label, Target: right.Label, Type: relation.type_, Label: relation.label,
						Confidence: confidence, Line: lineNumber, Excerpt: truncateRunes(strings.TrimSpace(line), 240),
					})
				}
				offset = end
			}
		}
	}
	return entities, relations
}

func validGraphLabel(label string) bool {
	length := utf8.RuneCountInString(label)
	if length < 2 || length > 80 {
		return false
	}
	_, generic := graphGenericTerms[strings.ToLower(label)]
	return !generic
}

func truncateRunes(value string, maximum int) string {
	if maximum < 1 || utf8.RuneCountInString(value) <= maximum {
		return value
	}
	return string([]rune(value)[:maximum])
}

func allHan(value []rune) bool {
	if len(value) == 0 {
		return false
	}
	for _, character := range value {
		if !unicode.Is(unicode.Han, character) {
			return false
		}
	}
	return true
}

func stringSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

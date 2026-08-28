package dashboard

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

type graphDocument struct {
	ID, Title, Path, Scope, ProjectID, Content string
	LineStart, LineEnd, SourceChunks           int
	Tokens                                     map[string]int
}

type graphEvidence struct {
	Path      string `json:"path,omitempty"`
	LineStart int    `json:"line_start,omitempty"`
	LineEnd   int    `json:"line_end,omitempty"`
	Excerpt   string `json:"excerpt,omitempty"`
}

type graphNode struct {
	ID            string          `json:"id"`
	Label         string          `json:"label"`
	DisplayLabel  string          `json:"display_label,omitempty"`
	Type          string          `json:"type"`
	Kind          string          `json:"kind"`
	Path          string          `json:"path,omitempty"`
	Scope         string          `json:"scope,omitempty"`
	ProjectID     string          `json:"project_id,omitempty"`
	Snippet       string          `json:"snippet,omitempty"`
	LineStart     int             `json:"line_start,omitempty"`
	LineEnd       int             `json:"line_end,omitempty"`
	Mentions      int             `json:"mentions,omitempty"`
	Confidence    float64         `json:"confidence,omitempty"`
	DocumentCount int             `json:"document_count,omitempty"`
	Evidence      []graphEvidence `json:"evidence,omitempty"`
}

type graphEdge struct {
	ID         string          `json:"id"`
	Source     string          `json:"source"`
	Target     string          `json:"target"`
	Type       string          `json:"type"`
	Label      string          `json:"label"`
	Confidence float64         `json:"confidence"`
	Weight     int             `json:"weight"`
	Evidence   []graphEvidence `json:"evidence,omitempty"`
}

type graphStats struct {
	Documents    int `json:"documents"`
	Entities     int `json:"entities"`
	Relations    int `json:"relations"`
	SourceChunks int `json:"source_chunks"`
}

type knowledgeGraph struct {
	Schema       int            `json:"schema"`
	Nodes        []graphNode    `json:"nodes"`
	Edges        []graphEdge    `json:"edges"`
	Stats        graphStats     `json:"stats"`
	Capabilities map[string]any `json:"capabilities"`
	Warnings     []string       `json:"warnings"`
}

type graphEntity struct {
	Label      string
	Kind       string
	Confidence float64
	Mentions   int
	Documents  map[string]bool
	Evidence   []graphEvidence
}

var graphTokenPattern = regexp.MustCompile(`[\p{L}\p{N}_+.-]+`)

func stableGraphID(kind, value string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + value))
	return kind + "-" + hex.EncodeToString(digest[:12])
}

func graphDocumentTitle(path, content string) string {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		hashes := 0
		for hashes < len(trimmed) && trimmed[hashes] == '#' {
			hashes++
		}
		if hashes < 1 || hashes > 6 || hashes >= len(trimmed) ||
			(trimmed[hashes] != ' ' && trimmed[hashes] != '\t') {
			continue
		}
		if title := canonicalGraphLabel(trimmed[hashes:]); title != "" {
			return title
		}
	}
	name := filepath.Base(path)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name = canonicalGraphLabel(name); name != "" {
		return name
	}
	return "知识文档"
}

func semanticGraphTokens(value string) map[string]int {
	result := map[string]int{}
	for _, raw := range graphTokenPattern.FindAllString(strings.ToLower(value), -1) {
		if graphGenericTerms[raw] || graphLowInformationTerms[raw] {
			continue
		}
		runes := []rune(raw)
		allHan := len(runes) > 0
		for _, r := range runes {
			if !unicode.Is(unicode.Han, r) {
				allHan = false
				break
			}
		}
		if allHan {
			if len(runes) >= 2 && len(runes) <= 8 {
				result[raw] += 2
			}
			for _, size := range []int{2, 3} {
				for index := 0; index+size <= len(runes); index++ {
					result[string(runes[index:index+size])]++
				}
			}
			continue
		}
		if len(runes) >= 2 {
			result[raw]++
		}
	}
	return result
}

func buildSemanticGraph(documents []graphDocument, limit int) knowledgeGraph {
	limit = clamp(limit, 20, 160)
	prepared := append([]graphDocument(nil), documents...)
	sourceChunks := 0
	for index := range prepared {
		prepared[index].Title = graphDocumentTitle(prepared[index].Path, prepared[index].Content)
		sourceChunks += prepared[index].SourceChunks
	}
	densityEntities, _ := aggregateGraphEntities(prepared)
	selectedDocuments := selectGraphDocuments(prepared, densityEntities, limit)
	selectedIDs := make(map[string]bool, len(selectedDocuments))
	titleCounts := map[string]int{}
	for _, document := range selectedDocuments {
		selectedIDs[document.ID] = true
		titleCounts[document.Title]++
	}
	entities, relations := aggregateGraphEntities(selectedDocuments)
	sort.Slice(entities, func(i, j int) bool {
		left, right := entities[i], entities[j]
		if len(left.Documents) != len(right.Documents) {
			return len(left.Documents) > len(right.Documents)
		}
		if left.Mentions != right.Mentions {
			return left.Mentions > right.Mentions
		}
		if left.Confidence != right.Confidence {
			return left.Confidence > right.Confidence
		}
		return left.Label < right.Label
	})
	entityLimit := limit - len(selectedDocuments)
	if len(entities) > entityLimit {
		entities = entities[:entityLimit]
	}
	nodes := make([]graphNode, 0, len(selectedDocuments)+len(entities))
	for _, document := range selectedDocuments {
		display := document.Title
		if titleCounts[document.Title] > 1 {
			parent := filepath.Base(filepath.Dir(document.Path))
			if parent == "." || parent == "" {
				parent = document.Scope
			}
			display += " · " + parent
		}
		nodes = append(nodes, graphNode{
			ID: document.ID, Label: document.Title, DisplayLabel: display,
			Type: "document", Kind: "document", Path: document.Path,
			Scope: document.Scope, ProjectID: document.ProjectID,
			Snippet:   truncateGraphText(document.Content, 240),
			LineStart: document.LineStart, LineEnd: document.LineEnd,
		})
	}
	selectedEntities := make(map[string]graphEntity, len(entities))
	for _, entity := range entities {
		selectedEntities[strings.ToLower(entity.Label)] = entity
		nodes = append(nodes, graphNode{
			ID:    stableGraphID("entity", strings.ToLower(entity.Label)),
			Label: entity.Label, Type: "entity", Kind: entity.Kind,
			Mentions: entity.Mentions, Confidence: roundGraphScore(entity.Confidence),
			DocumentCount: len(entity.Documents), Evidence: firstGraphEvidence(entity.Evidence, 5),
		})
	}
	edges := graphEdges(selectedDocuments, selectedIDs, selectedEntities, relations)
	warnings := []string{}
	if len(documents) == 0 {
		warnings = append(warnings, "NO_KNOWLEDGE_SOURCES")
	}
	return knowledgeGraph{
		Schema: 1, Nodes: nodes, Edges: edges,
		Stats: graphStats{len(selectedDocuments), len(entities), len(edges), sourceChunks},
		Capabilities: map[string]any{
			"entity_extraction": "rules-v1", "semantic_search": "concept-hybrid",
			"embedding": false, "recommendations": true, "path_exploration": true,
		},
		Warnings: warnings,
	}
}

func selectGraphDocuments(documents []graphDocument, entities []graphEntity, limit int) []graphDocument {
	density := map[string]int{}
	for _, entity := range entities {
		for identifier := range entity.Documents {
			density[identifier]++
		}
	}
	selected := append([]graphDocument(nil), documents...)
	sort.Slice(selected, func(i, j int) bool {
		left, right := selected[i], selected[j]
		if density[left.ID] != density[right.ID] {
			return density[left.ID] > density[right.ID]
		}
		if (left.Scope == "global") != (right.Scope == "global") {
			return left.Scope == "global"
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		return left.Path < right.Path
	})
	maximumDocuments := clamp(limit/3, 4, 36)
	if len(selected) > maximumDocuments {
		selected = selected[:maximumDocuments]
	}
	return selected
}

func aggregateGraphEntities(documents []graphDocument) ([]graphEntity, map[string][]graphRelation) {
	entities := map[string]*graphEntity{}
	relations := map[string][]graphRelation{}
	for _, document := range documents {
		mentions, extractedRelations := extractGraphKnowledge(document.Content)
		relations[document.ID] = extractedRelations
		lines := strings.Split(document.Content, "\n")
		for _, mention := range mentions {
			key := strings.ToLower(mention.Label)
			entity := entities[key]
			if entity == nil {
				entity = &graphEntity{Label: mention.Label, Kind: mention.Kind, Documents: map[string]bool{}}
				entities[key] = entity
			}
			entity.Mentions++
			entity.Documents[document.ID] = true
			if mention.Confidence > entity.Confidence {
				entity.Confidence = mention.Confidence
				entity.Kind = mention.Kind
			}
			entity.Evidence = append(entity.Evidence, graphDocumentEvidence(
				document, mention.Line, graphLineExcerpt(lines, mention.Line),
			))
		}
	}
	result := make([]graphEntity, 0, len(entities))
	for _, entity := range entities {
		if entity.Confidence >= 0.78 || entity.Mentions > 1 || len(entity.Documents) > 1 {
			result = append(result, *entity)
		}
	}
	return result, relations
}

func graphEdges(
	documents []graphDocument,
	selectedDocuments map[string]bool,
	entities map[string]graphEntity,
	relations map[string][]graphRelation,
) []graphEdge {
	edges := map[string]*graphEdge{}
	for _, document := range documents {
		mentions, _ := extractGraphKnowledge(document.Content)
		mentionCounts := map[string]int{}
		mentionEvidence := map[string][]graphEvidence{}
		lines := strings.Split(document.Content, "\n")
		for _, mention := range mentions {
			key := strings.ToLower(mention.Label)
			if _, selected := entities[key]; selected {
				mentionCounts[key]++
				mentionEvidence[key] = append(mentionEvidence[key], graphDocumentEvidence(
					document, mention.Line, graphLineExcerpt(lines, mention.Line),
				))
			}
		}
		for key, count := range mentionCounts {
			entity := entities[key]
			addGraphEdge(edges, document.ID, stableGraphID("entity", key), "mentions", "提及", entity.Confidence, count, firstGraphEvidence(mentionEvidence[key], 2))
		}
		for _, relation := range relations[document.ID] {
			left, leftOK := entities[strings.ToLower(relation.Source)]
			right, rightOK := entities[strings.ToLower(relation.Target)]
			if !leftOK || !rightOK {
				continue
			}
			evidence := []graphEvidence{graphDocumentEvidence(document, relation.Line, relation.Excerpt)}
			addGraphEdge(edges, stableGraphID("entity", strings.ToLower(left.Label)), stableGraphID("entity", strings.ToLower(right.Label)), relation.Type, relation.Label, relation.Confidence, 1, evidence)
		}
	}
	addSimilarDocumentEdges(edges, documents, selectedDocuments)
	result := make([]graphEdge, 0, len(edges))
	for _, edge := range edges {
		edge.Confidence = roundGraphScore(edge.Confidence)
		edge.Evidence = firstGraphEvidence(edge.Evidence, 3)
		result = append(result, *edge)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func addGraphEdge(edges map[string]*graphEdge, source, target, kind, label string, confidence float64, weight int, evidence []graphEvidence) {
	key := source + "\x00" + target + "\x00" + kind
	edge := edges[key]
	if edge == nil {
		edge = &graphEdge{ID: stableGraphID("edge", key), Source: source, Target: target, Type: kind, Label: label}
		edges[key] = edge
	}
	if confidence > edge.Confidence {
		edge.Confidence = confidence
	}
	edge.Weight += weight
	edge.Evidence = append(edge.Evidence, evidence...)
}

func addSimilarDocumentEdges(edges map[string]*graphEdge, documents []graphDocument, selected map[string]bool) {
	type similarity struct {
		left, right string
		score       float64
	}
	candidates := make([]similarity, 0)
	for leftIndex, left := range documents {
		if !selected[left.ID] {
			continue
		}
		for _, right := range documents[leftIndex+1:] {
			if !selected[right.ID] {
				continue
			}
			score := cosineGraphTokens(left.Tokens, right.Tokens)
			if score >= 0.12 {
				candidates = append(candidates, similarity{left.ID, right.ID, score})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].left != candidates[j].left {
			return candidates[i].left < candidates[j].left
		}
		return candidates[i].right < candidates[j].right
	})
	degree := map[string]int{}
	for _, candidate := range candidates {
		if degree[candidate.left] >= 2 || degree[candidate.right] >= 2 {
			continue
		}
		addGraphEdge(edges, candidate.left, candidate.right, "similar_to", "相似", candidate.score, 1, nil)
		degree[candidate.left]++
		degree[candidate.right]++
	}
}

func graphDocumentEvidence(document graphDocument, line int, excerpt string) graphEvidence {
	evidence := graphEvidence{Path: document.Path, Excerpt: truncateGraphText(strings.TrimSpace(excerpt), 240)}
	if document.LineStart > 0 && line > 0 {
		evidence.LineStart = document.LineStart + line - 1
		evidence.LineEnd = evidence.LineStart
	}
	return evidence
}

func graphLineExcerpt(lines []string, line int) string {
	if line < 1 || line > len(lines) {
		return ""
	}
	return lines[line-1]
}

func cosineGraphTokens(left, right map[string]int) float64 {
	numerator, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for token, value := range left {
		numerator += float64(value * right[token])
		leftNorm += float64(value * value)
	}
	for _, value := range right {
		rightNorm += float64(value * value)
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return numerator / math.Sqrt(leftNorm*rightNorm)
}

func firstGraphEvidence(values []graphEvidence, limit int) []graphEvidence {
	if len(values) <= limit {
		return values
	}
	return values[:limit]
}

func roundGraphScore(value float64) float64 {
	return math.Round(value*1000) / 1000
}

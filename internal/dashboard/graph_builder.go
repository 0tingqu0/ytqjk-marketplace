package dashboard

import (
	"math"
	"sort"
	"strconv"
	"strings"
)

func buildKnowledgeGraph(sources []graphSource, limit int, vectorAvailable bool) knowledgeGraph {
	limit = clamp(limit, 20, 160)
	documents := groupGraphDocuments(sources)
	entities, relations := aggregateGraphKnowledge(sources)
	selectedDocuments := selectGraphDocuments(documents, entities, limit)
	selectedDocumentIDs := make(map[string]struct{}, len(selectedDocuments))
	for _, document := range selectedDocuments {
		selectedDocumentIDs[document.ID] = struct{}{}
	}
	selectedEntities := selectGraphEntities(entities, selectedDocumentIDs, limit-len(selectedDocuments))
	nodes := make([]graphNode, 0, len(selectedDocuments)+len(selectedEntities))
	for _, document := range selectedDocuments {
		nodes = append(nodes, graphNode{
			ID: document.ID, Label: document.Title, Type: "document", Kind: "document",
			Path: document.Path, Scope: document.Scope, ProjectID: document.ProjectID,
			Snippet: graphSnippet(document.Content, nil), LineStart: document.LineStart, LineEnd: document.LineEnd,
		})
	}
	for _, entity := range selectedEntities {
		nodes = append(nodes, graphNode{
			ID: entity.Aggregate.ID, Label: entity.Aggregate.Label, Type: "entity", Kind: entity.Aggregate.Kind,
			Mentions: entity.Aggregate.Mentions, DocumentCount: entity.DocumentCount,
			Evidence: append([]graphEvidence(nil), entity.Aggregate.Evidence...),
		})
	}
	edges := aggregateGraphEdges(selectedDocuments, selectedEntities, relations)
	warnings := make([]string, 0, 1)
	if len(sources) == 0 {
		warnings = append(warnings, "NO_KNOWLEDGE_SOURCES")
	}
	semanticMode := "concept-hybrid"
	if vectorAvailable {
		semanticMode = "hybrid-vector"
	}
	return knowledgeGraph{
		Schema: 1, Nodes: nodes, Edges: edges,
		Stats: graphStats{Documents: len(selectedDocuments), Entities: len(selectedEntities), Relations: len(edges), SourceChunks: len(sources)},
		Capabilities: graphCapabilities{
			EntityExtraction: "go-rules-v1", SemanticSearch: semanticMode, Embedding: vectorAvailable,
			Recommendations: true, PathExploration: true,
		},
		Warnings: warnings,
	}
}

func aggregateGraphKnowledge(sources []graphSource) (map[string]*graphEntityAggregate, []graphExtractedRelation) {
	entities := map[string]*graphEntityAggregate{}
	relations := make([]graphExtractedRelation, 0)
	seenEntities := map[string]struct{}{}
	seenRelations := map[string]struct{}{}
	for _, source := range sources {
		documentID := graphDocumentID(source.Scope, source.Path)
		lineOffset := 0
		if source.LineStart > 0 {
			lineOffset = source.LineStart - 1
		}
		extractedEntities, extractedRelations := extractGraphKnowledge(source.Content, lineOffset)
		for _, item := range extractedEntities {
			identifier := graphEntityID(item.Label)
			occurrence := identifier + "\x00" + documentID + "\x00" + strconv.Itoa(item.Line)
			if _, duplicate := seenEntities[occurrence]; duplicate {
				continue
			}
			seenEntities[occurrence] = struct{}{}
			row := entities[identifier]
			if row == nil {
				row = &graphEntityAggregate{
					ID: identifier, Label: item.Label, Kind: item.Kind,
					Documents: map[string]struct{}{}, Evidence: make([]graphEvidence, 0, 3),
				}
				entities[identifier] = row
			} else if graphKindRank(item.Kind) < graphKindRank(row.Kind) {
				row.Kind = item.Kind
			}
			row.Mentions++
			row.Documents[documentID] = struct{}{}
			if len(row.Evidence) < 3 {
				row.Evidence = append(row.Evidence, graphSourceEvidence(source, item.Line, item.Excerpt))
			}
		}
		for _, item := range extractedRelations {
			sourceID, targetID := graphEntityID(item.Source), graphEntityID(item.Target)
			occurrence := sourceID + "\x00" + targetID + "\x00" + item.Type + "\x00" + documentID + "\x00" + strconv.Itoa(item.Line)
			if _, duplicate := seenRelations[occurrence]; duplicate {
				continue
			}
			seenRelations[occurrence] = struct{}{}
			relations = append(relations, graphExtractedRelation{
				Source: sourceID, Target: targetID, Type: item.Type, Label: item.Label,
				Confidence: item.Confidence, Evidence: graphSourceEvidence(source, item.Line, item.Excerpt),
			})
		}
	}
	return entities, relations
}

func graphSourceEvidence(source graphSource, line int, excerpt string) graphEvidence {
	if source.LineStart == 0 {
		line = 0
	}
	return graphEvidence{
		Path: source.Path, Scope: source.Scope, LineStart: line, LineEnd: line,
		Excerpt: truncateRunes(strings.TrimSpace(excerpt), 240),
	}
}

func selectGraphDocuments(documents []graphDocument, entities map[string]*graphEntityAggregate, limit int) []graphDocument {
	counts := map[string]int{}
	for _, entity := range entities {
		for document := range entity.Documents {
			counts[document]++
		}
	}
	ranked := append([]graphDocument(nil), documents...)
	sort.Slice(ranked, func(i, j int) bool {
		left, right := ranked[i], ranked[j]
		if counts[left.ID] != counts[right.ID] {
			return counts[left.ID] > counts[right.ID]
		}
		if (left.Scope == "global") != (right.Scope == "global") {
			return left.Scope == "global"
		}
		if left.Scope != right.Scope {
			return left.Scope < right.Scope
		}
		return left.Path < right.Path
	})
	maximum := min(36, max(4, limit/3))
	if len(ranked) > maximum {
		ranked = ranked[:maximum]
	}
	return ranked
}

func selectGraphEntities(entities map[string]*graphEntityAggregate, selectedDocuments map[string]struct{}, limit int) []selectedGraphEntity {
	if limit <= 0 {
		return []selectedGraphEntity{}
	}
	result := make([]selectedGraphEntity, 0, len(entities))
	for _, entity := range entities {
		documents := make([]string, 0, len(entity.Documents))
		for document := range entity.Documents {
			if _, selected := selectedDocuments[document]; selected {
				documents = append(documents, document)
			}
		}
		if len(documents) == 0 {
			continue
		}
		sort.Strings(documents)
		result = append(result, selectedGraphEntity{Aggregate: entity, Documents: documents, DocumentCount: len(documents)})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := result[i], result[j]
		if graphKindRank(left.Aggregate.Kind) != graphKindRank(right.Aggregate.Kind) {
			return graphKindRank(left.Aggregate.Kind) < graphKindRank(right.Aggregate.Kind)
		}
		if left.DocumentCount != right.DocumentCount {
			return left.DocumentCount > right.DocumentCount
		}
		if left.Aggregate.Mentions != right.Aggregate.Mentions {
			return left.Aggregate.Mentions > right.Aggregate.Mentions
		}
		return strings.ToLower(left.Aggregate.Label) < strings.ToLower(right.Aggregate.Label)
	})
	if len(result) > limit {
		result = result[:limit]
	}
	return result
}

func graphKindRank(kind string) int {
	switch kind {
	case "topic":
		return 0
	case "concept":
		return 1
	case "term":
		return 2
	default:
		return 3
	}
}

func aggregateGraphEdges(documents []graphDocument, entities []selectedGraphEntity, relations []graphExtractedRelation) []graphEdge {
	edges := make([]graphEdge, 0)
	selectedEntities := make(map[string]struct{}, len(entities))
	for _, entity := range entities {
		selectedEntities[entity.Aggregate.ID] = struct{}{}
		for _, documentID := range entity.Documents {
			evidence := make([]graphEvidence, 0, 2)
			for _, item := range entity.Aggregate.Evidence {
				if graphDocumentID(item.Scope, item.Path) == documentID && len(evidence) < 2 {
					evidence = append(evidence, item)
				}
			}
			confidence := math.Min(1, 0.62+float64(entity.Aggregate.Mentions)*0.04)
			edges = append(edges, newGraphEdge(documentID, entity.Aggregate.ID, "mentions", "提及", confidence, evidence, 1))
		}
	}
	type relationKey struct{ source, target, type_ string }
	grouped := map[relationKey][]graphExtractedRelation{}
	for _, relation := range relations {
		_, sourceSelected := selectedEntities[relation.Source]
		_, targetSelected := selectedEntities[relation.Target]
		if sourceSelected && targetSelected {
			key := relationKey{source: relation.Source, target: relation.Target, type_: relation.Type}
			grouped[key] = append(grouped[key], relation)
		}
	}
	keys := make([]relationKey, 0, len(grouped))
	for key := range grouped {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].source != keys[j].source {
			return keys[i].source < keys[j].source
		}
		if keys[i].target != keys[j].target {
			return keys[i].target < keys[j].target
		}
		return keys[i].type_ < keys[j].type_
	})
	for _, key := range keys {
		rows := grouped[key]
		confidence := 0.0
		evidence := make([]graphEvidence, 0, min(3, len(rows)))
		for _, row := range rows {
			if row.Confidence > confidence {
				confidence = row.Confidence
			}
			if len(evidence) < 3 {
				evidence = append(evidence, row.Evidence)
			}
		}
		edges = append(edges, newGraphEdge(key.source, key.target, key.type_, rows[0].Label, confidence, evidence, len(rows)))
	}
	return append(edges, similarGraphEdges(documents)...)
}

func similarGraphEdges(documents []graphDocument) []graphEdge {
	type candidate struct {
		score       float64
		left, right graphDocument
	}
	candidates := make([]candidate, 0)
	for leftIndex, left := range documents {
		for _, right := range documents[leftIndex+1:] {
			score := cosineGraphTokens(left.Tokens, right.Tokens)
			if score >= 0.12 {
				candidates = append(candidates, candidate{score: score, left: left, right: right})
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if candidates[i].left.ID != candidates[j].left.ID {
			return candidates[i].left.ID < candidates[j].left.ID
		}
		return candidates[i].right.ID < candidates[j].right.ID
	})
	counts := map[string]int{}
	edges := make([]graphEdge, 0)
	for _, candidate := range candidates {
		if counts[candidate.left.ID] >= 2 || counts[candidate.right.ID] >= 2 {
			continue
		}
		counts[candidate.left.ID]++
		counts[candidate.right.ID]++
		edges = append(edges, newGraphEdge(candidate.left.ID, candidate.right.ID, "similar_to", "相似", candidate.score, []graphEvidence{}, 1))
	}
	return edges
}

func newGraphEdge(source, target, edgeType, label string, confidence float64, evidence []graphEvidence, weight int) graphEdge {
	return graphEdge{
		ID:     stableGraphID("edge", source+"\x00"+target+"\x00"+edgeType),
		Source: source, Target: target, Type: edgeType, Label: label,
		Confidence: math.Round(confidence*1000) / 1000, Weight: weight,
		Evidence: append([]graphEvidence(nil), evidence...),
	}
}

func cosineGraphTokens(left, right map[string]int) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	numerator, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for token, value := range left {
		leftNorm += float64(value * value)
		numerator += float64(value * right[token])
	}
	for _, value := range right {
		rightNorm += float64(value * value)
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return numerator / (math.Sqrt(leftNorm) * math.Sqrt(rightNorm))
}

func graphSnippet(content string, terms map[string]struct{}) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if len(terms) == 0 {
			return truncateRunes(line, 260)
		}
		lineTokens := semanticGraphTokens(line)
		for term := range terms {
			if _, matched := lineTokens[term]; matched {
				return truncateRunes(line, 260)
			}
		}
	}
	return ""
}

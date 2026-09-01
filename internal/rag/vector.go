package rag

import (
	"errors"
	"hash/fnv"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

const (
	vectorSchemaVersion = 1
	vectorDimensions    = 2048
	vectorBackend       = "go-hashed-chargram-v1"
)

type VectorComponent struct {
	Index int     `json:"index"`
	Value float64 `json:"value"`
}

type VectorRecord struct {
	ID         string            `json:"id"`
	Components []VectorComponent `json:"components"`
}

type VectorIndex struct {
	SchemaVersion     int            `json:"schema_version"`
	Backend           string         `json:"backend"`
	Dimensions        int            `json:"dimensions"`
	SourceFingerprint string         `json:"source_fingerprint"`
	Records           []VectorRecord `json:"records"`
}

func writeVectors(directory string, chunks []Chunk, fingerprint, mode string) (map[string]any, error) {
	path := filepath.Join(directory, "vectors.json")
	if mode == "off" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return map[string]any{
			"enabled": false, "status": "DISABLED", "requested_mode": mode,
			"backend": vectorBackend, "dimensions": vectorDimensions, "records": 0,
		}, nil
	}
	records := make([]VectorRecord, 0, len(chunks))
	for _, chunk := range chunks {
		records = append(records, VectorRecord{ID: chunk.ID, Components: vectorize(chunk.Path + "\n" + chunk.Content)})
	}
	index := VectorIndex{
		SchemaVersion: vectorSchemaVersion, Backend: vectorBackend, Dimensions: vectorDimensions,
		SourceFingerprint: fingerprint, Records: records,
	}
	if err := safeio.WriteJSON(path, index); err != nil {
		return nil, err
	}
	return map[string]any{
		"enabled": true, "status": "READY", "requested_mode": mode,
		"backend": vectorBackend, "dimensions": vectorDimensions, "records": len(records),
	}, nil
}

func readVectors(directory, fingerprint string) (map[string][]VectorComponent, bool) {
	var index VectorIndex
	if err := safeio.ReadJSON(filepath.Join(directory, "vectors.json"), &index); err != nil {
		return nil, false
	}
	if index.SchemaVersion != vectorSchemaVersion || index.Backend != vectorBackend ||
		index.Dimensions != vectorDimensions || index.SourceFingerprint != fingerprint {
		return nil, false
	}
	result := make(map[string][]VectorComponent, len(index.Records))
	for _, record := range index.Records {
		if record.ID == "" || len(record.Components) == 0 {
			continue
		}
		if _, duplicate := result[record.ID]; duplicate || !validComponents(record.Components) {
			return nil, false
		}
		result[record.ID] = record.Components
	}
	return result, true
}

func validComponents(components []VectorComponent) bool {
	previous := -1
	for _, component := range components {
		if component.Index <= previous || component.Index < 0 || component.Index >= vectorDimensions ||
			math.IsNaN(component.Value) || math.IsInf(component.Value, 0) || component.Value == 0 {
			return false
		}
		previous = component.Index
	}
	return true
}

func vectorize(value string) []VectorComponent {
	counts := map[int]float64{}
	for _, token := range vectorTokens(value) {
		hash := fnv.New32a()
		_, _ = hash.Write([]byte(token))
		sum := hash.Sum32()
		index := int(sum % vectorDimensions)
		sign := 1.0
		if sum&(1<<31) != 0 {
			sign = -1
		}
		counts[index] += sign
	}
	components := make([]VectorComponent, 0, len(counts))
	norm := 0.0
	for index, count := range counts {
		if count == 0 {
			continue
		}
		value := math.Copysign(1+math.Log(math.Abs(count)), count)
		components = append(components, VectorComponent{Index: index, Value: value})
		norm += value * value
	}
	if norm == 0 {
		return nil
	}
	norm = math.Sqrt(norm)
	for index := range components {
		components[index].Value = components[index].Value / norm
	}
	sort.Slice(components, func(i, j int) bool { return components[i].Index < components[j].Index })
	return components
}

func vectorTokens(value string) []string {
	words := queryTokenPattern.FindAllString(strings.ToLower(value), -1)
	result := make([]string, 0, len(words)*4)
	for _, word := range words {
		result = append(result, "word:"+word)
		runes := []rune(word)
		for index, current := range runes {
			result = append(result, "rune:"+string(current))
			for size := 2; size <= 3 && index+size <= len(runes); size++ {
				result = append(result, "gram:"+string(runes[index:index+size]))
			}
		}
	}
	return result
}

func cosine(left, right []VectorComponent) float64 {
	leftIndex, rightIndex := 0, 0
	score := 0.0
	for leftIndex < len(left) && rightIndex < len(right) {
		switch {
		case left[leftIndex].Index < right[rightIndex].Index:
			leftIndex++
		case left[leftIndex].Index > right[rightIndex].Index:
			rightIndex++
		default:
			score += left[leftIndex].Value * right[rightIndex].Value
			leftIndex++
			rightIndex++
		}
	}
	if score < 0 {
		return 0
	}
	return math.Round(score*1e6) / 1e6
}

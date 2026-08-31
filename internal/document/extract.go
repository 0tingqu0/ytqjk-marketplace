package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tsawler/tabula"
	_ "golang.org/x/image/webp"

	securitycheck "github.com/0tingqu0/ytqjk-marketplace/internal/security"
)

const (
	MaxSourceBytes = 10 * 1024 * 1024
	chunkRunes     = 2000
)

type Chunk struct {
	ID           string   `json:"id"`
	ParentID     string   `json:"parent_id"`
	Ordinal      int      `json:"ordinal"`
	Text         string   `json:"text"`
	Digest       string   `json:"digest"`
	PageStart    int      `json:"page_start,omitempty"`
	PageEnd      int      `json:"page_end,omitempty"`
	Section      string   `json:"section,omitempty"`
	ElementTypes []string `json:"element_types,omitempty"`
}

type Result struct {
	SchemaVersion        int            `json:"schema_version"`
	State                string         `json:"state"`
	AutoApprovalEligible bool           `json:"auto_approval_eligible"`
	SourceName           string         `json:"source_name"`
	SourceSHA256         string         `json:"source_sha256"`
	Format               string         `json:"format"`
	MediaKind            string         `json:"media_kind"`
	Engine               string         `json:"engine"`
	OCRState             string         `json:"ocr_state"`
	Text                 string         `json:"text"`
	Chunks               []Chunk        `json:"chunks"`
	Warnings             []string       `json:"warnings"`
	ReviewReasons        []string       `json:"review_reasons"`
	Metadata             map[string]any `json:"metadata"`
}

func ExtractFile(path string) (Result, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Result{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return Result{}, errors.New("document source must be a regular non-symlink file")
	}
	if info.Size() > MaxSourceBytes {
		return Result{}, errors.New("document source exceeds 10 MiB")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return Result{}, err
	}
	if int64(len(content)) != info.Size() {
		return Result{}, errors.New("document source changed while being read")
	}
	return ExtractBytes(filepath.Base(path), content)
}

func ExtractBytes(name string, content []byte) (Result, error) {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "." || name == "" || len(name) > 240 || len(content) == 0 {
		return Result{}, errors.New("document source is invalid")
	}
	if len(content) > MaxSourceBytes {
		return Result{}, errors.New("document source exceeds 10 MiB")
	}
	digest := sha256.Sum256(content)
	result := Result{
		SchemaVersion: 1, State: "CANDIDATE", AutoApprovalEligible: false,
		SourceName: name, SourceSHA256: hex.EncodeToString(digest[:]),
		Format:   strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), "."),
		OCRState: "NOT_REQUIRED", Metadata: map[string]any{"bytes": len(content)},
	}
	extension := "." + result.Format
	switch {
	case textExtension(extension):
		result.MediaKind, result.Engine = "text", "go-stdlib-text-v1"
		text, err := extractText(extension, content)
		if err != nil {
			return Result{}, err
		}
		result.Text = text
		result.Chunks = chunks(result.SourceSHA256, text, nil)
	case tabulaExtension(extension):
		result.MediaKind, result.Engine = "document", "tabula-go-v1.6.14"
		text, sourceChunks, warnings, err := extractTabula(extension, content)
		if err != nil {
			return Result{}, fmt.Errorf("document extraction failed: %w", err)
		}
		result.Text, result.Warnings = text, warnings
		if len(sourceChunks) == 0 {
			result.Chunks = chunks(result.SourceSHA256, text, nil)
		} else {
			result.Chunks = normalizeTabulaChunks(result.SourceSHA256, sourceChunks)
		}
		if strings.TrimSpace(text) == "" {
			result.OCRState = "NOT_CONFIGURED"
			result.ReviewReasons = append(result.ReviewReasons, "NO_NATIVE_TEXT_OR_OCR")
		}
	case imageExtension(extension):
		result.MediaKind, result.Engine = "image", "go-image+tesseract-cli-v1"
		if err := extractImage(&result, content); err != nil {
			return Result{}, err
		}
	case extension == ".wav":
		result.MediaKind, result.Engine = "audio", "go-wave-metadata-v1"
		if err := extractWAV(&result, content); err != nil {
			return Result{}, err
		}
	case extension == ".mp3" || extension == ".m4a" || extension == ".flac" || extension == ".ogg":
		result.MediaKind, result.Engine = "audio", "go-audio-metadata-v1"
		result.Text = fmt.Sprintf("Audio file %s (%d bytes); speech transcription requires an external Go-compatible backend.", name, len(content))
		result.Chunks = chunks(result.SourceSHA256, result.Text, nil)
		result.ReviewReasons = append(result.ReviewReasons, "AUDIO_TRANSCRIPTION_NOT_CONFIGURED")
	default:
		return Result{}, fmt.Errorf("unsupported document format %q", extension)
	}
	if strings.TrimSpace(result.Text) == "" && len(result.Chunks) == 0 {
		return Result{}, errors.New("document extraction produced no auditable content")
	}
	if containsSecret(result.Text) {
		return Result{}, errors.New("document content contains a high-confidence secret")
	}
	sort.Strings(result.Warnings)
	sort.Strings(result.ReviewReasons)
	return result, nil
}

func textExtension(extension string) bool {
	switch extension {
	case ".txt", ".md", ".go", ".rs", ".java", ".c", ".h", ".cpp", ".ts", ".json", ".jsonl", ".yaml", ".yml", ".toml", ".csv", ".tsv", ".xml", ".ini", ".sh", ".ps1", ".diff", ".svg", ".sql":
		return true
	default:
		return false
	}
}

func tabulaExtension(extension string) bool {
	switch extension {
	case ".pdf", ".docx", ".odt", ".xlsx", ".pptx", ".html", ".htm", ".epub":
		return true
	default:
		return false
	}
}

func imageExtension(extension string) bool {
	switch extension {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	default:
		return false
	}
}

func extractText(extension string, content []byte) (string, error) {
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return "", errors.New("text document must be valid UTF-8 without NUL bytes")
	}
	text := strings.ReplaceAll(string(content), "\r\n", "\n")
	if extension == ".json" {
		var value any
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return "", errors.New("JSON document is invalid")
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return "", errors.New("JSON document has trailing content")
		}
		canonical, err := json.MarshalIndent(value, "", "  ")
		if err != nil {
			return "", err
		}
		text = string(canonical)
	}
	if extension == ".csv" || extension == ".tsv" {
		reader := csv.NewReader(strings.NewReader(text))
		if extension == ".tsv" {
			reader.Comma = '\t'
		}
		reader.FieldsPerRecord = -1
		records, err := reader.ReadAll()
		if err != nil {
			return "", errors.New("delimited document is invalid")
		}
		var lines []string
		for _, record := range records {
			lines = append(lines, strings.Join(record, " | "))
		}
		text = strings.Join(lines, "\n")
	}
	if strings.TrimSpace(text) == "" {
		return "", errors.New("text document is empty")
	}
	return strings.TrimSpace(text), nil
}

func extractTabula(extension string, content []byte) (text string, resultChunks []*tabulaChunk, warnings []string, err error) {
	temporary, err := os.CreateTemp("", "ytqjk-document-*"+extension)
	if err != nil {
		return "", nil, nil, err
	}
	path := temporary.Name()
	defer os.Remove(path)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return "", nil, nil, err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return "", nil, nil, err
	}
	if err := temporary.Close(); err != nil {
		return "", nil, nil, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("parser panic: %v", recovered)
		}
	}()
	collection, sourceWarnings, err := tabula.Open(path).Chunks()
	if err != nil {
		return "", nil, nil, err
	}
	for _, warning := range sourceWarnings {
		warnings = append(warnings, warning.Message)
	}
	var parts []string
	for _, source := range collection.Chunks {
		value := strings.TrimSpace(source.TextWithContext)
		if value == "" {
			value = strings.TrimSpace(source.Text)
		}
		if value == "" {
			continue
		}
		parts = append(parts, value)
		resultChunks = append(resultChunks, &tabulaChunk{
			Text: value, PageStart: source.Metadata.PageStart, PageEnd: source.Metadata.PageEnd,
			Section: source.Metadata.SectionTitle, ElementTypes: append([]string(nil), source.Metadata.ElementTypes...),
		})
	}
	return strings.Join(parts, "\n\n"), resultChunks, warnings, nil
}

type tabulaChunk struct {
	Text         string
	PageStart    int
	PageEnd      int
	Section      string
	ElementTypes []string
}

func normalizeTabulaChunks(parentID string, source []*tabulaChunk) []Chunk {
	result := make([]Chunk, 0, len(source))
	for _, item := range source {
		current := chunks(parentID, item.Text, item)
		for index := range current {
			current[index].Ordinal = len(result) + index + 1
			identifier := sha256.Sum256([]byte(parentID + ":" + fmt.Sprint(current[index].Ordinal) + ":" + current[index].Digest))
			current[index].ID = hex.EncodeToString(identifier[:])
		}
		result = append(result, current...)
	}
	return result
}

func chunks(parentID, text string, metadata *tabulaChunk) []Chunk {
	runes := []rune(strings.TrimSpace(text))
	result := make([]Chunk, 0, (len(runes)+chunkRunes-1)/chunkRunes)
	for start := 0; start < len(runes); start += chunkRunes {
		end := start + chunkRunes
		if end > len(runes) {
			end = len(runes)
		}
		value := strings.TrimSpace(string(runes[start:end]))
		if value == "" {
			continue
		}
		digest := sha256.Sum256([]byte(value))
		identifier := sha256.Sum256([]byte(parentID + ":" + fmt.Sprint(len(result)+1) + ":" + hex.EncodeToString(digest[:])))
		item := Chunk{ID: hex.EncodeToString(identifier[:]), ParentID: parentID, Ordinal: len(result) + 1, Text: value, Digest: hex.EncodeToString(digest[:])}
		if metadata != nil {
			item.PageStart, item.PageEnd, item.Section = metadata.PageStart, metadata.PageEnd, metadata.Section
			item.ElementTypes = append([]string(nil), metadata.ElementTypes...)
		}
		result = append(result, item)
	}
	return result
}

func extractImage(result *Result, content []byte) error {
	configuration, format, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		return errors.New("image document is invalid")
	}
	result.Format = format
	result.Metadata["width"] = configuration.Width
	result.Metadata["height"] = configuration.Height
	result.Text = fmt.Sprintf("Image %s, %d by %d pixels.", strings.ToUpper(format), configuration.Width, configuration.Height)
	path, err := exec.LookPath("tesseract")
	if err != nil {
		result.OCRState = "NOT_CONFIGURED"
		result.ReviewReasons = append(result.ReviewReasons, "OCR_NOT_CONFIGURED")
		result.Chunks = chunks(result.SourceSHA256, result.Text, nil)
		return nil
	}
	temporary, err := os.CreateTemp("", "ytqjk-ocr-*"+filepath.Ext(result.SourceName))
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	arguments := []string{temporaryPath, "stdout", "--dpi", "300"}
	if language := strings.TrimSpace(os.Getenv("YTQJK_TESSERACT_LANG")); language != "" {
		arguments = append(arguments, "-l", language)
	}
	command := exec.CommandContext(ctx, path, arguments...)
	command.Stderr = io.Discard
	output, err := command.Output()
	if ctx.Err() != nil {
		result.OCRState = "TIMEOUT"
		result.ReviewReasons = append(result.ReviewReasons, "OCR_TIMEOUT")
	} else if err != nil || !utf8.Valid(output) {
		result.OCRState = "FAILED"
		result.ReviewReasons = append(result.ReviewReasons, "OCR_FAILED")
	} else if text := strings.TrimSpace(string(output)); text != "" {
		result.OCRState = "READY"
		result.Text += "\n\nOCR text:\n" + text
	} else {
		result.OCRState = "EMPTY"
		result.ReviewReasons = append(result.ReviewReasons, "OCR_EMPTY")
	}
	result.Chunks = chunks(result.SourceSHA256, result.Text, nil)
	return nil
}

func extractWAV(result *Result, content []byte) error {
	if len(content) < 44 || string(content[:4]) != "RIFF" || string(content[8:12]) != "WAVE" {
		return errors.New("WAV document is invalid")
	}
	channels := int(uint16(content[22]) | uint16(content[23])<<8)
	sampleRate := int(uint32(content[24]) | uint32(content[25])<<8 | uint32(content[26])<<16 | uint32(content[27])<<24)
	byteRate := int(uint32(content[28]) | uint32(content[29])<<8 | uint32(content[30])<<16 | uint32(content[31])<<24)
	duration := 0.0
	if byteRate > 0 {
		duration = float64(len(content)-44) / float64(byteRate)
	}
	result.Metadata["channels"] = channels
	result.Metadata["sample_rate"] = sampleRate
	result.Metadata["duration_seconds"] = duration
	result.Text = fmt.Sprintf("WAV audio, %d channels, %d Hz, %.3f seconds.", channels, sampleRate, duration)
	result.Chunks = chunks(result.SourceSHA256, result.Text, nil)
	result.ReviewReasons = append(result.ReviewReasons, "AUDIO_TRANSCRIPTION_NOT_CONFIGURED")
	return nil
}

func containsSecret(value string) bool {
	return securitycheck.ContainsHighConfidenceSecret(value)
}

package dashboard

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/0tingqu0/ytqjk-marketplace/internal/safeio"
)

func readJSON(request *http.Request, target any) error {
	body := io.LimitReader(request.Body, maxBodyBytes+1)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func readRequestBody(request *http.Request) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(request.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, errors.New("request body too large")
	}
	return body, nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	data, _ := json.Marshal(value)
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", securityPolicy)
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}

func writeError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{"ok": false, "error": APIError{Code: code, Message: message}})
}

func safeRoute(path string) string {
	if strings.HasPrefix(path, "/api/intake/jobs/") {
		return "/api/intake/jobs/{job_id}"
	}
	if strings.HasPrefix(path, "/api/libraries/") {
		return "/api/libraries/{operation}"
	}
	if strings.HasPrefix(path, "/api/") {
		return path
	}
	return "/assets"
}

func safeDocumentPath(root, relative string) (string, error) {
	relative = filepath.Clean(filepath.FromSlash(relative))
	if filepath.IsAbs(relative) || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("invalid relative path")
	}
	return safeio.Contained(root, filepath.Join(root, relative))
}

func safeIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.') {
			return false
		}
	}
	return true
}

func clamp(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

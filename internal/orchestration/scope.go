package orchestration

import (
	"errors"
	"path/filepath"
	"sort"
	"strings"
)

func canonicalScope(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = filepath.ToSlash(value)
		cleaned := filepath.ToSlash(filepath.Clean(value))
		lower := strings.ToLower(cleaned)
		if value == "" || value != cleaned || filepath.IsAbs(value) || strings.Contains(value, "\\") || strings.Contains(value, ":") || value == ".." || strings.HasPrefix(value, "../") || sensitiveScope(lower) {
			return nil, errors.New("scope contains sensitive or unsafe path")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func canonicalCapabilities(values []string) ([]string, error) {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "run:lifecycle" {
			return nil, errors.New("capability is invalid")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result, nil
}

func directorGrantInvalid(role string, reads, writes []string, mutation bool, capabilities []string) bool {
	if role != "director" && role != "controller" {
		return false
	}
	if len(reads) != 0 || len(writes) != 0 || mutation {
		return true
	}
	for _, capability := range capabilities {
		if capability != "run:lifecycle" {
			return true
		}
	}
	return false
}

func sensitiveScope(value string) bool {
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if sensitiveScopeDirectories[part] {
			return true
		}
	}
	base := parts[len(parts)-1]
	if sensitiveScopeNames[base] || sensitiveConfigPattern.MatchString(base) || strings.HasPrefix(base, ".env") {
		return true
	}
	for _, ending := range sensitiveScopeEndings {
		if strings.HasSuffix(base, ending) {
			return true
		}
	}
	return false
}

func validRole(value string) bool {
	for _, role := range []string{"director", "controller", "worker", "reviewer", "git"} {
		if value == role {
			return true
		}
	}
	return false
}

func subset(values, allowed []string) bool {
	set := map[string]bool{}
	for _, value := range allowed {
		set[value] = true
	}
	for _, value := range values {
		if !set[value] {
			return false
		}
	}
	return true
}

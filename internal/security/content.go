package security

import (
	"path/filepath"
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)-----BEGIN (?:(?:OPENSSH|RSA|EC|DSA|PGP) )?PRIVATE KEY(?: BLOCK)?-----`),
	regexp.MustCompile(`\bgh[opusr]_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\bsk_(?:live|test)_[0-9A-Za-z]{16,}\b`),
	regexp.MustCompile(`\bsk-(?:proj-)?[0-9A-Za-z_-]{20,}\b`),
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
	regexp.MustCompile(`[A-Za-z][A-Za-z0-9+.-]*://[^/\s:@]+:[^/\s@]+@`),
	regexp.MustCompile(`(?i)authorization\s*:\s*bearer\s+\S+`),
}

var secretAssignment = regexp.MustCompile(`(?im)^\s*(?:api[_-]?key|access[_-]?token|auth[_-]?token|client[_-]?secret|secret|secret[_-]?key|token|password|passwd)\s*[:=]\s*["']?([^\s"'#;]{12,})`)
var sensitiveConfig = regexp.MustCompile(`(?i)^(?:auth|credential|credentials|secret|secrets|token|tokens)\.(?:cfg|conf|ini|json|properties|toml|ya?ml)$`)

var safeValueMarkers = []string{"changeme", "dummy", "example", "placeholder", "redacted", "sample", "your_"}

var skippedPathParts = map[string]bool{
	".git": true, ".venv": true, "node_modules": true, "vendor": true,
	"dist": true, "build": true, "coverage": true, "__pycache__": true,
}

var secretPathParts = map[string]bool{
	".aws": true, ".azure": true, ".cargo": true, ".docker": true,
	".gnupg": true, ".kube": true, ".m2": true, ".ssh": true, ".terraform": true,
}

var sensitiveNames = map[string]bool{
	".authinfo": true, ".git-credentials": true, ".my.cnf": true, ".netrc": true,
	".npmrc": true, ".pgpass": true, ".pypirc": true, ".yarnrc": true,
	".yarnrc.yml": true, "_netrc": true, "auth.json": true, "credentials": true,
	"credentials.json": true, "credentials.toml": true, "gradle.properties": true,
	"id_ed25519": true, "id_rsa": true, "kubeconfig": true, "nuget.config": true,
	"secret.json": true, "secrets.json": true, "service-account.json": true,
	"service_account.json": true, "settings-security.xml": true, "token.json": true,
	"tokens.json": true,
}

var sensitiveEndings = []string{
	".age", ".asc", ".gpg", ".jks", ".kdbx", ".key", ".keystore", ".p12",
	".pem", ".pfx", ".ovpn", ".tfstate", ".tfstate.backup", ".tfvars", ".tfvars.json",
}

// ContainsHighConfidenceSecret detects credential forms that should never be
// copied into indexes, candidates, logs, or peer responses.
func ContainsHighConfidenceSecret(value string) bool {
	for _, pattern := range secretPatterns {
		if pattern.MatchString(value) {
			return true
		}
	}
	for _, match := range secretAssignment.FindAllStringSubmatch(value, -1) {
		candidate := strings.TrimSpace(match[1])
		lower := strings.ToLower(candidate)
		if strings.HasPrefix(candidate, "${") || strings.HasPrefix(candidate, "{{") || strings.HasPrefix(candidate, "<") {
			continue
		}
		safe := false
		for _, marker := range safeValueMarkers {
			if strings.Contains(lower, marker) {
				safe = true
				break
			}
		}
		if !safe && distinctRunes(candidate) >= 4 {
			return true
		}
	}
	return false
}

// IsSensitivePath excludes common credential stores and generated dependency
// trees before any file content is opened.
func IsSensitivePath(value string) bool {
	value = strings.ReplaceAll(strings.ToLower(value), "\\", "/")
	for _, part := range strings.Split(value, "/") {
		if skippedPathParts[part] || secretPathParts[part] {
			return true
		}
	}
	name := strings.ToLower(filepath.Base(filepath.FromSlash(value)))
	if sensitiveNames[name] || sensitiveConfig.MatchString(name) || strings.HasPrefix(name, ".env") {
		return true
	}
	for _, ending := range sensitiveEndings {
		if strings.HasSuffix(name, ending) {
			return true
		}
	}
	return false
}

func distinctRunes(value string) int {
	seen := map[rune]bool{}
	for _, character := range value {
		seen[character] = true
	}
	return len(seen)
}

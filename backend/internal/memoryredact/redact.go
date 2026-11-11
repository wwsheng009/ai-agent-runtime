// Package memoryredact centralizes secret-shape detection for memory writes
// and diagnostic previews. It intentionally returns rule names, never raw
// secret values.
package memoryredact

import (
	"regexp"
	"strings"
)

type rule struct {
	name string
	re   *regexp.Regexp
}

var rules = []rule{
	{name: "openai_key", re: regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{10,}\b`)},
	{name: "bearer_token", re: regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{16,}`)},
	{name: "github_token", re: regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`)},
	{name: "aws_access_key", re: regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`)},
	{name: "private_key", re: regexp.MustCompile(`(?s)-----BEGIN (?:RSA |OPENSSH |EC )?PRIVATE KEY-----.*?-----END (?:RSA |OPENSSH |EC )?PRIVATE KEY-----`)},
	{name: "password_assignment", re: regexp.MustCompile(`(?i)\b(?:password|passwd|pwd)\s*[=:]\s*["']?[^\s"']{4,}`)},
	{name: "api_key_assignment", re: regexp.MustCompile(`(?i)\b(?:api[_-]?key|access[_-]?token|secret[_-]?key)\s*[=:]\s*["']?[^\s"']{8,}`)},
	{name: "url_query_secret", re: regexp.MustCompile(`(?i)[?&](?:access_token|api_key|token|secret)=[^&\s]{8,}`)},
	{name: "env_secret_line", re: regexp.MustCompile(`(?im)^[A-Z0-9_]*(?:TOKEN|SECRET|PASSWORD|API_KEY)[A-Z0-9_]*\s*=.*$`)},
}

// DetectSecret returns the first matching rule name, or "".
func DetectSecret(text string) string {
	for _, candidate := range rules {
		if candidate.re.MatchString(text) {
			return candidate.name
		}
	}
	return ""
}

// Redact replaces secret-shaped spans and returns the rule names that fired.
func Redact(text string) (string, []string) {
	matched := make([]string, 0, 1)
	for _, candidate := range rules {
		if !candidate.re.MatchString(text) {
			continue
		}
		matched = append(matched, candidate.name)
		text = candidate.re.ReplaceAllString(text, "[REDACTED:"+candidate.name+"]")
	}
	return text, matched
}

// ContainsInstructionLikeText is a cheap heuristic used before persisting
// model-generated content. It never changes the text; callers decide policy.
func ContainsInstructionLikeText(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "ignore previous instructions") ||
		strings.Contains(lower, "ignore all previous") ||
		strings.Contains(lower, "system prompt") ||
		strings.Contains(lower, "忽略之前") ||
		strings.Contains(lower, "忽略以上")
}

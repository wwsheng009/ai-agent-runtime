package memoryruntime

import (
	"regexp"
	"strings"
)

type redactionRule struct {
	name string
	re   *regexp.Regexp
}

var redactionRules = []redactionRule{
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

func DetectSecret(text string) string {
	for _, rule := range redactionRules {
		if rule.re.MatchString(text) {
			return rule.name
		}
	}
	return ""
}

// RedactForPrompt removes secret-shaped spans before the transcript reaches an
// extraction model. It returns only rule names, never raw secret values.
func RedactForPrompt(text string) (string, []string) {
	rules := make([]string, 0, 1)
	for _, rule := range redactionRules {
		if !rule.re.MatchString(text) {
			continue
		}
		rules = append(rules, rule.name)
		text = rule.re.ReplaceAllString(text, "[REDACTED:"+rule.name+"]")
	}
	return text, rules
}

func containsInstructionLikeMemory(text string) bool {
	lower := strings.ToLower(strings.TrimSpace(text))
	return strings.Contains(lower, "ignore previous instructions") ||
		strings.Contains(lower, "ignore all previous") ||
		strings.Contains(lower, "system prompt") ||
		strings.Contains(lower, "忽略之前") ||
		strings.Contains(lower, "忽略以上")
}

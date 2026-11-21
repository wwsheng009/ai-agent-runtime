package memoryredact

import (
	"strings"
	"testing"
)

func TestDetectAndRedactSecretShapes(t *testing.T) {
	text := "api_key=supersecretvalue123"
	if rule := DetectSecret(text); rule != "api_key_assignment" {
		t.Fatalf("DetectSecret = %q", rule)
	}
	redacted, rules := Redact("https://example.test/?token=supersecretvalue123")
	if len(rules) == 0 || strings.Contains(redacted, "supersecretvalue123") ||
		!strings.Contains(redacted, "REDACTED") {
		t.Fatalf("Redact = %q, %v", redacted, rules)
	}
}

func TestDetectSecretRuleCoverageAndFastReject(t *testing.T) {
	tests := []struct {
		text string
		rule string
	}{
		{"sk-" + strings.Repeat("a", 20), "openai_key"},
		{"Authorization: Bearer " + strings.Repeat("a", 20), "bearer_token"},
		{"ghp_" + strings.Repeat("a", 20), "github_token"},
		{"AKIA" + strings.Repeat("A", 16), "aws_access_key"},
		{"-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----", "private_key"},
		{"password=supersecret", "password_assignment"},
		{"api_key=supersecretvalue123", "api_key_assignment"},
		{"https://example.test/?token=supersecretvalue123", "url_query_secret"},
		{"MY_SECRET_KEY=supersecretvalue", "env_secret_line"},
		{"ordinary deployment note", ""},
		{"ordinary deployment note about a tokenizer", ""},
	}
	for _, test := range tests {
		if got := DetectSecret(test.text); got != test.rule {
			t.Fatalf("DetectSecret(%q) = %q, want %q", test.text, got, test.rule)
		}
	}
}

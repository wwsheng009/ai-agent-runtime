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

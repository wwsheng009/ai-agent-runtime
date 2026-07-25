package buildinfo

import (
	"strings"
	"testing"
)

func TestBuildUserAgentDefaultShape(t *testing.T) {
	got := BuildUserAgent("aicli", "v1.2.3", "windows", "amd64")
	want := "aicli/v1.2.3 (windows; amd64)"
	if got != want {
		t.Fatalf("User-Agent = %q, want %q", got, want)
	}
}

func TestBuildUserAgentFallsBackOnEmptyComponents(t *testing.T) {
	got := BuildUserAgent("", "", "", "")
	if !strings.HasPrefix(got, DefaultOriginator+"/") {
		t.Fatalf("expected originator prefix, got %q", got)
	}
	if !strings.Contains(got, "(unknown; unknown)") {
		t.Fatalf("expected unknown platform tokens, got %q", got)
	}
}

func TestBuildUserAgentSanitizesInvalidCharacters(t *testing.T) {
	got := BuildUserAgent("ai cli\n", "1.0\x00", "darwin", "arm64")
	if strings.ContainsAny(got, "\n\x00") {
		t.Fatalf("User-Agent still contains invalid characters: %q", got)
	}
	if !strings.Contains(got, "ai_cli") {
		t.Fatalf("expected sanitized originator token, got %q", got)
	}
	if !isValidHTTPHeaderValue(got) {
		t.Fatalf("User-Agent is not a valid HTTP header value: %q", got)
	}
}

func TestUserAgentIsStableAndValid(t *testing.T) {
	first := UserAgent()
	second := UserAgent()
	if first == "" {
		t.Fatal("UserAgent() returned empty string")
	}
	if first != second {
		t.Fatalf("UserAgent() not stable: %q vs %q", first, second)
	}
	if !isValidHTTPHeaderValue(first) {
		t.Fatalf("UserAgent() invalid header value: %q", first)
	}
	if !strings.HasPrefix(first, Originator()+"/") {
		t.Fatalf("UserAgent() missing originator prefix: %q", first)
	}
}

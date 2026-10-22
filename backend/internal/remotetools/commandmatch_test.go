package remotetools

import (
	"strings"
	"testing"
)

func TestNormalizeCommandLineUsesProgramIdentity(t *testing.T) {
	cases := map[string]string{
		"  GIT   status  ":       "git status",
		"./deploy.sh --force":    "deploy.sh --force",
		"/opt/tools/deploy.sh x": "deploy.sh x",
		`bash -c "echo hello"`:   `bash -c echo hello`,
		"":                       "",
	}
	for input, want := range cases {
		if got := normalizeCommandLine(input); got != want {
			t.Fatalf("normalizeCommandLine(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestWildcardMatch(t *testing.T) {
	cases := []struct {
		value, pattern string
		want           bool
	}{
		{"deploy238_a.sh", "deploy238_*.sh", true},
		{"deploy238_a.sh.bak", "deploy238_*.sh", false},
		{"deploy238_ab.sh", "deploy238_?.sh", false},
		{"deploy238_a.sh", "deploy238_?.sh", true},
		{"anything", "*", true},
		{"", "*", true},
		{"abc", "a?c", true},
		{"abc", "a*d", false},
		{"a/b/c", "a/*/c", true},
		{"a-X-c", "a-*-c", true},
	}
	for _, tc := range cases {
		if got := wildcardMatch(tc.value, tc.pattern); got != tc.want {
			t.Fatalf("wildcardMatch(%q, %q) = %v, want %v", tc.value, tc.pattern, got, tc.want)
		}
	}
}

func TestCommandPatternMatchesIsFullString(t *testing.T) {
	if !commandPatternMatches("deploy238_a.sh", "deploy238_*.sh") {
		t.Fatal("expected script family to match")
	}
	if commandPatternMatches("deploy238_a.sh --force", "deploy238_*.sh") {
		t.Fatal("a pattern without a trailing wildcard must not match extra args")
	}
	if !commandPatternMatches("deploy238_a.sh --force", "deploy238_*.sh *") {
		t.Fatal("expected trailing wildcard to cover args")
	}
	if !commandPatternMatches("deploy238_a.sh a b", "deploy238_*.sh *") {
		t.Fatal("trailing wildcard must cover multiple args (it may span spaces)")
	}
	if commandPatternMatches("deploy238_a.sh", "deploy238_*.sh *") {
		t.Fatal("a pattern with a required space must not match a command without args")
	}
	if !commandPatternMatches("systemctl restart itsm-backend", "systemctl restart itsm-*") {
		t.Fatal("expected service suffix pattern to match")
	}
	if commandPatternMatches("systemctl stop itsm-backend", "systemctl restart itsm-*") {
		t.Fatal("different subcommand must not match")
	}
}

func TestNormalizeCommandPatterns(t *testing.T) {
	got := normalizeCommandPatterns([]string{"  Deploy238_*.SH  ", "deploy238_*.sh", "", "A  B"})
	want := []string{"deploy238_*.sh", "a b"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestValidateCommandPatterns(t *testing.T) {
	if err := validateCommandPatterns("allowed", []string{"ok *"}); err != nil {
		t.Fatalf("valid pattern rejected: %v", err)
	}
	if err := validateCommandPatterns("allowed", []string{""}); err == nil {
		t.Fatal("empty pattern must be rejected")
	}
	if err := validateCommandPatterns("allowed", []string{"a\nb"}); err == nil {
		t.Fatal("newline pattern must be rejected")
	}
	if err := validateCommandPatterns("allowed", []string{strings.Repeat("a", maxCommandPatternLength+1)}); err == nil {
		t.Fatal("oversized pattern must be rejected")
	}
}

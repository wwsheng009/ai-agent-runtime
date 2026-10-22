package remotetools

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/shellrisk"
)

// maxCommandPatternLength bounds one pattern so a malformed policy cannot turn
// every command check into a long scan.
const maxCommandPatternLength = 512

// normalizeCommandPatterns trims, lowercases and deduplicates patterns.
//
// Patterns use shell-style '*' and '?' wildcards but are matched against the
// whole normalized command line, not token by token. Keeping normalization in
// one place makes "git  status" and "GIT STATUS" the same policy input.
func normalizeCommandPatterns(input []string) []string {
	if len(input) == 0 {
		return nil
	}
	out := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, item := range input {
		normalized := normalizeCommandLine(item)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true
		out = append(out, normalized)
	}
	return out
}

// validateCommandPatterns rejects patterns that cannot be matched predictably.
func validateCommandPatterns(field string, patterns []string) error {
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) == "" {
			return fmt.Errorf("%s contains an empty pattern", field)
		}
		if len(pattern) > maxCommandPatternLength {
			return fmt.Errorf("%s pattern %q is longer than %d bytes", field, pattern, maxCommandPatternLength)
		}
		if strings.ContainsAny(pattern, "\r\n") {
			return fmt.Errorf("%s pattern %q contains a newline", field, pattern)
		}
	}
	return nil
}

// normalizeCommandLine produces the canonical form both commands and patterns
// are compared in: tokens are unquoted by shellrisk.Fields, joined with one
// space, and lowercased.
func normalizeCommandLine(command string) string {
	fields := shellrisk.Fields(strings.TrimSpace(command))
	if len(fields) == 0 {
		return ""
	}
	// Match policies on the program identity, not on how the caller spelled its
	// path: "./deploy.sh", "/opt/tools/deploy.sh" and "deploy.sh" are one command.
	fields[0] = commandBaseName(fields[0])
	return strings.ToLower(strings.Join(fields, " "))
}

// commandPatternMatches reports whether one normalized command matches a
// normalized wildcard pattern. Matching is full-string: add a trailing '*' when
// extra arguments are allowed.
func commandPatternMatches(command, pattern string) bool {
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	return wildcardMatch(command, pattern)
}

// wildcardMatch implements '*' (any run, including empty) and '?' (one rune).
// Both inputs are expected to be lowercase; the function is deliberately not a
// regexp so a policy cannot introduce catastrophic backtracking.
func wildcardMatch(value, pattern string) bool {
	v := []rune(value)
	p := []rune(pattern)
	vi, pi := 0, 0
	star := -1
	mark := 0
	for vi < len(v) {
		if pi < len(p) && (p[pi] == '?' || p[pi] == v[vi]) {
			vi++
			pi++
			continue
		}
		if pi < len(p) && p[pi] == '*' {
			star = pi
			mark = vi
			pi++
			continue
		}
		if star >= 0 {
			pi = star + 1
			mark++
			vi = mark
			continue
		}
		return false
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

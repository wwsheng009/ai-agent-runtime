package agentconfig

import (
	"bytes"
	"path"
	"strings"
)

// Response marker stripping: providers may emit literal marker tokens inside
// streamed assistant content (for example minimax-family models emitting
// "]<]minimax[>[") that corrupt tool-call markup parsing and pollute history.
// The rules are configured through providers.response_marker_rules (and
// presets.yaml) and resolved per request against the effective model name.

// MatchResponseMarkerRule reports whether rule applies to the given model.
// An empty Models list matches every model; otherwise any pattern (path.Match
// style glob, case-insensitive) that matches the model is sufficient.
func MatchResponseMarkerRule(rule ResponseMarkerRule, model string) bool {
	model = strings.TrimSpace(model)
	if model == "" {
		return false
	}
	if len(rule.Models) == 0 {
		return true
	}
	lowerModel := strings.ToLower(model)
	for _, pattern := range rule.Models {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		matched, err := path.Match(strings.ToLower(pattern), lowerModel)
		if err == nil && matched {
			return true
		}
		if strings.EqualFold(pattern, model) {
			return true
		}
	}
	return false
}

// ResolveResponseMarkers returns the union of markers from every rule that
// matches the given model. The result is nil when no rule matches.
func ResolveResponseMarkers(rules []ResponseMarkerRule, model string) []string {
	var resolved []string
	for _, rule := range rules {
		if !MatchResponseMarkerRule(rule, model) {
			continue
		}
		for _, marker := range rule.Markers {
			marker = strings.TrimSpace(marker)
			if marker == "" {
				continue
			}
			resolved = append(resolved, marker)
		}
	}
	return resolved
}

// StripMarkers removes every occurrence of the given literal markers from
// input. The input is returned unchanged when markers is empty.
func StripMarkers(input string, markers []string) string {
	if input == "" || len(markers) == 0 {
		return input
	}
	return string(StripMarkersBytes([]byte(input), markers))
}

// StripMarkersBytes removes every occurrence of the given literal markers from
// input bytes. The input is returned unchanged when markers is empty. Markers
// are plain ASCII-safe literal byte sequences; stripping them never corrupts
// multi-byte UTF-8 sequences.
func StripMarkersBytes(input []byte, markers []string) []byte {
	if len(input) == 0 || len(markers) == 0 {
		return input
	}
	out := input
	for _, marker := range markers {
		if marker == "" {
			continue
		}
		out = bytes.ReplaceAll(out, []byte(marker), nil)
	}
	return out
}
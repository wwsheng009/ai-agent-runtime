package tools

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolcore/searchcore"
)

// The glob tool itself now runs the shared core implementation. These helpers
// remain because grep's include/exclude filters and bash's glob-shaped
// arguments use the same pattern language; they delegate to searchcore so there
// is still exactly one interpretation of a glob pattern in the process.

func hasGlobMeta(pattern string) bool {
	return strings.ContainsAny(pattern, "*?[")
}

// expandShellBraceGlobs expands common shell brace patterns emitted by models
// (e.g. *.{go,ts} -> [*.go *.ts]) for grep's include/exclude filters.
func expandShellBraceGlobs(pattern string) []string {
	return searchcore.ExpandGlobBraces(pattern)
}

// looksLikeUnsupportedBraceGlob detects shell brace expansions that were left
// unexpanded (too many alternatives or malformed), so grep can guide the caller
// to split the call instead of silently matching nothing.
func looksLikeUnsupportedBraceGlob(pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return false
	}
	if !searchcore.HasGlobBraceAlternatives(pattern) {
		return false
	}
	// Whether this particular pattern expanded successfully is the caller's
	// question (grep tracks the expanded specs next to the raw input). What the
	// name promises - and what those callers need - is the shape test: this
	// looks like a brace alternation that path.Match and rg would take
	// literally.
	return true
}

func matchGlobPattern(pattern, relPath string) (bool, error) {
	return searchcore.MatchGlobPath(pattern, relPath, false)
}

func normalizeGlobPattern(pattern string) string {
	return searchcore.NormalizeGlobPattern(pattern)
}

func splitGlobSegments(value string) []string {
	return searchcore.SplitGlobSegments(value)
}

func containsDoubleStar(parts []string) bool {
	return searchcore.ContainsDoubleStar(parts)
}

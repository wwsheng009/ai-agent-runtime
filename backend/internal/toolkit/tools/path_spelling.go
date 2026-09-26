package tools

import "github.com/wwsheng009/ai-agent-runtime/internal/pathrepair"

// This file supports recovering from "invisible" filename mismatches.
//
// Design rationale (docs/analysis/commandcode-read-tool-design-borrowing-20260926.md §3.7):
// when the model requests a name that differs from the on-disk name only in
// invisible Unicode code points (narrow no-break space, curly quotes), an
// existence check fails forever and the model retries the same dead path.
// findSpellingMatches scans the parent directory for folded-name candidates
// after such a failure. The caller MUST re-run the sandbox boundary check on
// every candidate: the repair must not become an escape hatch.
//
// The folding table lives in internal/pathrepair so the tool preflight layer
// ranks candidates with exactly the same notion of "same name".

// normalizeNameForSpelling folds invisible Unicode variants into a comparable
// ASCII form (thin wrapper for tests and in-package callers).
func normalizeNameForSpelling(s string) string {
	return pathrepair.NormalizeNameForSpelling(s)
}

// findSpellingMatches returns the directory entries whose folded name equals
// the folded base name, excluding the exact spelling itself.
func findSpellingMatches(dir string, base string) []string {
	return pathrepair.FindSpellingMatches(dir, base)
}

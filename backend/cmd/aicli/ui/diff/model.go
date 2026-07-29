// Package diff provides structured unified-diff models and terminal rendering.
package diff

import "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"

const (
	// DefaultHighlightMaxBytes keeps large tool diffs responsive while the
	// add/delete roles and line numbers remain available without Chroma.
	DefaultHighlightMaxBytes = 64 * 1024
	DefaultHighlightMaxLines = 2000

	// DiffTabWidth is the column width used when expanding tabs in diff
	// content, so gutter alignment and wrapping agree with the terminal.
	DiffTabWidth = 4

	// minDiffContentCols is the narrowest content column worth wrapping into.
	// Below this the row is clipped instead, avoiding a tall column of
	// single-character fragments on very narrow terminals.
	minDiffContentCols = 8
)

// LineKind classifies a single diff line.
type LineKind int

const (
	LineContext LineKind = iota
	LineAdd
	LineDelete
	LineHeader
	LineHunk
	LineMeta
)

// DiffLine is one display row of a file diff.
type DiffLine struct {
	Kind      LineKind
	OldLineNo int // 0 = none
	NewLineNo int
	Text      string // raw content without leading +/- for Add/Delete/Context
	Raw       string // original line including marker when parsed from text
}

// Hunk is a contiguous change region.
type Hunk struct {
	OldStart int
	OldCount int
	NewStart int
	NewCount int
	Header   string // raw @@ line without trailing newline
	Lines    []DiffLine
}

// FileDiff is one file's change set.
type FileDiff struct {
	OldPath  string
	NewPath  string
	Language string
	Hunks    []Hunk
}

// ParseOptions controls legacy unified-diff parsing.
type ParseOptions struct {
	// MaxLines caps total parsed content lines (safety).
	MaxLines int
}

// DefaultHighlightLimits returns the syntax budget for a single diff hunk.
// Diff semantics remain styled when source-token highlighting is skipped.
func DefaultHighlightLimits() syntax.Limits {
	return syntax.Limits{
		MaxBytes: DefaultHighlightMaxBytes,
		MaxLines: DefaultHighlightMaxLines,
	}
}

// DefaultParseOptions returns production limits.
func DefaultParseOptions() ParseOptions {
	return ParseOptions{MaxLines: 20000}
}

// Round-to-round review diffing: compare the plan body between two archived
// snapshots and render a bounded unified diff.
//
// The archive keeps every review round's body, so "what changed since the last
// round" is pure data — no editor state involved. The renderer is deliberately
// dependency-free and bounded: a plan edit between two rounds is usually small,
// and a pathological rewrite degrades to one delete/insert block instead of
// allocating a quadratic matrix.
package planmode

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/planstore"
)

const (
	// diffDefaultContext is the standard 3 lines of context around a change.
	diffDefaultContext = 3
	// diffDefaultMaxLines bounds the rendered diff (a plan is reviewed by a
	// human or a model; an unbounded wall of text is never useful).
	diffDefaultMaxLines = 400
	// diffMaxMatrixLines caps the LCS matrix size (600x600 cells); beyond it the
	// changed middle degrades to a coarse delete+insert block.
	diffMaxMatrixLines = 600
)

// DiffOptions tunes one diff render.
type DiffOptions struct {
	// ContextLines is the number of unchanged lines kept around a change.
	ContextLines int
	// MaxLines bounds the rendered diff body; the result reports truncation.
	MaxLines int
}

// DiffResult is one rendered comparison between two plan bodies.
type DiffResult struct {
	// Text is the unified diff (empty when Identical).
	Text string
	// Added/Removed count changed lines.
	Added   int
	Removed int
	// Identical reports that both sides are byte-equal.
	Identical bool
	// Truncated reports that Text hit MaxLines (the counts stay accurate for the
	// rendered part).
	Truncated bool
	// OldLines/NewLines are the compared line counts.
	OldLines int
	NewLines int
	// Coarse reports that the change was too large for line-level matching and
	// was rendered as one delete+insert block.
	Coarse bool
	// FromVersion/ToVersion are the compared archive versions (0 for a raw
	// UnifiedDiff call).
	FromVersion int
	ToVersion   int
	// RecordID is the archived record the comparison came from ("" for a raw
	// UnifiedDiff call).
	RecordID string
}

// DiffVersionsOptions describes comparing two archived snapshots of one record.
type DiffVersionsOptions struct {
	Store    *planstore.Store
	RecordID string
	// From/To select versions; 0 means "derive": To defaults to the latest
	// version and From to the previous one.
	From int
	To   int
	// Options tunes the render.
	Options DiffOptions
}

// UnifiedDiff renders a git-style unified diff between two texts.
func UnifiedDiff(oldText, newText string, opts DiffOptions) DiffResult {
	context := opts.ContextLines
	if context < 0 {
		context = 0
	}
	if context == 0 {
		context = diffDefaultContext
	}
	maxLines := opts.MaxLines
	if maxLines <= 0 {
		maxLines = diffDefaultMaxLines
	}

	oldLines, oldTrailing := splitDiffLines(oldText)
	newLines, newTrailing := splitDiffLines(newText)
	result := DiffResult{OldLines: len(oldLines), NewLines: len(newLines)}

	prefix := 0
	for prefix < len(oldLines) && prefix < len(newLines) && oldLines[prefix] == newLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldLines)-prefix && suffix < len(newLines)-prefix &&
		oldLines[len(oldLines)-1-suffix] == newLines[len(newLines)-1-suffix] {
		suffix++
	}
	if prefix == len(oldLines) && prefix == len(newLines) {
		result.Identical = true
		return result
	}

	middleOld := oldLines[prefix : len(oldLines)-suffix]
	middleNew := newLines[prefix : len(newLines)-suffix]
	var middle []diffOp
	if len(middleOld) <= diffMaxMatrixLines && len(middleNew) <= diffMaxMatrixLines {
		middle = lcsDiffOps(middleOld, middleNew)
	} else {
		result.Coarse = true
		middle = make([]diffOp, 0, len(middleOld)+len(middleNew))
		for _, line := range middleOld {
			middle = append(middle, diffOp{kind: '-', text: line})
		}
		for _, line := range middleNew {
			middle = append(middle, diffOp{kind: '+', text: line})
		}
	}

	ops := make([]diffOp, 0, prefix+suffix+len(middle))
	for _, line := range oldLines[:prefix] {
		ops = append(ops, diffOp{kind: ' ', text: line})
	}
	ops = append(ops, middle...)
	for _, line := range oldLines[len(oldLines)-suffix:] {
		ops = append(ops, diffOp{kind: ' ', text: line})
	}

	for _, op := range ops {
		switch op.kind {
		case '+':
			result.Added++
		case '-':
			result.Removed++
		}
	}

	text, truncated := renderUnifiedDiff(ops, context, maxLines, oldNoNewline(oldTrailing, newTrailing, ops))
	result.Text = text
	result.Truncated = truncated
	return result
}

// DiffArchivedVersions compares two stored snapshots and labels the result with
// each round's decision/source/timestamp.
func DiffArchivedVersions(opts DiffVersionsOptions) (DiffResult, error) {
	id := strings.TrimSpace(opts.RecordID)
	if id == "" {
		return DiffResult{}, fmt.Errorf("planmode: diff requires an archived plan id")
	}
	store := opts.Store
	if store == nil {
		store = DefaultPlanStore()
	}
	record, ok, err := store.Get(id)
	if err != nil {
		return DiffResult{}, err
	}
	if !ok {
		return DiffResult{}, fmt.Errorf("%w: %s", planstore.ErrNotFound, id)
	}
	if record.Version <= 0 {
		return DiffResult{}, fmt.Errorf("planmode: archived plan %s has no snapshot yet", record.ID)
	}

	to := opts.To
	if to <= 0 {
		to = record.Version
	}
	from := opts.From
	if from <= 0 {
		from = to - 1
		if from < 1 {
			from = to
		}
	}
	oldContent, err := store.ReadVersion(record.ID, from)
	if err != nil {
		return DiffResult{}, err
	}
	newContent, err := store.ReadVersion(record.ID, to)
	if err != nil {
		return DiffResult{}, err
	}

	result := UnifiedDiff(string(oldContent), string(newContent), opts.Options)
	result.FromVersion = from
	result.ToVersion = to
	result.RecordID = record.ID
	result.Text = diffVersionHeader(record, from, to) + result.Text
	return result, nil
}

// diffVersionHeader frames a diff with the two rounds' review metadata so the
// reader knows what decision produced each side.
func diffVersionHeader(record planstore.Record, from, to int) string {
	return fmt.Sprintf("--- v%d %s\n+++ v%d %s\n",
		from, describeRound(record, from),
		to, describeRound(record, to))
}

// describeRound renders "decision (source, time)" for one stored round.
func describeRound(record planstore.Record, version int) string {
	for _, round := range record.Rounds {
		if round.Version != version {
			continue
		}
		parts := make([]string, 0, 2)
		if strings.TrimSpace(round.Source) != "" {
			parts = append(parts, strings.TrimSpace(round.Source))
		}
		if strings.TrimSpace(round.CreatedAt) != "" {
			parts = append(parts, strings.TrimSpace(round.CreatedAt))
		}
		label := strings.TrimSpace(round.Decision)
		if label == "" {
			label = "snapshot"
		}
		if len(parts) > 0 {
			return fmt.Sprintf("%s (%s)", label, strings.Join(parts, ", "))
		}
		return label
	}
	return "snapshot"
}

// diffOp is one rendered line with its diff marker.
type diffOp struct {
	kind byte // ' ' context, '-' removed, '+' added
	text string
}

// splitDiffLines splits text into lines and reports whether it ended with a
// newline (needed for the "\ No newline at end of file" marker).
func splitDiffLines(text string) ([]string, bool) {
	if text == "" {
		return nil, true
	}
	trailing := strings.HasSuffix(text, "\n")
	if trailing {
		text = strings.TrimSuffix(text, "\n")
	}
	return strings.Split(text, "\n"), trailing
}

// oldNoNewline reports which side owns the last rendered line so the caller can
// emit the "no newline" marker on the right side (nil when both end with one).
func oldNoNewline(oldTrailing, newTrailing bool, ops []diffOp) []bool {
	if oldTrailing && newTrailing {
		return nil
	}
	last := -1
	for index, op := range ops {
		if op.kind != '+' {
			last = index
		}
	}
	lastNew := -1
	for index, op := range ops {
		if op.kind != '-' {
			lastNew = index
		}
	}
	marks := make([]bool, len(ops))
	if !oldTrailing && last >= 0 {
		marks[last] = true
	}
	if !newTrailing && lastNew >= 0 {
		marks[lastNew] = true
	}
	return marks
}

// lcsDiffOps produces a minimal-ish line diff via a longest-common-subsequence
// table. Callers keep the inputs inside diffMaxMatrixLines.
func lcsDiffOps(oldLines, newLines []string) []diffOp {
	n, m := len(oldLines), len(newLines)
	table := make([][]int, n+1)
	for i := range table {
		table[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				table[i][j] = table[i+1][j+1] + 1
				continue
			}
			if table[i+1][j] >= table[i][j+1] {
				table[i][j] = table[i+1][j]
			} else {
				table[i][j] = table[i][j+1]
			}
		}
	}
	ops := make([]diffOp, 0, n+m)
	for i, j := 0, 0; i < n || j < m; {
		switch {
		case i < n && j < m && oldLines[i] == newLines[j]:
			ops = append(ops, diffOp{kind: ' ', text: oldLines[i]})
			i++
			j++
		case j < m && (i == n || table[i][j+1] > table[i+1][j]):
			ops = append(ops, diffOp{kind: '+', text: newLines[j]})
			j++
		default:
			ops = append(ops, diffOp{kind: '-', text: oldLines[i]})
			i++
		}
	}
	return ops
}

// renderUnifiedDiff groups changes into hunks with context and bounds the
// output. The noNewline slice (when non-nil) indexes ops that need the
// "\ No newline at end of file" marker.
func renderUnifiedDiff(ops []diffOp, context, maxLines int, noNewline []bool) (string, bool) {
	groups := hunkGroups(ops, context)
	if len(groups) == 0 {
		return "", false
	}
	lines := make([]string, 0, min(len(ops), maxLines))
	truncated := false
	for _, group := range groups {
		if len(lines) >= maxLines {
			truncated = true
			break
		}
		oldStart, newStart, oldCount, newCount := hunkRange(ops, group[0], group[1])
		lines = append(lines, fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount))
		for index := group[0]; index < group[1]; index++ {
			if len(lines) >= maxLines {
				truncated = true
				break
			}
			lines = append(lines, string(ops[index].kind)+ops[index].text)
			if noNewline != nil && index < len(noNewline) && noNewline[index] {
				lines = append(lines, `\ No newline at end of file`)
			}
		}
	}
	if truncated {
		lines = append(lines, fmt.Sprintf("... diff 已截断（上限 %d 行）", maxLines))
	}
	return strings.Join(lines, "\n") + "\n", truncated
}

// hunkGroups returns [start,end) ranges of ops to render: every change plus
// `context` unchanged lines around it, merging groups separated by no more than
// 2*context unchanged lines.
func hunkGroups(ops []diffOp, context int) [][2]int {
	groups := make([][2]int, 0, 4)
	start := -1
	lastChange := -1
	for index, op := range ops {
		if op.kind == ' ' {
			continue
		}
		if start < 0 {
			start = max(0, index-context)
		} else if index-lastChange > 2*context {
			groups = append(groups, [2]int{start, min(lastChange+context+1, len(ops))})
			start = max(0, index-context)
		}
		lastChange = index
	}
	if start >= 0 {
		groups = append(groups, [2]int{start, min(lastChange+context+1, len(ops))})
	}
	return groups
}

// hunkRange computes the "@@ -a,b +c,d @@" numbers for a rendered range.
func hunkRange(ops []diffOp, start, end int) (oldStart, newStart, oldCount, newCount int) {
	oldLine, newLine := 1, 1
	for _, op := range ops[:start] {
		if op.kind != '+' {
			oldLine++
		}
		if op.kind != '-' {
			newLine++
		}
	}
	for _, op := range ops[start:end] {
		if op.kind != '+' {
			oldCount++
		}
		if op.kind != '-' {
			newCount++
		}
	}
	return oldLine, newLine, oldCount, newCount
}

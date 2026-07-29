package diff

import (
	"strconv"
	"strings"
)

const (
	// hunkSeparatorText is the trimmed form of the inter-hunk marker emitted
	// by chat tool rendering ("      ...") between non-adjacent regions of an
	// "• Edited" supplement. Matching on the trimmed value keeps leading
	// indent from the pretty-printer optional.
	hunkSeparatorText = "..."
	// contextMarkerColumn is the marker column of an unchanged row: the
	// producer writes "<n>   <text>" where changed rows use "<n> +/- <text>".
	contextMarkerColumn = "   "
)

// ParseUnified parses a unified diff text into one or more FileDiff values.
// It is the legacy compatibility path; structured tool events should build
// FileDiff directly.
func ParseUnified(text string, opts ParseOptions) []FileDiff {
	files, _ := ParseUnifiedWithLimit(text, opts)
	return files
}

// ParseUnifiedWithLimit parses like ParseUnified and additionally reports
// whether opts.MaxLines cut the input short, so callers can tell the reader
// that rows were dropped instead of ending the diff without explanation.
func ParseUnifiedWithLimit(text string, opts ParseOptions) (files []FileDiff, truncated bool) {
	if opts.MaxLines <= 0 {
		opts = DefaultParseOptions()
	}
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")

	var cur *FileDiff
	var hunk *Hunk
	oldNo, newNo := 0, 0
	contentLines := 0
	// pendingNewPath is set by a "--- " header so the following "+++ " line is
	// read as its pair instead of as hunk content.
	pendingNewPath := false

	flushHunk := func() {
		if cur != nil && hunk != nil && (len(hunk.Lines) > 0 || hunk.Header != "") {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	for i := 0; i < len(lines); i++ {
		if contentLines >= opts.MaxLines {
			truncated = hasRemainingContent(lines[i:])
			break
		}
		line := lines[i]
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			cur = &FileDiff{}
			// diff --git a/foo b/bar
			parts := strings.Fields(line)
			if len(parts) >= 4 {
				cur.OldPath = strings.TrimPrefix(parts[2], "a/")
				cur.NewPath = strings.TrimPrefix(parts[3], "b/")
			}
			pendingNewPath = false
		case isOldFileHeader(lines, i):
			// apply_patch and plain `diff -u` output often omit "diff --git",
			// so a "--- "/"+++ " pair after content is the only signal that the
			// next file started. Without this the second file's rows would be
			// appended to the first one.
			if cur != nil && (len(cur.Hunks) > 0 || hunk != nil) {
				flushFile()
			}
			if cur == nil {
				cur = &FileDiff{}
			}
			cur.OldPath = headerPath(line, "--- ", "a/")
			pendingNewPath = true
		case strings.HasPrefix(line, "+++ ") && (pendingNewPath || hunk == nil):
			pendingNewPath = false
			if cur == nil {
				cur = &FileDiff{}
			}
			cur.NewPath = headerPath(line, "+++ ", "b/")
		case strings.HasPrefix(line, "@@"):
			pendingNewPath = false
			if cur == nil {
				cur = &FileDiff{}
			}
			flushHunk()
			hunk = &Hunk{Header: line}
			oldNo, newNo = parseHunkHeader(line, hunk)
			contentLines++
		case hunk != nil && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ") || line == "\\ No newline at end of file"):
			contentLines++
			dl := DiffLine{Raw: line}
			switch {
			case line == "\\ No newline at end of file":
				dl.Kind = LineMeta
				dl.Text = line
			case strings.HasPrefix(line, "+"):
				dl.Kind = LineAdd
				dl.Text = line[1:]
				newNo++
				dl.NewLineNo = newNo
			case strings.HasPrefix(line, "-"):
				dl.Kind = LineDelete
				dl.Text = line[1:]
				oldNo++
				dl.OldLineNo = oldNo
			default: // context " ..."
				dl.Kind = LineContext
				if strings.HasPrefix(line, " ") {
					dl.Text = line[1:]
				} else {
					dl.Text = line
				}
				oldNo++
				newNo++
				dl.OldLineNo = oldNo
				dl.NewLineNo = newNo
			}
			hunk.Lines = append(hunk.Lines, dl)
		default:
			// File header misc (index, mode) — attach as meta if in file.
			if cur != nil && hunk == nil && strings.TrimSpace(line) != "" {
				// ignore index lines mostly
				if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "new file") || strings.HasPrefix(line, "deleted file") || strings.HasPrefix(line, "similarity ") || strings.HasPrefix(line, "rename ") {
					continue
				}
			}
		}
	}
	flushFile()
	return files, truncated
}

// hasRemainingContent reports whether the unparsed tail still holds anything
// but blank lines, so a budget that stops exactly at the end is not reported
// as truncation.
func hasRemainingContent(rest []string) bool {
	for _, line := range rest {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

// isOldFileHeader reports whether lines[i] opens a file section.
//
// The "+++ " line must follow immediately. Requiring the pair keeps hunk
// content that happens to start with "--- " or "+++ " (an added line whose own
// text begins with "++ ") from being mistaken for a file header.
func isOldFileHeader(lines []string, i int) bool {
	return strings.HasPrefix(lines[i], "--- ") &&
		i+1 < len(lines) &&
		strings.HasPrefix(lines[i+1], "+++ ")
}

// headerPath extracts the path from a "--- "/"+++ " header, dropping the
// prefix, the a//b/ root and any trailing timestamp column.
func headerPath(line, marker, root string) string {
	path := strings.TrimSpace(strings.TrimPrefix(line, marker))
	path = strings.TrimPrefix(path, root)
	if i := strings.IndexByte(path, '\t'); i >= 0 {
		path = strings.TrimSpace(path[:i])
	}
	return path
}

func parseHunkHeader(line string, h *Hunk) (oldStart, newStart int) {
	// @@ -l,s +l,s @@ optional
	rest := strings.TrimPrefix(line, "@@")
	rest = strings.TrimSpace(rest)
	if i := strings.Index(rest, "@@"); i >= 0 {
		rest = strings.TrimSpace(rest[:i])
	}
	parts := strings.Fields(rest)
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			a, b := parseRange(p[1:])
			h.OldStart = a
			h.OldCount = b
			oldStart = a - 1 // will pre-increment
		} else if strings.HasPrefix(p, "+") {
			a, b := parseRange(p[1:])
			h.NewStart = a
			h.NewCount = b
			newStart = a - 1
		}
	}
	return oldStart, newStart
}

func parseRange(s string) (start, count int) {
	count = 1
	if i := strings.IndexByte(s, ','); i >= 0 {
		start, _ = strconv.Atoi(s[:i])
		count, _ = strconv.Atoi(s[i+1:])
		return start, count
	}
	start, _ = strconv.Atoi(s)
	return start, count
}

// Supplement is one numbered diff block emitted by the chat transcript.
// Label distinguishes mutating edit output from read-only diff inspection.
type Supplement struct {
	Label string
	Diff  FileDiff
}

// ParseSupplementBlocks parses one or more "• Edited/• Diff path (+n -m)"
// numbered blocks. Each file and each visible elision marker gets its own
// hunk, so language inference and highlighting budgets stay file-local.
func ParseSupplementBlocks(text string) []Supplement {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	lines := strings.Split(normalized, "\n")
	var supplements []Supplement
	var current *Supplement
	var hunk Hunk

	flushHunk := func() {
		if current != nil && len(hunk.Lines) > 0 {
			current.Diff.Hunks = append(current.Diff.Hunks, hunk)
		}
		hunk = Hunk{}
	}
	flushFile := func() {
		flushHunk()
		if current != nil && len(current.Diff.Hunks) > 0 {
			supplements = append(supplements, *current)
		}
		current = nil
	}

	for _, line := range lines {
		trim := strings.TrimSpace(line)
		if label, rest, ok := parseSupplementHeader(trim); ok {
			flushFile()
			// path may be followed by (+n -m)
			path := rest
			if i := strings.Index(rest, " (+"); i >= 0 {
				path = strings.TrimSpace(rest[:i])
			}
			current = &Supplement{
				Label: label,
				Diff:  FileDiff{OldPath: path, NewPath: path},
			}
			continue
		}
		if current == nil {
			continue
		}
		// Hunk separator emitted between non-adjacent regions.
		if trim == hunkSeparatorText {
			flushHunk()
			hunk.Lines = append(hunk.Lines, DiffLine{Kind: LineMeta, Text: hunkSeparatorText, Raw: line})
			continue
		}
		// "259 -     old", "259 +     new" or "259   unchanged"
		no, kind, body, ok := parseNumberedDiffLine(line)
		if !ok {
			continue
		}
		dl := DiffLine{Text: body, Raw: line}
		switch kind {
		case '-':
			dl.Kind = LineDelete
			dl.OldLineNo = no
		case '+':
			dl.Kind = LineAdd
			dl.NewLineNo = no
		default:
			// Context rows carry the same number on both sides; the supplement
			// format only prints one column.
			dl.Kind = LineContext
			dl.OldLineNo = no
			dl.NewLineNo = no
		}
		hunk.Lines = append(hunk.Lines, dl)
	}
	flushFile()
	return supplements
}

// ParseEditedSupplement is the single-file compatibility adapter. Production
// rendering uses ParseSupplementBlocks so later files are never collapsed into
// the first file's model.
func ParseEditedSupplement(text string) (FileDiff, bool) {
	blocks := ParseSupplementBlocks(text)
	if len(blocks) == 0 || blocks[0].Label != "Edited" {
		return FileDiff{}, false
	}
	return blocks[0].Diff, true
}

func parseSupplementHeader(line string) (label, rest string, ok bool) {
	for _, candidate := range []string{"Edited", "Diff"} {
		prefix := "• " + candidate + " "
		if strings.HasPrefix(line, prefix) {
			return candidate, strings.TrimPrefix(line, prefix), true
		}
	}
	return "", "", false
}

// parseNumberedDiffLine reads one supplement row.
//
// The layout is fixed by the producer: "<n> <marker> <text>" for changed rows
// and "<n>   <text>" for context rows. Matching by position instead of
// skipping all spaces matters because content may itself start with '+' or
// '-'; a greedy scan would misread "12   -1 offset" as a deletion.
//
// marker is '+', '-' or ' ' for context.
func parseNumberedDiffLine(line string) (lineNo int, marker rune, body string, ok bool) {
	s := strings.TrimLeft(line, " \t")
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != ' ' {
		return 0, 0, "", false
	}
	lineNo, _ = strconv.Atoi(s[:i])

	// Context: number followed by the three-space marker column.
	if strings.HasPrefix(s[i:], contextMarkerColumn) {
		return lineNo, ' ', s[i+len(contextMarkerColumn):], true
	}
	// Change: single space, marker, then a space (or end of line).
	rest := s[i+1:]
	if rest == "" {
		return 0, 0, "", false
	}
	switch rest[0] {
	case '+', '-':
		marker = rune(rest[0])
	default:
		return 0, 0, "", false
	}
	rest = rest[1:]
	if rest == "" {
		return lineNo, marker, "", true
	}
	if rest[0] != ' ' {
		return 0, 0, "", false
	}
	return lineNo, marker, rest[1:], true
}

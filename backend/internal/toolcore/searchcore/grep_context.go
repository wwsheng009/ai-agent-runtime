package searchcore

import "os"

// MaxGrepContextLines caps before/after context per match. The local tool has
// always clamped at this value; living in core turns it from a surface
// accident into a shared contract.
const MaxGrepContextLines = 10

// ClampGrepContextLines bounds a caller-supplied context size to the shared
// range.
func ClampGrepContextLines(value int) int {
	if value < 0 {
		return 0
	}
	if value > MaxGrepContextLines {
		return MaxGrepContextLines
	}
	return value
}

// GrepContextLine is one rendered line of a context window: either a match or
// one of the lines surrounding it. GroupStart marks the first line of a
// contiguous block so a renderer can separate blocks the way ripgrep does.
type GrepContextLine struct {
	File       string
	Line       int
	Text       string
	IsMatch    bool
	GroupStart bool
}

type grepContextWindow struct {
	start int
	end   int
}

// ExpandGrepContext turns the windowed match list into the display lines of
// each match's context window. It is engine-independent by construction: context
// lines are produced by re-reading the matched files through the same
// bounded line reader the walker uses, so ripgrep never has to pass
// --context and the two engines cannot render different windows. A match that
// falls inside an earlier match's window is not duplicated.
//
// onlyMatching is the one place the two output units meet: with it a matched
// line shows its fragments and the surrounding lines still show whole lines -
// exactly what `rg -o -C` prints. Every fragment on the line is its own entry,
// and the entries stay adjacent so a renderer does not separate them.
func ExpandGrepContext(matches []GrepMatch, before, after int, onlyMatching bool) []GrepContextLine {
	before = ClampGrepContextLines(before)
	after = ClampGrepContextLines(after)
	out := make([]GrepContextLine, 0, len(matches))
	if before == 0 && after == 0 {
		for _, match := range matches {
			appendGrepContextLine(&out, match.File, match.Line, match.Text, true)
		}
		return out
	}

	reader := newLineReader()
	for i := 0; i < len(matches); {
		file := matches[i].File
		j := i
		for j < len(matches) && matches[j].File == file {
			j++
		}
		expandGrepContextFile(reader, matches[i:j], file, before, after, onlyMatching, &out)
		i = j
	}
	return out
}

func expandGrepContextFile(reader *lineReader, matches []GrepMatch, file string, before, after int, onlyMatching bool, out *[]GrepContextLine) {
	windows := make([]grepContextWindow, 0, len(matches))
	for _, match := range matches {
		start := match.Line - before
		if start < 1 {
			start = 1
		}
		end := match.Line + after
		if len(windows) > 0 && start <= windows[len(windows)-1].end+1 {
			if end > windows[len(windows)-1].end {
				windows[len(windows)-1].end = end
			}
			continue
		}
		windows = append(windows, grepContextWindow{start: start, end: end})
	}

	source, err := os.Open(file)
	if err != nil {
		// The file vanished between search and expansion (or cannot be
		// re-read): keep the match lines the engine already read instead of
		// dropping evidence.
		for _, match := range matches {
			appendGrepContextLine(out, match.File, match.Line, match.Text, true)
		}
		return
	}
	defer source.Close()
	reader.reset(source)

	matchIdx, windowIdx := 0, 0
	for {
		lineNo, text, truncated, ok := reader.next()
		if !ok {
			break
		}
		for windowIdx < len(windows) && lineNo > windows[windowIdx].end {
			windowIdx++
		}
		if windowIdx >= len(windows) {
			// Every requested window has been read; stop instead of scanning
			// the rest of a large file.
			break
		}
		if lineNo < windows[windowIdx].start {
			continue
		}
		display := reader.displayText(text, truncated)
		if !validUTF8(display) {
			continue
		}
		isMatch := false
		if matchIdx < len(matches) && lineNo == matches[matchIdx].Line {
			isMatch = true
		}
		if isMatch && onlyMatching {
			// The matched line carries fragments; every fragment on it is one
			// entry, and they share a line number on purpose.
			for matchIdx < len(matches) && matches[matchIdx].Line == lineNo {
				appendGrepContextLine(out, file, lineNo, matches[matchIdx].Text, true)
				matchIdx++
			}
			continue
		}
		if isMatch {
			matchIdx++
		}
		appendGrepContextLine(out, file, lineNo, string(display), isMatch)
	}
}

func appendGrepContextLine(out *[]GrepContextLine, file string, line int, text string, isMatch bool) {
	*out = append(*out, GrepContextLine{
		File:       file,
		Line:       line,
		Text:       text,
		IsMatch:    isMatch,
		GroupStart: startsGrepContextGroup(*out, file, line),
	})
}

// startsGrepContextGroup reports whether a line begins a new context block. Two
// entries on the same line are one block: with only_matching a line contributes
// several fragments, and a separator between them would invent a gap that is not
// there.
func startsGrepContextGroup(out []GrepContextLine, file string, line int) bool {
	if len(out) == 0 {
		return true
	}
	last := out[len(out)-1]
	if last.File != file {
		return true
	}
	return line != last.Line && line != last.Line+1
}

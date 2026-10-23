package mutatecore

import (
	"strings"
	"unicode"
)

// NormalizePatchEnvelope repairs the common model wrappers around a Codex
// patch before parsing: heredoc fences, a surrounding markdown fence, and
// several markers collapsed onto one line. It is shared by local apply_patch
// and the core parser so both accept the same input shapes.
func NormalizePatchEnvelope(input string) string {
	input = unwrapPatchHeredoc(input)
	lines := strings.Split(input, "\n")
	if len(lines) >= 3 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") &&
		strings.TrimSpace(lines[len(lines)-1]) == "```" {
		lines = lines[1 : len(lines)-1]
	}
	lines = expandCollapsedPatchLines(lines)
	normalized := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case isPatchBoundaryLine(trimmed, PatchBegin):
			normalized = append(normalized, PatchBegin)
		case isPatchBoundaryLine(trimmed, PatchEnd):
			normalized = append(normalized, PatchEnd)
		default:
			normalized = append(normalized, line)
		}
	}
	return strings.TrimSpace(strings.Join(normalized, "\n"))
}

func unwrapPatchHeredoc(input string) string {
	lines := strings.Split(input, "\n")
	if len(lines) < 4 {
		return input
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	switch first {
	case "<<EOF", "<<'EOF'", `<<"EOF"`:
		if strings.HasSuffix(last, "EOF") {
			return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
		}
	}
	return input
}

func expandCollapsedPatchLines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	markers := []string{PatchBegin, PatchEnd, PatchAdd, PatchUpdate, PatchDelete, PatchMoveTo, PatchEOF}
	out := make([]string, 0, len(lines)+4)
	for _, line := range lines {
		out = append(out, splitCollapsedPatchLine(line, markers)...)
	}
	return out
}

func splitCollapsedPatchLine(line string, markers []string) []string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return []string{line}
	}
	if !strings.Contains(trimmed, "***") && !strings.HasPrefix(trimmed, "@@") {
		return []string{line}
	}
	type cut struct {
		at     int
		marker string
	}
	cuts := make([]cut, 0, 4)
	for _, marker := range markers {
		searchFrom := 0
		for {
			idx := strings.Index(trimmed[searchFrom:], marker)
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			if abs > 0 {
				cuts = append(cuts, cut{at: abs, marker: marker})
			}
			searchFrom = abs + len(marker)
		}
	}
	if !strings.HasPrefix(trimmed, "@@") {
		searchFrom := 0
		for {
			idx := strings.Index(trimmed[searchFrom:], "@@")
			if idx < 0 {
				break
			}
			abs := searchFrom + idx
			if abs > 0 {
				cuts = append(cuts, cut{at: abs, marker: "@@"})
			}
			searchFrom = abs + 2
		}
	}
	if len(cuts) == 0 {
		return []string{line}
	}
	for i := 1; i < len(cuts); i++ {
		for j := i; j > 0 && cuts[j].at < cuts[j-1].at; j-- {
			cuts[j], cuts[j-1] = cuts[j-1], cuts[j]
		}
	}
	parts := make([]string, 0, len(cuts)+1)
	prev := 0
	for _, c := range cuts {
		if c.at <= prev {
			continue
		}
		segment := strings.TrimSpace(trimmed[prev:c.at])
		if segment != "" && (!isOnlyStarsAndSpaces(segment) || len(parts) == 0) {
			parts = append(parts, segment)
		}
		prev = c.at
	}
	if tail := strings.TrimSpace(trimmed[prev:]); tail != "" {
		parts = append(parts, tail)
	}
	if len(parts) == 0 {
		return []string{line}
	}
	return parts
}

func isOnlyStarsAndSpaces(s string) bool {
	for _, r := range s {
		if r != '*' && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func isPatchBoundaryLine(line, marker string) bool {
	line = strings.TrimSpace(line)
	if line == marker {
		return true
	}
	if !strings.HasPrefix(line, marker) {
		return false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(line, marker))
	return rest == "" || isOnlyStarsAndSpaces(rest)
}

package mutatecore

import (
	"strings"
	"unicode"
)

// LineMatcher compares one model-supplied line with one file line.
type LineMatcher func(actual, expected string) bool

// LineMatchers returns the shared recovery ladder used by edit and apply_patch:
// exact, trailing-space tolerant, full trim, then punctuation/whitespace
// collapsed. Keeping the ladder in one place prevents the two mutation tools
// from healing different classes of drift.
func LineMatchers() []LineMatcher {
	return []LineMatcher{
		func(actual, expected string) bool {
			return actual == expected
		},
		func(actual, expected string) bool {
			return strings.TrimRightFunc(actual, unicode.IsSpace) == strings.TrimRightFunc(expected, unicode.IsSpace)
		},
		func(actual, expected string) bool {
			return strings.TrimSpace(actual) == strings.TrimSpace(expected)
		},
		func(actual, expected string) bool {
			return NormalizeComparableLine(actual) == NormalizeComparableLine(expected)
		},
	}
}

// SliceMatches reports whether two equal-length line slices match under matcher.
func SliceMatches(left, right []string, matcher LineMatcher) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if !matcher(left[index], right[index]) {
			return false
		}
	}
	return true
}

// NormalizeComparableLine trims, folds common typographic punctuation, and
// collapses internal whitespace. It is only used for comparison; callers must
// still write the file's exact bytes.
func NormalizeComparableLine(line string) string {
	line = strings.TrimSpace(line)
	var builder strings.Builder
	builder.Grow(len(line))
	for _, char := range line {
		switch char {
		case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
			builder.WriteRune('-')
		case '\u2018', '\u2019', '\u201a', '\u201b':
			builder.WriteRune('\'')
		case '\u201c', '\u201d', '\u201e', '\u201f':
			builder.WriteRune('"')
		case '\u00a0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006',
			'\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
			builder.WriteRune(' ')
		default:
			builder.WriteRune(char)
		}
	}
	return strings.Join(strings.Fields(builder.String()), " ")
}

// LeadingWhitespace returns the leading spaces/tabs of one line.
func LeadingWhitespace(line string) string {
	i := 0
	for i < len(line) {
		if line[i] != ' ' && line[i] != '\t' {
			break
		}
		i++
	}
	return line[:i]
}

// NormalizeLineEndings converts CRLF and lone CR to LF.
func NormalizeLineEndings(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return text
}

// NonBlankLines returns lines whose trimmed form is non-empty.
func NonBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

// IsGenericStructuralLine reports brace/paren-only lines that match everywhere
// and therefore must not anchor recovery scoring.
func IsGenericStructuralLine(line string) bool {
	line = strings.TrimSpace(line)
	switch line {
	case "}", ")", "(", "{", "};", "},", "];", "],", "*/", "/*":
		return true
	}
	trimmed := strings.Trim(line, " \t{};,()")
	return trimmed == "" && line != ""
}

// RuneEditSimilarity is a cheap normalized inverse Levenshtein on runes.
// Used only for closest-snippet ranking, never for auto-heal writes.
func RuneEditSimilarity(a, b string) float64 {
	if a == b {
		return 1
	}
	ar := []rune(a)
	br := []rune(b)
	if len(ar) == 0 || len(br) == 0 {
		return 0
	}
	if len(ar) > len(br)*2 || len(br) > len(ar)*2 {
		return 0
	}
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := 0; j <= len(br); j++ {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := cur[j-1] + 1
			sub := prev[j-1] + cost
			cur[j] = del
			if ins < cur[j] {
				cur[j] = ins
			}
			if sub < cur[j] {
				cur[j] = sub
			}
		}
		prev, cur = cur, prev
	}
	dist := prev[len(br)]
	maxLen := len(ar)
	if len(br) > maxLen {
		maxLen = len(br)
	}
	if maxLen == 0 {
		return 1
	}
	sim := 1 - float64(dist)/float64(maxLen)
	if sim < 0 {
		return 0
	}
	return sim
}

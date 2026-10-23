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

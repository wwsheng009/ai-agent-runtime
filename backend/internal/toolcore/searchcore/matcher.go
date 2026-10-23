package searchcore

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"
)

// GlobMatcher implements the subset of glob syntax the model expects:
//
//   - matches any run of characters except '/'
//     **     matches any run including '/'
//     ?      matches one character except '/'
//     [a-z]  character class
//     {a,b}  alternation
//
// It is a thin wrapper over the same segment matcher used by the local glob
// surface. Keeping one implementation prevents the two surfaces from drifting.
type GlobMatcher struct {
	patterns []string
}

func NewGlobMatcher(pattern string) (*GlobMatcher, error) {
	raw := strings.TrimSpace(pattern)
	if raw == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	expanded, err := expandBraceAlternatives(raw)
	if err != nil {
		return nil, err
	}
	patterns := make([]string, 0, len(expanded))
	for _, candidate := range expanded {
		normalized := NormalizeGlobPattern(candidate)
		if len(SplitGlobSegments(normalized)) == 0 {
			return nil, fmt.Errorf("empty pattern")
		}
		patterns = append(patterns, normalized)
	}
	return &GlobMatcher{patterns: patterns}, nil
}

func (m *GlobMatcher) Match(candidate string) bool {
	if m == nil || len(m.patterns) == 0 {
		return false
	}
	for _, pattern := range m.patterns {
		if matched, err := MatchGlobPath(pattern, candidate, false); err == nil && matched {
			return true
		}
	}
	return false
}

// expandBraceAlternatives expands every top-level {a,b} group, recursively.
// It deliberately lives here (not in toolkit) so remote search and local glob
// share one brace interpretation.
func expandBraceAlternatives(pattern string) ([]string, error) {
	return expandBraceAlternativesDepth(pattern, 0)
}

func expandBraceAlternativesDepth(pattern string, depth int) ([]string, error) {
	if depth > 16 {
		return nil, fmt.Errorf("brace nesting too deep")
	}
	open, close, hasComma, err := firstExpandableBrace(pattern)
	if err != nil {
		return nil, err
	}
	if open < 0 || !hasComma {
		return []string{pattern}, nil
	}
	parts := splitTopLevelBraceParts(pattern[open+1 : close])
	if len(parts) <= 1 {
		return []string{pattern}, nil
	}
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		expanded, expandErr := expandBraceAlternativesDepth(pattern[:open]+part+pattern[close+1:], depth+1)
		if expandErr != nil {
			return nil, expandErr
		}
		out = append(out, expanded...)
		if len(out) > 64 {
			return nil, fmt.Errorf("brace expansion exceeds 64 alternatives")
		}
	}
	return out, nil
}

func firstExpandableBrace(pattern string) (open, close int, hasComma bool, err error) {
	for i := 0; i < len(pattern); i++ {
		if pattern[i] != '{' {
			continue
		}
		depth := 1
		for j := i + 1; j < len(pattern); j++ {
			switch pattern[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					if hasComma {
						return i, j, true, nil
					}
					i = j
				}
			case ',':
				if depth == 1 {
					hasComma = true
				}
			}
			if depth == 0 {
				break
			}
		}
		if depth != 0 {
			return 0, 0, false, fmt.Errorf("unterminated alternation")
		}
		hasComma = false
	}
	return -1, -1, false, nil
}

func splitTopLevelBraceParts(inner string) []string {
	parts := make([]string, 0, 2)
	depth := 0
	start := 0
	for i := 0; i < len(inner); i++ {
		switch inner[i] {
		case '{':
			depth++
		case '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, inner[start:])
	return parts
}

func baseName(path string) string {
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

// NormalizeGlobPattern canonicalizes a model-supplied pattern for matching.
// Local and remote surfaces share this so backslash, "./" and leading "/" are
// handled identically.
func NormalizeGlobPattern(pattern string) string {
	pattern = strings.TrimSpace(pattern)
	pattern = strings.ReplaceAll(pattern, `\`, `/`)
	for strings.HasPrefix(pattern, "./") {
		pattern = strings.TrimPrefix(pattern, "./")
	}
	for strings.HasPrefix(pattern, "/") {
		pattern = strings.TrimPrefix(pattern, "/")
	}
	return pattern
}

// SplitGlobSegments splits a normalized pattern or relative path into its
// non-empty segments.
func SplitGlobSegments(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		out = append(out, part)
	}
	return out
}

// MatchGlobPart matches one path segment. It intentionally uses path.Match
// rather than the regexp-based GlobMatcher: the local glob surface has long
// relied on path.Match's `**`-free segment semantics and case folding.
func MatchGlobPart(patternPart, pathPart string, caseInsensitive bool) (bool, error) {
	if caseInsensitive {
		patternPart = strings.ToLower(patternPart)
		pathPart = strings.ToLower(pathPart)
	}
	return path.Match(patternPart, pathPart)
}

// MatchGlobSegments matches a split pattern against a split relative path.
// A `**` segment matches zero or more path segments; other segments are
// matched with MatchGlobPart.
func MatchGlobSegments(patternParts, pathParts []string, caseInsensitive bool) (bool, error) {
	if !ContainsDoubleStar(patternParts) {
		if len(patternParts) != len(pathParts) {
			return false, nil
		}
		for i := range patternParts {
			matched, err := MatchGlobPart(patternParts[i], pathParts[i], caseInsensitive)
			if err != nil || !matched {
				return false, err
			}
		}
		return true, nil
	}

	type matchState struct {
		patternIndex int
		pathIndex    int
	}
	memo := make(map[matchState]bool)
	var match func(int, int) (bool, error)
	match = func(patternIndex, pathIndex int) (bool, error) {
		state := matchState{patternIndex: patternIndex, pathIndex: pathIndex}
		if cached, ok := memo[state]; ok {
			return cached, nil
		}
		var result bool
		defer func() { memo[state] = result }()

		if patternIndex >= len(patternParts) {
			result = pathIndex >= len(pathParts)
			return result, nil
		}
		current := patternParts[patternIndex]
		if current == "**" {
			if patternIndex == len(patternParts)-1 {
				result = true
				return true, nil
			}
			for skip := pathIndex; skip <= len(pathParts); skip++ {
				matched, err := match(patternIndex+1, skip)
				if err != nil {
					return false, err
				}
				if matched {
					result = true
					return true, nil
				}
			}
			result = false
			return false, nil
		}
		if pathIndex >= len(pathParts) {
			result = false
			return false, nil
		}
		matched, err := MatchGlobPart(current, pathParts[pathIndex], caseInsensitive)
		if err != nil || !matched {
			result = false
			return false, err
		}
		return match(patternIndex+1, pathIndex+1)
	}
	return match(0, 0)
}

// MatchGlobPath applies the shared full-path rule: a pattern without a slash
// also matches a file's basename, which is what both local and remote glob
// surfaces have always exposed to models.
func MatchGlobPath(pattern, candidate string, caseInsensitive bool) (bool, error) {
	pattern = NormalizeGlobPattern(pattern)
	candidate = NormalizeGlobPattern(candidate)
	patternParts := SplitGlobSegments(pattern)
	pathParts := SplitGlobSegments(candidate)
	if matched, err := MatchGlobSegments(patternParts, pathParts, caseInsensitive); err != nil || matched {
		return matched, err
	}
	if !strings.Contains(pattern, "/") {
		return MatchGlobSegments(patternParts, []string{baseName(candidate)}, caseInsensitive)
	}
	return false, nil
}

// MatchLeadingDoubleStarTail matches a pattern whose leading `**` was already
// consumed, against the tail of a path.
func MatchLeadingDoubleStarTail(tailParts, pathParts []string, caseInsensitive bool) (bool, error) {
	if len(tailParts) == 0 {
		return true, nil
	}
	if len(pathParts) < len(tailParts) {
		return false, nil
	}
	start := len(pathParts) - len(tailParts)
	for i := range tailParts {
		matched, err := MatchGlobPart(tailParts[i], pathParts[start+i], caseInsensitive)
		if err != nil || !matched {
			return false, err
		}
	}
	return true, nil
}

// ContainsDoubleStar reports whether a split pattern contains a `**` segment.
func ContainsDoubleStar(parts []string) bool {
	for _, part := range parts {
		if part == "**" {
			return true
		}
	}
	return false
}

// CompileRegexp compiles a search pattern with the shared case-fold flag.
func CompileRegexp(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

const (
	lineReaderBufferBytes = 64 * 1024
	truncatedMarker       = "…"
)

type lineReader struct {
	reader  *bufio.Reader
	line    int
	prefix  []byte
	display []byte
}

// newLineReader returns a reader that can be reset for every file. Grep uses
// one instance per call so the read buffer is not allocated once per file.
func newLineReader() *lineReader {
	return &lineReader{reader: bufio.NewReaderSize(nil, lineReaderBufferBytes)}
}

func (r *lineReader) reset(source io.Reader) {
	if r == nil {
		return
	}
	r.reader.Reset(source)
	r.line = 0
	r.prefix = r.prefix[:0]
	r.display = r.display[:0]
}

func trimLineCR(line []byte) []byte {
	if len(line) > 0 && line[len(line)-1] == '\r' {
		return line[:len(line)-1]
	}
	return line
}

func (r *lineReader) finish(line []byte, truncated bool) ([]byte, bool) {
	if !truncated {
		line = trimLineCR(line)
	}
	if len(line) > MaxGrepLineBytes {
		line = line[:MaxGrepLineBytes]
		truncated = true
	}
	if truncated {
		line = trimIncompleteUTF8Suffix(line)
	}
	return line, truncated
}

// next returns the next line, capped at MaxGrepLineBytes. The returned slice is
// valid until the next call. A truncated line reports truncated=true and omits
// the discarded suffix; displayText adds the marker used in results.
func (r *lineReader) next() (lineNo int, text []byte, truncated bool, ok bool) {
	if r == nil || r.reader == nil {
		return 0, nil, false, false
	}
	r.prefix = r.prefix[:0]
	over := false
	for {
		fragment, err := r.reader.ReadSlice('\n')
		if err == nil {
			fragment = fragment[:len(fragment)-1]
			// A complete line's trailing CR is delimiter syntax, not content.
			// Strip it before applying the byte cap so an exact-limit CRLF
			// line is not misreported as truncated.
			fragment = trimLineCR(fragment)
		}
		if len(fragment) > 0 && !over {
			remaining := MaxGrepLineBytes - len(r.prefix)
			if len(fragment) <= remaining {
				r.prefix = append(r.prefix, fragment...)
			} else {
				if remaining > 0 {
					r.prefix = append(r.prefix, fragment[:remaining]...)
				}
				over = true
			}
		}
		switch {
		case err == nil:
			text, truncated = r.finish(r.prefix, over)
			r.line++
			return r.line, text, truncated, true
		case err == bufio.ErrBufferFull:
			continue
		case err == io.EOF:
			if len(r.prefix) == 0 && !over {
				return 0, nil, false, false
			}
			text, truncated = r.finish(r.prefix, over)
			r.line++
			return r.line, text, truncated, true
		default:
			return 0, nil, false, false
		}
	}
}

// trimIncompleteUTF8Suffix drops a byte-capped rune prefix only when the cap
// landed inside a multi-byte rune. Genuinely invalid bytes are left untouched
// so the caller still classifies the file as binary.
func trimIncompleteUTF8Suffix(line []byte) []byte {
	for i := len(line) - 1; i >= 0 && len(line)-i <= utf8.UTFMax; i-- {
		lead := line[i]
		if lead < utf8.RuneSelf {
			return line
		}
		if lead&0xC0 == 0x80 {
			continue
		}
		want := 0
		switch {
		case lead&0xE0 == 0xC0:
			want = 2
		case lead&0xF0 == 0xE0:
			want = 3
		case lead&0xF8 == 0xF0:
			want = 4
		default:
			return line
		}
		if want > len(line)-i {
			return line[:i]
		}
		return line
	}
	return line
}

func (r *lineReader) displayText(text []byte, truncated bool) []byte {
	if !truncated {
		return text
	}
	r.display = append(r.display[:0], text...)
	r.display = append(r.display, truncatedMarker...)
	return r.display
}

func validUTF8(text []byte) bool {
	return utf8.Valid(text)
}

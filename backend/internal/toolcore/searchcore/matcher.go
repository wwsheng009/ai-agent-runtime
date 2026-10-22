package searchcore

import (
	"bufio"
	"fmt"
	"io"
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
type GlobMatcher struct {
	regex *regexp.Regexp
	raw   string
}

func NewGlobMatcher(pattern string) (*GlobMatcher, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return nil, fmt.Errorf("empty pattern")
	}
	expr, err := globToRegexp(pattern)
	if err != nil {
		return nil, err
	}
	compiled, err := regexp.Compile("^" + expr + "$")
	if err != nil {
		return nil, err
	}
	return &GlobMatcher{regex: compiled, raw: pattern}, nil
}

func (m *GlobMatcher) Match(path string) bool {
	if m == nil || m.regex == nil {
		return false
	}
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	if m.regex.MatchString(path) {
		return true
	}
	if !strings.Contains(m.raw, "/") {
		return m.regex.MatchString(baseName(path))
	}
	return false
}

func baseName(path string) string {
	if idx := strings.LastIndexByte(path, '/'); idx >= 0 {
		return path[idx+1:]
	}
	return path
}

func globToRegexp(pattern string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				j := i + 2
				if j < len(pattern) && pattern[j] == '/' {
					builder.WriteString("(?:.*/)?")
					i = j
					continue
				}
				builder.WriteString(".*")
				i++
				continue
			}
			builder.WriteString("[^/]*")
		case '?':
			builder.WriteString("[^/]")
		case '[':
			end := strings.IndexByte(pattern[i:], ']')
			if end < 0 {
				return "", fmt.Errorf("unterminated character class")
			}
			builder.WriteString(pattern[i : i+end+1])
			i += end
		case '{':
			end := strings.IndexByte(pattern[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated alternation")
			}
			body := pattern[i+1 : i+end]
			parts := strings.Split(body, ",")
			quoted := make([]string, 0, len(parts))
			for _, part := range parts {
				quoted = append(quoted, regexp.QuoteMeta(part))
			}
			builder.WriteString("(?:" + strings.Join(quoted, "|") + ")")
			i += end
		default:
			builder.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	return builder.String(), nil
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

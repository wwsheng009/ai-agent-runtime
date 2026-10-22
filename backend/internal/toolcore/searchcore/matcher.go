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

const lineReaderMaxBytes = 1024 * 1024

type lineReader struct {
	scanner *bufio.Scanner
	line    int
}

func newLineReader(reader io.Reader) *lineReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), lineReaderMaxBytes)
	return &lineReader{scanner: scanner}
}

func (r *lineReader) next() (lineNo int, text string, ok bool) {
	if r == nil || r.scanner == nil {
		return 0, "", false
	}
	if !r.scanner.Scan() {
		return 0, "", false
	}
	r.line++
	return r.line, r.scanner.Text(), true
}

func validUTF8(text string) bool {
	return utf8.ValidString(text)
}

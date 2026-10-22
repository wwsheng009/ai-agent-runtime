package remotetools

import (
	"bufio"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// globMatcher implements the subset of glob syntax the model expects:
//
//	*      matches any run of characters except '/'
//	**     matches any run including '/'
//	?      matches one character except '/'
//	[a-z]  character class
//	{a,b}  alternation
//
// filepath.Match does not support ** or {a,b}, and a remote search that silently
// ignores ** would return the wrong file set, so the matcher is implemented here
// and shared by glob and grep --glob.
type globMatcher struct {
	regex *regexp.Regexp
	raw   string
}

// newGlobMatcher compiles a glob into an anchored regular expression.
func newGlobMatcher(pattern string) (*globMatcher, error) {
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
	return &globMatcher{regex: compiled, raw: pattern}, nil
}

// match reports whether a slash-separated relative path matches.
func (m *globMatcher) match(path string) bool {
	if m == nil || m.regex == nil {
		return false
	}
	path = strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
	if m.regex.MatchString(path) {
		return true
	}
	// A bare pattern such as *.go should also match a nested file's base name:
	// models overwhelmingly mean "any file named like this".
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

// globToRegexp translates a glob into a regular expression fragment.
func globToRegexp(pattern string) (string, error) {
	var builder strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				// Collapse **/ and /** so they match zero or more directories.
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

// compileRemoteRegexp compiles a search pattern with an optional case fold.
//
// It is a thin wrapper so the grep tool and future tools agree on one flag.
func compileRemoteRegexp(pattern string, caseInsensitive bool) (*regexp.Regexp, error) {
	if caseInsensitive {
		pattern = "(?i)" + pattern
	}
	return regexp.Compile(pattern)
}

// utf8ValidString reports whether the line is valid UTF-8 text.
func utf8ValidString(text string) bool {
	return utf8.ValidString(text)
}

// lineReader yields line numbers and text without loading the file.
type lineReader struct {
	scanner *bufio.Scanner
	line    int
}

const remoteLineReaderMaxBytes = 1024 * 1024

func newLineReader(reader io.Reader) *lineReader {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), remoteLineReaderMaxBytes)
	return &lineReader{scanner: scanner}
}

// next returns the next line; ok is false at EOF or on a read error.
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
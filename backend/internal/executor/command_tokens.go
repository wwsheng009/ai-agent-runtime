package executor

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// SplitCommandTokens tokenizes a shell command while preserving quoted spans and
// common shell separators as standalone tokens.
func SplitCommandTokens(command string) []string {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	tokens := make([]string, 0, 8)
	var current strings.Builder
	inQuote := rune(0)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" {
			return
		}
		tokens = append(tokens, token)
	}

	for i := 0; i < len(command); {
		r, size := utf8.DecodeRuneInString(command[i:])
		if inQuote != 0 {
			if r == inQuote {
				inQuote = 0
				i += size
				continue
			}
			if r == '\\' && inQuote == '"' && i+size < len(command) {
				next, nextSize := utf8.DecodeRuneInString(command[i+size:])
				switch next {
				case '\\', '"', '$', '`':
					current.WriteRune(next)
					i += size + nextSize
					continue
				}
			}
			current.WriteRune(r)
			i += size
			continue
		}

		switch {
		case unicode.IsSpace(r):
			flush()
			i += size
		case r == '\'' || r == '"':
			inQuote = r
			i += size
		case r == '|':
			flush()
			if i+size < len(command) {
				next, nextSize := utf8.DecodeRuneInString(command[i+size:])
				if next == '|' {
					tokens = append(tokens, "||")
					i += size + nextSize
					continue
				}
			}
			tokens = append(tokens, "|")
			i += size
		case r == '&':
			flush()
			if i+size < len(command) {
				next, nextSize := utf8.DecodeRuneInString(command[i+size:])
				if next == '&' {
					tokens = append(tokens, "&&")
					i += size + nextSize
					continue
				}
			}
			tokens = append(tokens, "&")
			i += size
		case r == ';':
			flush()
			tokens = append(tokens, ";")
			i += size
		case r == '>' || r == '<':
			flush()
			tokens = append(tokens, string(r))
			i += size
		default:
			current.WriteRune(r)
			i += size
		}
	}

	flush()
	return tokens
}

// HasPipedHeadToken reports whether the token stream contains a pipe followed by head.
func HasPipedHeadToken(tokens []string) bool {
	for i := 0; i+1 < len(tokens); i++ {
		if tokens[i] == "|" && strings.EqualFold(tokens[i+1], "head") {
			return true
		}
	}
	return false
}

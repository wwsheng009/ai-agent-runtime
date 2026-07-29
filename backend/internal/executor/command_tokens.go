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

// IsGitDiffCommand reports whether a shell command contains a git diff
// invocation. It understands common git global options without treating an
// option value named "diff" as the subcommand.
func IsGitDiffCommand(command string) bool {
	tokens := SplitCommandTokens(command)
	for i := 0; i < len(tokens); i++ {
		if !isGitExecutableToken(tokens[i]) {
			continue
		}
		for j := i + 1; j < len(tokens); j++ {
			token := tokens[j]
			if isShellCommandSeparator(token) {
				break
			}
			if gitGlobalOptionConsumesValue(token) {
				j++
				continue
			}
			if strings.HasPrefix(token, "-") {
				continue
			}
			if strings.EqualFold(token, "diff") {
				return true
			}
			break
		}
	}
	return false
}

// LooksLikeUnifiedDiffOutput performs a cheap gate before a complete shell
// capture is promoted into the UI's full unified-diff parser.
func LooksLikeUnifiedDiffOutput(output string) bool {
	lines := strings.Split(strings.ReplaceAll(output, "\r\n", "\n"), "\n")
	for i := 0; i+2 < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "--- ") &&
			strings.HasPrefix(strings.TrimSpace(lines[i+1]), "+++ ") &&
			strings.HasPrefix(strings.TrimSpace(lines[i+2]), "@@") {
			return true
		}
	}
	return false
}

func isGitExecutableToken(token string) bool {
	token = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(token, `\`, "/")))
	if i := strings.LastIndexByte(token, '/'); i >= 0 {
		token = token[i+1:]
	}
	return token == "git" || token == "git.exe"
}

func gitGlobalOptionConsumesValue(token string) bool {
	switch strings.ToLower(token) {
	case "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path", "--super-prefix", "--config-env":
		return true
	default:
		return false
	}
}

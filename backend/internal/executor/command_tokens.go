package executor

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// CommandToken is one shell command token. FullyQuoted reports whether the whole
// token originated from quoted spans ('...' / "..."); a partially quoted token
// such as "dir/"*.go keeps FullyQuoted=false because its unquoted remainder is
// still subject to shell expansion.
type CommandToken struct {
	Text        string
	FullyQuoted bool
}

// SplitCommandTokens tokenizes a shell command while preserving quoted spans and
// common shell separators as standalone tokens.
func SplitCommandTokens(command string) []string {
	tokens := SplitCommandTokensDetailed(command)
	if len(tokens) == 0 {
		return nil
	}
	texts := make([]string, 0, len(tokens))
	for _, token := range tokens {
		texts = append(texts, token.Text)
	}
	return texts
}

// SplitCommandTokensDetailed is SplitCommandTokens plus per-token quote
// provenance. Token text is identical to SplitCommandTokens; FullyQuoted is true
// only when no unquoted character contributed to the token.
func SplitCommandTokensDetailed(command string) []CommandToken {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	tokens := make([]CommandToken, 0, 8)
	var current strings.Builder
	inQuote := rune(0)
	quoted := false
	unquoted := false

	flush := func() {
		if current.Len() == 0 {
			quoted = false
			unquoted = false
			return
		}
		token := strings.TrimSpace(current.String())
		current.Reset()
		if token == "" {
			quoted = false
			unquoted = false
			return
		}
		tokens = append(tokens, CommandToken{Text: token, FullyQuoted: quoted && !unquoted})
		quoted = false
		unquoted = false
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
					quoted = true
					i += size + nextSize
					continue
				}
			}
			current.WriteRune(r)
			quoted = true
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
					tokens = append(tokens, CommandToken{Text: "||"})
					i += size + nextSize
					continue
				}
			}
			tokens = append(tokens, CommandToken{Text: "|"})
			i += size
		case r == '&':
			flush()
			if i+size < len(command) {
				next, nextSize := utf8.DecodeRuneInString(command[i+size:])
				if next == '&' {
					tokens = append(tokens, CommandToken{Text: "&&"})
					i += size + nextSize
					continue
				}
			}
			tokens = append(tokens, CommandToken{Text: "&"})
			i += size
		case r == ';':
			flush()
			tokens = append(tokens, CommandToken{Text: ";"})
			i += size
		case r == '>' || r == '<':
			flush()
			tokens = append(tokens, CommandToken{Text: string(r)})
			i += size
		default:
			current.WriteRune(r)
			unquoted = true
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

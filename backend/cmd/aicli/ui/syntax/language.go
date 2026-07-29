package syntax

import (
	"path/filepath"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// NormalizeLanguage maps common fence aliases to Chroma lexer names.
func NormalizeLanguage(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	lang = strings.TrimPrefix(lang, "language-")
	// Fence info may include attributes: ```go title="x"
	if i := strings.IndexAny(lang, " \t{"); i >= 0 {
		lang = lang[:i]
	}
	switch lang {
	case "js", "javascript", "node":
		return "javascript"
	case "ts", "typescript":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "py", "python", "python3":
		return "python"
	case "sh", "shell", "bash", "zsh", "fish":
		return "bash"
	case "yml", "yaml":
		return "yaml"
	case "md", "markdown":
		return "markdown"
	case "rs", "rust":
		return "rust"
	case "rb", "ruby":
		return "ruby"
	case "cs", "csharp":
		return "csharp"
	case "c++", "cpp", "cxx":
		return "c++"
	case "golang":
		return "go"
	case "kt", "kotlin":
		return "kotlin"
	case "plaintext", "text", "txt", "plain":
		return "plaintext"
	default:
		return lang
	}
}

// ResolveLexer picks a Chroma lexer from explicit language, filename, or analyse.
func ResolveLexer(language, filename, code string) chroma.Lexer {
	if lang := NormalizeLanguage(language); lang != "" && lang != "plaintext" {
		if lex := lexers.Get(lang); lex != nil {
			return chroma.Coalesce(lex)
		}
	}
	if filename != "" {
		base := filepath.Base(filename)
		if lex := lexers.Match(base); lex != nil {
			return chroma.Coalesce(lex)
		}
	}
	if code != "" {
		if lex := lexers.Analyse(code); lex != nil {
			return chroma.Coalesce(lex)
		}
	}
	return chroma.Coalesce(lexers.Fallback)
}

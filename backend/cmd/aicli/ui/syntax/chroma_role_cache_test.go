package syntax

import (
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// chromaRoleFor memoizes "Code."+TokenType.String(). The memo may only remove
// allocations, never change the role string: a wrong role would re-theme code
// blocks. Cover the whole table of token types the highlighter classifies
// against, then the exact types a real lexer emits on the hot path.
func TestChromaRoleForMatchesInlineConcat(t *testing.T) {
	types := []chroma.TokenType{
		chroma.None,
		chroma.Text,
		chroma.TextWhitespace,
		chroma.Keyword,
		chroma.KeywordConstant,
		chroma.KeywordDeclaration,
		chroma.Name,
		chroma.NameFunction,
		chroma.NameBuiltin,
		chroma.Literal,
		chroma.LiteralString,
		chroma.LiteralNumber,
		chroma.Operator,
		chroma.Punctuation,
		chroma.Comment,
		chroma.CommentSingle,
		chroma.GenericDeleted,
		chroma.GenericHeading,
		chroma.Error,
	}
	for _, tokType := range types {
		want := "Code." + tokType.String()
		// First call populates the memo, later calls must return the same value.
		for attempt := 0; attempt < 3; attempt++ {
			if got := chromaRoleFor(tokType); got != want {
				t.Fatalf("chromaRoleFor(%v) attempt %d = %q, want %q", tokType, attempt, got, want)
			}
		}
	}
}

// TestChromaRoleForCoversLexedTokens proves that every style role produced by
// tokensToLines for a real sample still equals the inline concatenation the
// memo replaced.
func TestChromaRoleForCoversLexedTokens(t *testing.T) {
	sample := "package main\n\nimport \"fmt\"\n\n// doc\nfunc main() {\n\tv := 42\n\tfmt.Println(v, `raw`, 3.14)\n}\n"
	lexer := ResolveLexer("go", "", sample)
	if lexer == nil {
		t.Skip("go lexer unavailable")
	}
	iterator, err := lexer.Tokenise(nil, sample)
	if err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	seen := map[chroma.TokenType]bool{}
	for tok := iterator(); tok != chroma.EOF; tok = iterator() {
		seen[tok.Type] = true
		if got, want := chromaRoleFor(tok.Type), "Code."+tok.Type.String(); got != want {
			t.Fatalf("role for %v = %q, want %q", tok.Type, got, want)
		}
	}
	if len(seen) < 4 {
		t.Fatalf("sample only exercised %d token types: %v", len(seen), seen)
	}
}

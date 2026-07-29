package syntax

import (
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// DiffScopeBackgrounds carries the raw diff backgrounds a syntax theme
// declares for inserted/deleted scopes.
//
// Values are always RGB (or unset). Depth adaptation is intentionally left to
// the diff renderer, which knows whether the terminal wants truecolor, a
// quantized xterm index, or no background at all.
type DiffScopeBackgrounds struct {
	Inserted render.Color
	Deleted  render.Color
}

// ResolveThemeName applies the same fallback chain the highlighter uses:
// explicit name, then the process-wide default.
func ResolveThemeName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, "auto") {
		return GlobalDefaultTheme()
	}
	return name
}

// DiffScopeBackgroundsFor reports the theme-declared backgrounds for
// added/removed source lines.
//
// Chroma's GenericInserted/GenericDeleted tokens are the equivalent of the
// TextMate markup.inserted / markup.deleted scopes. Themes that do not define
// them yield unset colors so callers keep their own palette.
func DiffScopeBackgroundsFor(themeName string) DiffScopeBackgrounds {
	chromaStyle := styles.Get(ResolveThemeName(themeName))
	if chromaStyle == nil {
		return DiffScopeBackgrounds{}
	}
	return DiffScopeBackgrounds{
		Inserted: scopeBackground(chromaStyle, chroma.GenericInserted),
		Deleted:  scopeBackground(chromaStyle, chroma.GenericDeleted),
	}
}

// scopeBackground extracts an explicitly declared background for one token
// type.
//
// Style.Get inherits from parent categories and ultimately from the theme's
// own Background entry, so an undefined diff scope would otherwise report the
// editor background and paint every changed line. Has() plus an explicit
// comparison against the global background keeps only real declarations.
func scopeBackground(chromaStyle *chroma.Style, tokenType chroma.TokenType) render.Color {
	if !chromaStyle.Has(tokenType) {
		return render.Color{}
	}
	background := chromaStyle.Get(tokenType).Background
	if !background.IsSet() {
		return render.Color{}
	}
	if global := chromaStyle.Get(chroma.Background).Background; global.IsSet() && global == background {
		return render.Color{}
	}
	return render.RGB(background.Red(), background.Green(), background.Blue())
}

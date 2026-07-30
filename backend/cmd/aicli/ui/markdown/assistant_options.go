package markdown

import (
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/syntax"
)

// ApplyAssistantBodyContract forces the options that keep ActiveBand body
// rendering and Formatter.Format / history replay on the same blank-line and
// plain-text contract. Hyperlinks stay off so link labels keep the visible
// "text (url)" fallback used by scrollback transcripts.
func (o *Options) ApplyAssistantBodyContract() {
	if o == nil {
		return
	}
	o.Hyperlinks = false
	if o.TableMode != TableGrid && o.TableMode != TableRecords && o.TableMode != TablePlain {
		o.TableMode = TableAuto
	}
}

// AssistantBodyOptions builds shared assistant-body options from a theme
// context. Callers that paint a live ActiveBand may still set Highlighter and
// HideHighlightFallback after this baseline.
func AssistantBodyOptions(width int, theme style.ThemeContext) Options {
	opts := DefaultOptions(width, theme)
	opts.ApplyAssistantBodyContract()
	return opts
}

// ActiveBandBodyOptions builds ActiveBand markdown options on top of the
// shared assistant-body contract. HideHighlightFallback keeps large-block
// skip labels out of the live viewport.
func ActiveBandBodyOptions(width int, syntaxTheme string, highlighter syntax.Highlighter) Options {
	if width <= 0 {
		width = 80
	}
	opts := Options{
		Width:       width,
		TableMode:   TableAuto,
		SyntaxTheme: syntaxTheme,
		Highlighter: highlighter,
	}
	opts.ApplyAssistantBodyContract()
	opts.HideHighlightFallback = true
	return opts
}

package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// CurrentColorProfile returns a best-effort color profile for the process.
// Offline defaults come from AICLI_OSC_FG / AICLI_OSC_BG; on interactive ANSI
// terminals a process-once bounded OSC 10/11 probe fills remaining gaps
// (honors AICLI_DISABLE_OSC_PROBE).
func CurrentColorProfile() style.ColorProfile {
	term := NewTerminal()
	caps := term.Capabilities()
	opts := style.DetectOptions{
		Interactive:   caps.Interactive,
		ANSICapable:   caps.ANSI,
		ColorOverride: "auto",
		DepthOverride: "auto",
	}
	if caps.Interactive && caps.ANSI {
		opts.OSCProbe = LiveOSCProbe()
	}
	return style.DetectColorProfile(opts)
}

// CurrentThemeContext builds a ThemeContext from the active palette/mode.
func CurrentThemeContext() style.ThemeContext {
	return ThemeContextForProfile(CurrentColorProfile())
}

// ThemeContextForProfile combines the active palette selection with a known
// terminal profile. Surfaces should prefer their existing driver profile so
// rendering does not perform another terminal capability probe per frame.
func ThemeContextForProfile(profile style.ColorProfile) style.ThemeContext {
	return ThemeContextForTheme(GetTheme(ThemeAuto), profile)
}

// ThemeContextForTheme combines an explicit compatibility Theme selection
// with a negotiated profile without assuming truecolor or a second color
// implementation.
func ThemeContextForTheme(theme *Theme, profile style.ColorProfile) style.ThemeContext {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	sel := style.ThemeSelection{
		PaletteName: theme.Name,
		SyntaxName:  CurrentSyntaxThemeName(),
		Mode:        themeModeFromType(theme.Type),
	}
	return style.BuildThemeContext(sel, profile)
}

// renderDocumentWithProfile resolves semantic roles through the selected
// palette and the terminal's real color depth. The ANSI backend itself owns
// NoColor degradation, so every caller follows the same structured path.
func renderDocumentWithProfile(doc render.Document, theme *Theme) string {
	return style.RenderDocument(doc, ThemeContextForTheme(theme, CurrentColorProfile()))
}

// renderDocumentWithThemeProfile resolves a document with an already-known
// terminal profile. Long-lived surfaces should use this path so every frame
// shares the same capability decision.
func renderDocumentWithThemeProfile(doc render.Document, theme *Theme, profile style.ColorProfile) string {
	return style.RenderDocument(doc, ThemeContextForTheme(theme, profile))
}

// RoleTextDocument converts untrusted text into semantic spans. Newlines are
// represented as Line boundaries so Span.Text never contains terminal control
// sequences or embedded newlines.
func RoleTextDocument(text string, role style.Role) render.Document {
	safe := strings.ReplaceAll(SanitizeTerminalText(text), "\r", "")
	parts := strings.Split(safe, "\n")
	lines := make([]render.Line, 0, len(parts))
	for _, part := range parts {
		lines = append(lines, render.Line{Spans: []render.Span{
			{Text: part, Style: render.Style{Role: string(role)}},
		}})
	}
	return render.LinesDoc(lines...)
}

// RenderRoleText renders semantic text through the active palette and the
// negotiated terminal color profile.
func RenderRoleText(text string, role style.Role) string {
	return RenderRoleTextWithTheme(text, role, GetTheme(ThemeAuto))
}

// RenderRoleTextWithTheme is the compatibility bridge for legacy string APIs.
// It intentionally remains a Document encoder rather than a string colorizer.
func RenderRoleTextWithTheme(text string, role style.Role, theme *Theme) string {
	return renderDocumentWithProfile(RoleTextDocument(text, role), theme)
}

func themeModeFromType(t ThemeType) style.ThemeMode {
	switch t {
	case ThemeLight:
		return style.ThemeModeLight
	case ThemeDark:
		return style.ThemeModeDark
	default:
		return style.ThemeModeAuto
	}
}

// RenderDocumentANSI resolves roles with the current theme context and encodes ANSI.
func RenderDocumentANSI(doc render.Document) string {
	return style.RenderDocument(doc, CurrentThemeContext())
}

// RenderDocumentPlain returns plain text for a document.
func RenderDocumentPlain(doc render.Document) string {
	return render.PlainBackend{}.Render(doc)
}

// SanitizeToolOutput removes terminal control sequences from tool/shell output.
// Allowed path: plain text only. External ANSI must go through render.ANSIToSpans.
func SanitizeToolOutput(text string) string {
	return SanitizeTerminalText(text)
}

// PreviewToolOutputANSI parses trusted-local ANSI into safe plain text for
// previews. Control sequences that move the cursor or change terminal state
// are dropped; SGR colors are stripped in the plain projection.
func PreviewToolOutputANSI(text string) string {
	return render.ANSIToPlain(text)
}

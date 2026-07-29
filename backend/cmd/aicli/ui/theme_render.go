package ui

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/cell"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/diff"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

func (t *Theme) ColorizeSecondary(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleTextSecondary, t)
}

func (t *Theme) ColorizeMuted(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleTextMuted, t)
}

func (t *Theme) ColorizeLabel(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleMetaLabel, t)
}

func StyleAssistantSupplementLine(line string) string {
	return GetTheme(ThemeAuto).StyleAssistantSupplementLine(line)
}

// FormatAssistantSupplementBlock styles a multi-line assistant supplement.
//
// Phase 3/5: prefers typed diff rendering for "• Edited/• Diff" blocks and typed
// TimelineEvent.Document for known timeline lines whose plain projection
// matches the original layout. Falls back to legacy per-line styling for
// bullets, unknown tags, and non-timeline content.
func FormatAssistantSupplementBlock(text string) string {
	if text == "" {
		return ""
	}
	text = normalizeSupplementBlockText(text)
	if text == "" {
		return ""
	}

	// Structured edit/read-only diff block (colored path only). Plain/NoColor keeps
	// the original layout via per-line styling so transcripts stay stable.
	if supplements := diff.ParseSupplementBlocks(text); len(supplements) > 0 {
		theme := supplementThemeContext()
		if theme.Terminal.Enabled {
			opts := diff.DefaultRenderOptions(GetTerminalWidth(), theme)
			opts.ShowLineNo = true
			doc := diff.SupplementDocument(supplements, opts)
			return style.RenderDocument(doc, theme)
		}
	}

	// Per-line path: StyleAssistantSupplementLine routes known layout-identical
	// bracket tags through TimelineEvent.Document; bullets/unknown stay legacy.
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = StyleAssistantSupplementLine(line)
	}
	return strings.Join(lines, "\n")
}

// supplementThemeContext resolves the user's palette, syntax theme and the
// terminal's real color depth instead of assuming focus/dark/TrueColor, so
// pipes, ANSI-16 terminals and light backgrounds degrade correctly.
func supplementThemeContext() style.ThemeContext {
	return CurrentThemeContext()
}

func normalizeSupplementBlockText(text string) string {
	if text == "" {
		return ""
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.Trim(text, "\n")
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	normalized := make([]string, 0, len(lines))
	blankRun := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blankRun++
			if blankRun > 1 {
				continue
			}
			normalized = append(normalized, "")
			continue
		}
		blankRun = 0
		normalized = append(normalized, line)
	}
	for len(normalized) > 0 && normalized[0] == "" {
		normalized = normalized[1:]
	}
	for len(normalized) > 0 && normalized[len(normalized)-1] == "" {
		normalized = normalized[:len(normalized)-1]
	}
	return strings.Join(normalized, "\n")
}

func (t *Theme) StyleAssistantSupplementLine(line string) string {
	if t == nil || line == "" {
		return line
	}
	line = SanitizeTerminalText(line)
	leading, body := splitLeadingWhitespace(line)
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return line
	}
	if isAssistantSupplementDivider(trimmed) {
		return t.renderSupplementLine(leading, body, dividerRoleForLine(trimmed))
	}
	// Known layout-identical timeline lines (including bullet markers) use
	// Document role spans. Unknown tags and non-equivalent projections stay
	// on the stable legacy coloring path below.
	if styled, ok := renderKnownTimelineSupplementLine(line, t); ok {
		return styled
	}
	if ev, ok := parseSupplementTimeline(line); ok {
		return t.styleTimelineEvent(leading, body, ev)
	}
	if styled, ok := t.styleEditedDiffSupplementLine(leading, body); ok {
		return styled
	}
	if prefix, rest, ok := splitBulletStatus(body); ok {
		return t.renderSupplementSpans(leading,
			semanticSpan(prefix, style.RoleTool, true),
			semanticSpan(rest, style.RoleTextSecondary, false),
		)
	}
	if strings.HasPrefix(trimmed, "failed:") {
		return t.renderSupplementLine(leading, body, style.RoleError)
	}
	if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
		return t.renderSupplementLine(leading, body, style.RoleTextSecondary)
	}
	return t.renderSupplementLine(leading, body, style.RoleTextMuted)
}

func semanticSpan(text string, role style.Role, bold bool) render.Span {
	return render.Span{Text: text, Style: render.Style{Role: string(role), Bold: bold}}
}

func (t *Theme) renderSupplementLine(leading, body string, role style.Role) string {
	return t.renderSupplementSpans(leading, semanticSpan(body, role, false))
}

func (t *Theme) renderSupplementSpans(leading string, spans ...render.Span) string {
	doc := render.SingleLineDoc(spans...)
	return leading + renderDocumentWithProfile(doc, t)
}

// renderKnownTimelineSupplementLine styles a timeline line via
// TimelineEvent.Document when the plain projection matches the source layout.
// Unknown tags and non-equivalent projections return false so callers keep
// the legacy path. Bullet markers are preserved via TimelineEvent.Marker.
func renderKnownTimelineSupplementLine(line string, theme *Theme) (string, bool) {
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	leading, body := splitLeadingWhitespace(line)
	if body == "" {
		return "", false
	}
	ev, ok := cell.LegacyTimelineParser(line)
	if !ok || ev.Kind == cell.TimelineUnknown {
		return "", false
	}
	plain := ev.FormatPlain()
	if plain != body {
		return "", false
	}
	if theme == nil {
		return leading + plain, true
	}
	return leading + renderDocumentWithProfile(ev.Document(), theme), true
}

func parseSupplementTimeline(line string) (cell.TimelineEvent, bool) {
	return cell.LegacyTimelineParser(line)
}

func (t *Theme) styleTimelineEvent(leading, body string, ev cell.TimelineEvent) string {
	tagRole, bodyRole := rolesForTimeline(ev)
	if ev.Status == cell.StatusError {
		tagRole, bodyRole = style.RoleError, style.RoleError
	}
	// Re-split so we color the original tag/bullet text stably.
	if bullet, tag, rest, ok := splitBulletBracketTag(body); ok {
		return t.renderSupplementSpans(leading,
			semanticSpan(bullet, tagRole, true),
			semanticSpan(tag, tagRole, true),
			semanticSpan(rest, bodyRole, false),
		)
	}
	if tag, rest, ok := splitBracketTag(body); ok {
		return t.renderSupplementSpans(leading,
			semanticSpan(tag, tagRole, true),
			semanticSpan(rest, bodyRole, false),
		)
	}
	if strings.HasPrefix(strings.TrimLeft(body, " \t"), "• ") {
		return t.renderSupplementSpans(leading, semanticSpan(body, tagRole, true))
	}
	return t.renderSupplementLine(leading, body, tagRole)
}

func rolesForTimeline(ev cell.TimelineEvent) (style.Role, style.Role) {
	bodyRole := style.RoleTextSecondary
	switch ev.Kind {
	case cell.TimelineTeam, cell.TimelineTask, cell.TimelineProgress,
		cell.TimelineTip, cell.TimelineInput, cell.TimelineNotice:
		bodyRole = style.RoleTextMuted
	}
	return cell.RoleForKind(ev.Kind), bodyRole
}

func (t *Theme) styleEditedDiffSupplementLine(leading, body string) (string, bool) {
	if t == nil {
		return "", false
	}
	if strings.HasPrefix(body, "• Edited ") {
		return t.renderSupplementLine(leading, body, style.RoleTool), true
	}
	marker, ok := editedDiffSupplementMarker(body)
	if !ok {
		return "", false
	}
	switch marker {
	case '+':
		return t.renderSupplementLine(leading, body, style.RoleSuccess), true
	case '-':
		return t.renderSupplementLine(leading, body, style.RoleError), true
	default:
		return "", false
	}
}

func editedDiffSupplementMarker(body string) (rune, bool) {
	i := 0
	for i < len(body) && body[i] >= '0' && body[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, false
	}
	spaceCount := 0
	for i < len(body) && body[i] == ' ' {
		i++
		spaceCount++
	}
	if spaceCount == 0 || i >= len(body) {
		return 0, false
	}
	switch body[i] {
	case '+', '-':
		return rune(body[i]), true
	default:
		return 0, false
	}
}

func splitLeadingWhitespace(text string) (string, string) {
	for i, r := range text {
		if r != ' ' && r != '\t' {
			return text[:i], text[i:]
		}
	}
	return text, ""
}

func splitBracketTag(text string) (string, string, bool) {
	if !strings.HasPrefix(text, "[") {
		return "", "", false
	}
	idx := strings.Index(text, "]")
	if idx <= 0 {
		return "", "", false
	}
	return text[:idx+1], text[idx+1:], true
}

func splitBulletStatus(text string) (string, string, bool) {
	for _, prefix := range []string{"• Running ", "• Ran "} {
		if strings.HasPrefix(text, prefix) {
			return prefix, text[len(prefix):], true
		}
	}
	return "", "", false
}

func splitBulletBracketTag(text string) (string, string, string, bool) {
	if !strings.HasPrefix(text, "• ") {
		return "", "", "", false
	}
	tag, rest, ok := splitBracketTag(text[2:])
	if !ok {
		return "", "", "", false
	}
	return "• ", tag, rest, true
}

func isAssistantSupplementDivider(text string) bool {
	return strings.HasPrefix(text, "──") || strings.HasPrefix(text, "═") || strings.HasPrefix(text, "---")
}

func dividerRoleForLine(text string) style.Role {
	switch {
	case strings.Contains(text, "reasoning"):
		return style.RoleReasoning
	case strings.Contains(text, "command"):
		return style.RoleTool
	default:
		return style.RoleBorder
	}
}

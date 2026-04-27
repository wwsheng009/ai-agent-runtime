package ui

import "strings"

func (t *Theme) ColorizeSecondary(text string) string {
	if t == nil {
		return text
	}
	return t.SecondaryColor.Sprint(text)
}

func (t *Theme) ColorizeMuted(text string) string {
	if t == nil {
		return text
	}
	return t.MutedColor.Sprint(text)
}

func (t *Theme) ColorizeLabel(text string) string {
	if t == nil {
		return text
	}
	return t.MetaLabelColor.Sprint(text)
}

func StyleAssistantSupplementLine(line string) string {
	return GetTheme(ThemeAuto).StyleAssistantSupplementLine(line)
}

func FormatAssistantSupplementBlock(text string) string {
	if text == "" {
		return ""
	}
	text = normalizeSupplementBlockText(text)
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = StyleAssistantSupplementLine(line)
	}
	return strings.Join(lines, "\n")
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
		return leading + dividerColorForLine(t, trimmed).Sprint(body)
	}
	if bullet, tag, rest, ok := splitBulletBracketTag(body); ok {
		tagColor, bodyColor := t.assistantSupplementColors(tag)
		return leading + tagColor.Sprint(bullet) + tagColor.Sprint(tag) + bodyColor.Sprint(rest)
	}
	if tag, rest, ok := splitBracketTag(body); ok {
		tagColor, bodyColor := t.assistantSupplementColors(tag)
		return leading + tagColor.Sprint(tag) + bodyColor.Sprint(rest)
	}
	if prefix, rest, ok := splitBulletStatus(body); ok {
		tagColor, bodyColor := themeColor(t.ToolColor), themeColor(t.SecondaryColor)
		return leading + tagColor.Sprint(prefix) + bodyColor.Sprint(rest)
	}
	if strings.HasPrefix(trimmed, "failed:") {
		return leading + t.ErrorColor.Sprint(body)
	}
	if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
		return leading + t.ColorizeSecondary(body)
	}
	return leading + t.ColorizeMuted(body)
}

func (t *Theme) assistantSupplementColors(tag string) (*ThemeColor, *ThemeColor) {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "[tool]", "[tool done]", "[tool denied]", "[tools]":
		return themeColor(t.ToolColor), themeColor(t.SecondaryColor)
	case "[approval]":
		return themeColor(t.ApprovalColor), themeColor(t.SecondaryColor)
	case "[question]":
		return themeColor(t.InfoColor), themeColor(t.SecondaryColor)
	case "[reasoning]":
		return themeColor(t.ReasoningColor), themeColor(t.SecondaryColor)
	case "[thinking]", "[planning]", "[progress]", "[team]", "[team summary]", "[subagent]", "[task]", "[tip]", "[input]":
		return themeColor(t.TimelineColor), themeColor(t.MutedColor)
	default:
		return themeColor(t.TimelineColor), themeColor(t.SecondaryColor)
	}
}

type ThemeColor struct {
	sprint func(...interface{}) string
}

func themeColor(c interface{ Sprint(...interface{}) string }) *ThemeColor {
	return &ThemeColor{sprint: c.Sprint}
}

func (c *ThemeColor) Sprint(args ...interface{}) string {
	if c == nil || c.sprint == nil {
		return ""
	}
	return c.sprint(args...)
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

func dividerColorForLine(theme *Theme, text string) *ThemeColor {
	switch {
	case strings.Contains(text, "reasoning"):
		return themeColor(theme.ReasoningColor)
	case strings.Contains(text, "command"):
		return themeColor(theme.ToolColor)
	default:
		return themeColor(theme.SeparatorColor)
	}
}

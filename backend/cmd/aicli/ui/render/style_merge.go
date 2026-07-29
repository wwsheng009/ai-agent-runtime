package render

// MergeStyles combines base and overlay using explicit override rules.
// Overlay wins for set colors and true modifiers; false modifiers do not clear
// true base modifiers unless ClearModifiers is used.
func MergeStyles(base, overlay Style) Style {
	out := base
	if overlay.Foreground.IsSet() {
		out.Foreground = overlay.Foreground
	}
	if overlay.Background.IsSet() {
		out.Background = overlay.Background
	}
	if overlay.Bold {
		out.Bold = true
	}
	if overlay.Dim {
		out.Dim = true
	}
	if overlay.Italic {
		out.Italic = true
	}
	if overlay.Underline {
		out.Underline = true
	}
	if overlay.Reverse {
		out.Reverse = true
	}
	if overlay.Role != "" {
		out.Role = overlay.Role
	}
	return out
}

// EffectiveSpanStyle merges line style under span style.
// Span colors and modifiers override the line; line background is retained
// when the span does not set a background (useful for Diff row tints).
func EffectiveSpanStyle(line Style, span Style) Style {
	out := MergeStyles(line, span)
	if !span.Background.IsSet() && line.Background.IsSet() {
		out.Background = line.Background
	}
	if span.Role == "" && line.Role != "" {
		out.Role = line.Role
	}
	return out
}

// ClearModifiers returns a style with only colors retained.
func ClearModifiers(s Style) Style {
	return Style{
		Foreground: s.Foreground,
		Background: s.Background,
		Role:       s.Role,
	}
}

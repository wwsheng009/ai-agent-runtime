package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
)

// Palette preset names (axis 2: color scheme).
const (
	ThemePresetClassic  = "classic"
	ThemePresetFocus    = "focus"
	ThemePresetContrast = "contrast"
	ThemePresetMono     = "mono"
)

var (
	currentThemeName = ThemePresetFocus
	themePresetNames = []string{
		ThemePresetClassic,
		ThemePresetFocus,
		ThemePresetContrast,
		ThemePresetMono,
	}
	themeModeNames = []string{
		ThemeModeAuto,
		ThemeModeDark,
		ThemeModeLight,
	}
)

// SupportedThemePresetNames returns sorted palette names.
func SupportedThemePresetNames() []string {
	out := append([]string(nil), themePresetNames...)
	sort.Strings(out)
	return out
}

// SupportedThemeModeNames returns mode names in a stable UI order.
func SupportedThemeModeNames() []string {
	return append([]string(nil), themeModeNames...)
}

// NormalizeThemePresetName maps theme aliases to a canonical palette name.
// Empty/"default"/"balanced" normalize to focus; unknown names return "".
func NormalizeThemePresetName(raw string) string {
	return normalizeThemePresetName(raw)
}

func normalizeThemePresetName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "default", "balanced":
		return ThemePresetFocus
	case "classic":
		return ThemePresetClassic
	case "focus":
		return ThemePresetFocus
	case "contrast", "high-contrast":
		return ThemePresetContrast
	case "mono", "minimal":
		return ThemePresetMono
	default:
		return ""
	}
}

// NormalizeThemeModeName maps mode aliases to auto|dark|light.
// Empty normalizes to auto; unknown names return "".
func NormalizeThemeModeName(raw string) string {
	return normalizeThemeModeName(raw)
}

func normalizeThemeModeName(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "auto", "default", "system":
		return ThemeModeAuto
	case "dark", "night", "black":
		return ThemeModeDark
	case "light", "day", "white":
		return ThemeModeLight
	default:
		return ""
	}
}

// ThemeModeFromName converts a mode name into ThemeType.
// Unknown values return ThemeAuto.
func ThemeModeFromName(raw string) ThemeType {
	switch normalizeThemeModeName(raw) {
	case ThemeModeLight:
		return ThemeLight
	case ThemeModeDark:
		return ThemeDark
	default:
		return ThemeAuto
	}
}

// ThemeModeName returns the canonical name for a ThemeType preference.
func ThemeModeName(mode ThemeType) string {
	switch mode {
	case ThemeLight:
		return ThemeModeLight
	case ThemeDark:
		return ThemeModeDark
	default:
		return ThemeModeAuto
	}
}

// CurrentThemeName returns the active palette name.
func CurrentThemeName() string {
	themeMutex.RLock()
	defer themeMutex.RUnlock()
	return normalizeThemePresetName(currentThemeName)
}

// CurrentThemeModeName returns the active mode preference name (auto|dark|light).
func CurrentThemeModeName() string {
	themeMutex.RLock()
	defer themeMutex.RUnlock()
	return ThemeModeName(currentThemeMode)
}

// CurrentThemeMode returns the active mode preference.
func CurrentThemeMode() ThemeType {
	themeMutex.RLock()
	defer themeMutex.RUnlock()
	return currentThemeMode
}

// CurrentThemeResolvedModeName returns the effective light/dark after auto-detect.
func CurrentThemeResolvedModeName() string {
	theme := GetTheme(ThemeAuto)
	if theme == nil {
		return ThemeModeDark
	}
	return ThemeModeName(theme.Type)
}

// SetThemePreset switches the palette axis and rebuilds the theme using the current mode.
func SetThemePreset(name string) error {
	normalized := normalizeThemePresetName(name)
	if normalized == "" {
		return fmt.Errorf("未知配色: %s（可选值: %s）", strings.TrimSpace(name), strings.Join(SupportedThemePresetNames(), "|"))
	}

	themeMutex.Lock()
	defer themeMutex.Unlock()

	currentThemeName = normalized
	currentTheme = createTheme(currentThemeMode)
	return nil
}

// SetThemeMode switches the light/dark preference and rebuilds with the current palette.
func SetThemeMode(mode string) error {
	normalized := normalizeThemeModeName(mode)
	if normalized == "" {
		return fmt.Errorf("未知明暗模式: %s（可选值: %s）", strings.TrimSpace(mode), strings.Join(SupportedThemeModeNames(), "|"))
	}

	themeMutex.Lock()
	defer themeMutex.Unlock()

	currentThemeMode = ThemeModeFromName(normalized)
	currentTheme = createTheme(currentThemeMode)
	return nil
}

// ApplyThemeSelection applies palette and/or mode changes.
// Empty strings leave the corresponding axis unchanged.
func ApplyThemeSelection(palette string, mode string) error {
	palette = strings.TrimSpace(palette)
	mode = strings.TrimSpace(mode)
	if palette == "" && mode == "" {
		return nil
	}

	var normalizedPalette string
	if palette != "" {
		normalizedPalette = normalizeThemePresetName(palette)
		if normalizedPalette == "" {
			return fmt.Errorf("未知配色: %s（可选值: %s）", palette, strings.Join(SupportedThemePresetNames(), "|"))
		}
	}

	var normalizedMode string
	if mode != "" {
		normalizedMode = normalizeThemeModeName(mode)
		if normalizedMode == "" {
			return fmt.Errorf("未知明暗模式: %s（可选值: %s）", mode, strings.Join(SupportedThemeModeNames(), "|"))
		}
	}

	themeMutex.Lock()
	defer themeMutex.Unlock()

	if normalizedPalette != "" {
		currentThemeName = normalizedPalette
	}
	if normalizedMode != "" {
		currentThemeMode = ThemeModeFromName(normalizedMode)
	}
	currentTheme = createTheme(currentThemeMode)
	return nil
}

// ThemeSelectionDescription returns a short human-readable status string.
func ThemeSelectionDescription() string {
	return fmt.Sprintf("mode=%s palette=%s", CurrentThemeModeName(), CurrentThemeName())
}

// ThemeModeDescription returns a short Chinese description for a mode name.
func ThemeModeDescription(name string) string {
	switch normalizeThemeModeName(name) {
	case ThemeModeAuto:
		return "自动明暗（跟随终端）"
	case ThemeModeDark:
		return "暗色模式"
	case ThemeModeLight:
		return "亮色模式"
	default:
		return ""
	}
}

// ThemePresetDescription returns a short Chinese description for a palette name.
func ThemePresetDescription(name string) string {
	switch normalizeThemePresetName(name) {
	case ThemePresetClassic:
		return "经典配色（传统 aicli 强调色）"
	case ThemePresetFocus:
		return "聚焦配色（默认，主内容高对比）"
	case ThemePresetContrast:
		return "高对比配色（加粗强调，便于辨识）"
	case ThemePresetMono:
		return "单色配色（仅明暗/粗细，无彩色语义）"
	default:
		return ""
	}
}

// BuildThemePreview builds a theme for display without mutating global selection.
// Empty palette/mode keep the current global axis values.
// Auto mode resolves to the effective terminal light/dark for a realistic sample.
func BuildThemePreview(palette string, mode string) *Theme {
	themeMutex.RLock()
	currentPalette := currentThemeName
	currentMode := currentThemeMode
	themeMutex.RUnlock()

	palette = strings.TrimSpace(palette)
	mode = strings.TrimSpace(mode)

	resolvedPalette := currentPalette
	if palette != "" {
		if normalized := normalizeThemePresetName(palette); normalized != "" {
			resolvedPalette = normalized
		}
	}

	modePref := currentMode
	if mode != "" {
		if normalized := normalizeThemeModeName(mode); normalized != "" {
			modePref = ThemeModeFromName(normalized)
		}
	}

	// Resolve to concrete light/dark so createThemeWithPalette does not read globals.
	actualType := modePref
	switch modePref {
	case ThemeLight, ThemeDark:
		// keep
	default:
		actualType = detectTerminalThemeType()
		if actualType != ThemeLight && actualType != ThemeDark {
			actualType = ThemeDark
		}
	}

	return createThemeWithPalette(actualType, resolvedPalette)
}

// FormatThemePreviewSample returns a short colored sample line for a theme.
// Useful for /theme list and /theme preview.
func FormatThemePreviewSample(theme *Theme) string {
	if theme == nil {
		return ""
	}
	parts := []string{
		safeColorSprint(theme.UserColor, "user"),
		safeColorSprint(theme.AssistantColor, "assistant"),
		safeColorSprint(theme.ToolColor, "tool"),
		safeColorSprint(theme.ReasoningColor, "reason"),
		safeColorSprint(theme.ErrorColor, "err"),
		safeColorSprint(theme.SuccessColor, "ok"),
		safeColorSprint(theme.MutedColor, "muted"),
	}
	return strings.Join(parts, " ")
}

func safeColorSprint(c *color.Color, text string) string {
	if c == nil {
		return text
	}
	return c.Sprint(text)
}

func applyThemePreset(theme *Theme, presetName string) {
	if theme == nil {
		return
	}
	presetName = normalizeThemePresetName(presetName)
	if presetName == "" {
		presetName = ThemePresetFocus
	}
	theme.Name = presetName

	switch presetName {
	case ThemePresetClassic:
		applyClassicTheme(theme)
	case ThemePresetContrast:
		applyContrastTheme(theme)
	case ThemePresetMono:
		applyMonoTheme(theme)
	default:
		applyFocusTheme(theme)
	}
}

func applyClassicTheme(theme *Theme) {
	if theme == nil {
		return
	}
	if theme.Type == ThemeLight {
		// Classic light: traditional magenta/yellow accents on a light base.
		theme.UserColor = color.New(color.FgBlue, color.Bold)
		theme.AssistantColor = color.New(color.FgBlack)
		theme.SystemColor = color.New(color.FgYellow)
		theme.CommandColor = color.New(color.FgMagenta)
		theme.OutputColor = color.New(color.Reset)
		theme.SecondaryColor = color.New(color.FgBlack)
		theme.MutedColor = color.New(color.FgHiBlack)
		theme.MetaLabelColor = color.New(color.FgMagenta)
		theme.TimelineColor = color.New(color.FgHiBlack)
		theme.ToolColor = color.New(color.FgMagenta)
		theme.ReasoningColor = color.New(color.FgYellow)
		theme.ApprovalColor = color.New(color.FgYellow, color.Bold)
		theme.InfoColor = color.New(color.FgMagenta)
		theme.SeparatorColor = color.New(color.FgHiBlack)
		// Status colors keep base light red/yellow/green for semantic clarity.
		return
	}
	// Classic dark: original aicli accent palette layered on dark base.
	theme.UserColor = color.New(color.FgCyan, color.Bold)
	theme.AssistantColor = color.New(color.FgGreen)
	theme.SystemColor = color.New(color.FgHiYellow)
	theme.CommandColor = color.New(color.FgMagenta)
	theme.OutputColor = color.New(color.Reset)
	theme.SecondaryColor = color.New(color.Reset)
	theme.MutedColor = color.New(color.FgHiBlack)
	theme.MetaLabelColor = color.New(color.FgMagenta)
	theme.TimelineColor = color.New(color.FgHiBlack)
	theme.ToolColor = color.New(color.FgMagenta)
	theme.ReasoningColor = color.New(color.FgYellow)
	theme.ApprovalColor = color.New(color.FgYellow, color.Bold)
	theme.InfoColor = color.New(color.FgMagenta)
	theme.SeparatorColor = color.New(color.FgHiBlack)
}

func applyFocusTheme(theme *Theme) {
	if theme == nil {
		return
	}

	if theme.Type == ThemeLight {
		theme.UserColor = color.New(color.FgBlue, color.Bold)
		theme.AssistantColor = color.New(color.FgBlack)
		theme.SystemColor = color.New(color.FgBlue)
		theme.CommandColor = color.New(color.FgHiBlack)
		theme.OutputColor = color.New(color.Reset)
		theme.SecondaryColor = color.New(color.FgBlack)
		theme.MutedColor = color.New(color.FgHiBlack)
		theme.MetaLabelColor = color.New(color.FgHiBlack)
		theme.TimelineColor = color.New(color.FgHiBlack)
		theme.ToolColor = color.New(color.FgBlue, color.Bold)
		theme.ReasoningColor = color.New(color.FgYellow)
		theme.ApprovalColor = color.New(color.FgMagenta, color.Bold)
		theme.SeparatorColor = color.New(color.FgHiBlack)
		theme.InfoColor = color.New(color.FgCyan)
		return
	}

	theme.UserColor = color.New(color.FgCyan, color.Bold)
	theme.AssistantColor = color.New(color.FgHiWhite)
	theme.SystemColor = color.New(color.FgHiBlue)
	theme.CommandColor = color.New(color.FgHiBlack)
	theme.OutputColor = color.New(color.Reset)
	theme.SecondaryColor = color.New(color.FgWhite)
	theme.MutedColor = color.New(color.FgHiBlack)
	theme.MetaLabelColor = color.New(color.FgHiBlack)
	theme.TimelineColor = color.New(color.FgHiBlack)
	theme.ToolColor = color.New(color.FgHiCyan, color.Bold)
	theme.ReasoningColor = color.New(color.FgHiYellow)
	theme.ApprovalColor = color.New(color.FgHiMagenta, color.Bold)
	theme.SeparatorColor = color.New(color.FgHiBlack)
	theme.InfoColor = color.New(color.FgHiCyan)
}

func applyContrastTheme(theme *Theme) {
	if theme == nil {
		return
	}

	if theme.Type == ThemeLight {
		theme.UserColor = color.New(color.FgBlue, color.Bold)
		theme.AssistantColor = color.New(color.FgBlack, color.Bold)
		theme.SystemColor = color.New(color.FgBlue, color.Bold)
		theme.CommandColor = color.New(color.FgBlue, color.Bold)
		theme.OutputColor = color.New(color.Reset)
		theme.SecondaryColor = color.New(color.FgBlack)
		theme.MutedColor = color.New(color.FgHiBlack)
		theme.MetaLabelColor = color.New(color.FgBlue)
		theme.TimelineColor = color.New(color.FgCyan)
		theme.ToolColor = color.New(color.FgMagenta, color.Bold)
		theme.ReasoningColor = color.New(color.FgYellow, color.Bold)
		theme.ApprovalColor = color.New(color.FgRed, color.Bold)
		theme.SeparatorColor = color.New(color.FgBlue)
		theme.InfoColor = color.New(color.FgCyan, color.Bold)
		theme.ErrorColor = color.New(color.FgRed, color.Bold)
		theme.WarningColor = color.New(color.FgYellow, color.Bold)
		theme.SuccessColor = color.New(color.FgGreen, color.Bold)
		theme.ProgressColor = color.New(color.FgGreen, color.Bold)
		return
	}

	theme.UserColor = color.New(color.FgHiCyan, color.Bold)
	theme.AssistantColor = color.New(color.FgHiWhite, color.Bold)
	theme.SystemColor = color.New(color.FgHiBlue, color.Bold)
	theme.CommandColor = color.New(color.FgHiBlue, color.Bold)
	theme.OutputColor = color.New(color.Reset)
	theme.SecondaryColor = color.New(color.FgHiWhite)
	theme.MutedColor = color.New(color.FgWhite)
	theme.MetaLabelColor = color.New(color.FgHiBlue)
	theme.TimelineColor = color.New(color.FgCyan)
	theme.ToolColor = color.New(color.FgHiMagenta, color.Bold)
	theme.ReasoningColor = color.New(color.FgHiYellow, color.Bold)
	theme.ApprovalColor = color.New(color.FgHiRed, color.Bold)
	theme.SeparatorColor = color.New(color.FgHiBlue)
	theme.InfoColor = color.New(color.FgHiCyan, color.Bold)
	theme.ErrorColor = color.New(color.FgHiRed, color.Bold)
	theme.WarningColor = color.New(color.FgHiYellow, color.Bold)
	theme.SuccessColor = color.New(color.FgHiGreen, color.Bold)
	theme.ProgressColor = color.New(color.FgHiGreen, color.Bold)
}

func applyMonoTheme(theme *Theme) {
	if theme == nil {
		return
	}

	base := color.New()
	emphasis := color.New(color.Bold)
	muted := color.New(color.FgHiBlack)
	if theme.Type == ThemeLight {
		// On light backgrounds, plain default fg is fine; keep muted grey.
		muted = color.New(color.FgHiBlack)
	}

	theme.UserColor = emphasis
	theme.AssistantColor = base
	theme.SystemColor = emphasis
	theme.CommandColor = base
	theme.OutputColor = base
	theme.SecondaryColor = base
	theme.MutedColor = muted
	theme.MetaLabelColor = muted
	theme.TimelineColor = muted
	theme.ToolColor = emphasis
	theme.ReasoningColor = emphasis
	theme.ApprovalColor = emphasis
	// Status roles stay monochrome (weight only) so mono never leaks base red/green/yellow.
	theme.ErrorColor = emphasis
	theme.WarningColor = emphasis
	theme.SuccessColor = emphasis
	theme.ProgressColor = emphasis
	theme.InfoColor = base
	theme.SeparatorColor = muted
}

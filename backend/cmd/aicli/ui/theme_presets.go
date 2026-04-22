package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
)

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
)

func SupportedThemePresetNames() []string {
	out := append([]string(nil), themePresetNames...)
	sort.Strings(out)
	return out
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

func CurrentThemeName() string {
	themeMutex.RLock()
	defer themeMutex.RUnlock()
	return normalizeThemePresetName(currentThemeName)
}

func SetThemePreset(name string) error {
	normalized := normalizeThemePresetName(name)
	if normalized == "" {
		return fmt.Errorf("未知主题: %s（可选值: %s）", strings.TrimSpace(name), strings.Join(SupportedThemePresetNames(), "|"))
	}

	themeMutex.Lock()
	defer themeMutex.Unlock()

	currentThemeName = normalized
	requestedType := ThemeAuto
	if currentTheme != nil {
		requestedType = currentTheme.Type
	}
	currentTheme = createTheme(requestedType)
	return nil
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
	theme.SecondaryColor = color.New(color.Reset)
	theme.MutedColor = color.New(color.FgHiBlack)
	theme.MetaLabelColor = color.New(color.FgMagenta)
	theme.TimelineColor = color.New(color.FgHiBlack)
	theme.ToolColor = color.New(color.FgMagenta)
	theme.ReasoningColor = color.New(color.FgYellow)
	theme.ApprovalColor = color.New(color.FgYellow, color.Bold)
}

func applyFocusTheme(theme *Theme) {
	if theme == nil {
		return
	}

	if theme.Type == ThemeLight {
		theme.AssistantColor = color.New(color.FgBlack)
		theme.SystemColor = color.New(color.FgBlue)
		theme.CommandColor = color.New(color.FgHiBlack)
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

	theme.AssistantColor = color.New(color.FgHiWhite)
	theme.SystemColor = color.New(color.FgHiBlue)
	theme.CommandColor = color.New(color.FgHiBlack)
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
		theme.AssistantColor = color.New(color.FgBlack, color.Bold)
		theme.SystemColor = color.New(color.FgBlue, color.Bold)
		theme.CommandColor = color.New(color.FgBlue, color.Bold)
		theme.SecondaryColor = color.New(color.FgBlack)
		theme.MutedColor = color.New(color.FgHiBlack)
		theme.MetaLabelColor = color.New(color.FgBlue)
		theme.TimelineColor = color.New(color.FgCyan)
		theme.ToolColor = color.New(color.FgMagenta, color.Bold)
		theme.ReasoningColor = color.New(color.FgYellow, color.Bold)
		theme.ApprovalColor = color.New(color.FgRed, color.Bold)
		theme.SeparatorColor = color.New(color.FgBlue)
		return
	}

	theme.AssistantColor = color.New(color.FgHiWhite, color.Bold)
	theme.SystemColor = color.New(color.FgHiBlue, color.Bold)
	theme.CommandColor = color.New(color.FgHiBlue, color.Bold)
	theme.SecondaryColor = color.New(color.FgHiWhite)
	theme.MutedColor = color.New(color.FgWhite)
	theme.MetaLabelColor = color.New(color.FgHiBlue)
	theme.TimelineColor = color.New(color.FgCyan)
	theme.ToolColor = color.New(color.FgHiMagenta, color.Bold)
	theme.ReasoningColor = color.New(color.FgHiYellow, color.Bold)
	theme.ApprovalColor = color.New(color.FgHiRed, color.Bold)
	theme.SeparatorColor = color.New(color.FgHiBlue)
}

func applyMonoTheme(theme *Theme) {
	if theme == nil {
		return
	}

	base := color.New()
	emphasis := color.New(color.Bold)
	muted := color.New(color.FgHiBlack)

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
	theme.InfoColor = base
	theme.SeparatorColor = muted
}

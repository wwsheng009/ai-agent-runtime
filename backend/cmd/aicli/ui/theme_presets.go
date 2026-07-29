package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
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
	currentThemeMode = ThemeModeFromName(normalized)
	currentTheme = createTheme(currentThemeMode)
	themeMutex.Unlock()
	refreshAutoSyntaxTheme()
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
	if normalizedPalette != "" {
		currentThemeName = normalizedPalette
	}
	if normalizedMode != "" {
		currentThemeMode = ThemeModeFromName(normalizedMode)
	}
	currentTheme = createTheme(currentThemeMode)
	themeMutex.Unlock()
	refreshAutoSyntaxTheme()
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
	doc := render.SingleLineDoc(themePreviewSampleSpans()...)
	return renderDocumentWithProfile(doc, theme)
}

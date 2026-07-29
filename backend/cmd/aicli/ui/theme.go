package ui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
	"golang.org/x/term"
)

// ThemeType 主题类型（明暗轴）
type ThemeType int

const (
	ThemeAuto  ThemeType = iota // 自动检测终端
	ThemeLight                  // 亮色模式
	ThemeDark                   // 暗色模式
)

// Theme mode preference names (axis 1: light/dark).
const (
	ThemeModeAuto  = "auto"
	ThemeModeLight = "light"
	ThemeModeDark  = "dark"
)

// Theme 主题定义
type Theme struct {
	Type ThemeType
	Name string

	UserIcon      string
	AssistantIcon string
	SystemIcon    string
	CommandIcon   string
	ErrorIcon     string
	WarningIcon   string
	SuccessIcon   string
	InfoIcon      string

	// Shell 图标
	ShellIcon string

	// 边框字符
	BorderHorizontal string
	BorderVertical   string
	Separator        string
}

var (
	// 当前主题实例（单例模式）
	currentTheme *Theme
	themeMutex   sync.RWMutex

	// currentThemeMode is the user preference for the light/dark axis.
	// ThemeAuto means detect from the terminal environment.
	currentThemeMode ThemeType = ThemeAuto
)

// GetTheme 获取主题（单例）
func GetTheme(themeType ThemeType) *Theme {
	themeMutex.Lock()
	defer themeMutex.Unlock()

	if currentTheme != nil && themeMatchesRequest(currentTheme, themeType) && currentTheme.Name == normalizeThemePresetName(currentThemeName) {
		return currentTheme
	}

	currentTheme = createTheme(themeType)
	return currentTheme
}

func themeMatchesRequest(theme *Theme, requested ThemeType) bool {
	if theme == nil {
		return false
	}
	if requested == ThemeAuto {
		// For Auto, cache is valid only when it matches the currently preferred/resolved mode.
		return theme.Type == resolvePreferredThemeType()
	}
	return theme.Type == requested
}

// resolvePreferredThemeType returns the effective light/dark under the current mode preference.
// Caller must hold themeMutex when currentThemeMode may race.
func resolvePreferredThemeType() ThemeType {
	switch currentThemeMode {
	case ThemeLight:
		return ThemeLight
	case ThemeDark:
		return ThemeDark
	default:
		return detectTerminalThemeType()
	}
}

// createTheme builds a theme using the current global palette selection.
func createTheme(themeType ThemeType) *Theme {
	return createThemeWithPalette(themeType, currentThemeName)
}

// createThemeWithPalette builds a theme for an explicit palette without relying
// on currentTheme beyond themeType resolution for ThemeAuto.
// Caller must hold themeMutex when reading currentThemeMode/currentThemeName races matter;
// this helper only reads currentThemeMode via resolvePreferredThemeType for ThemeAuto.
func createThemeWithPalette(themeType ThemeType, palette string) *Theme {
	actualType := themeType
	if themeType == ThemeAuto {
		actualType = resolvePreferredThemeType()
	}
	if actualType == ThemeAuto {
		actualType = ThemeDark
	}

	theme := baseTheme(actualType)
	theme.Name = normalizeThemePresetName(palette)
	if theme.Name == "" {
		theme.Name = ThemePresetFocus
	}

	return theme
}

// baseTheme stores non-style presentation data. Semantic colors live only in
// style.Palette and are resolved at the final render boundary.
func baseTheme(actualType ThemeType) *Theme {
	return &Theme{
		Type:             actualType,
		UserIcon:         ">",
		AssistantIcon:    "",
		SystemIcon:       "ℹ️",
		CommandIcon:      "❯",
		ErrorIcon:        "❌",
		WarningIcon:      "⚠️",
		SuccessIcon:      "✅",
		InfoIcon:         "💡",
		ShellIcon:        "💻",
		BorderHorizontal: "═",
		BorderVertical:   "║",
		Separator:        "─",
	}
}

// detectTerminalThemeType roughly detects light vs dark terminal background.
// Prefer COLORFGBG (common on Unix terminals); default to dark when unknown.
func detectTerminalThemeType() ThemeType {
	if v := strings.TrimSpace(os.Getenv("COLORFGBG")); v != "" {
		parts := strings.Split(v, ";")
		if len(parts) > 0 {
			bgRaw := strings.TrimSpace(parts[len(parts)-1])
			if bg, err := strconv.Atoi(bgRaw); err == nil {
				// ANSI palette: 7/15 are light backgrounds; 0/8 are dark.
				if bg == 7 || bg == 15 {
					return ThemeLight
				}
				return ThemeDark
			}
		}
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("TERM_PROGRAM"))) {
	case "vscode", "iterm.app", "apple_terminal", "windows_terminal":
		return ThemeDark
	}
	return ThemeDark
}

// SetTheme 设置明暗模式偏好（auto/light/dark），并立即重建当前配色。
func SetTheme(themeType ThemeType) {
	themeMutex.Lock()
	currentThemeMode = themeType
	currentTheme = createTheme(themeType)
	themeMutex.Unlock()
	refreshAutoSyntaxTheme()
}

// String 返回主题字符串表示
func (t *Theme) String() string {
	switch t.Type {
	case ThemeLight:
		return "Light"
	case ThemeDark:
		return "Dark"
	default:
		return "Auto"
	}
}

// PrintSeparator 打印分隔线
func (t *Theme) PrintSeparator(width int) {
	_, _ = WriteTerminalLine(os.Stdout, RenderRoleTextWithTheme(strings.Repeat(t.Separator, width), style.RoleBorder, t))
}

// PrintBorder 打印边框分隔线
func (t *Theme) PrintBorder(width int) {
	_, _ = WriteTerminalLine(os.Stdout, RenderRoleTextWithTheme(strings.Repeat(t.BorderHorizontal, width), style.RoleBorder, t))
}

// FormatUser 格式化用户消息
func (t *Theme) FormatUser(text string) string {
	return t.formatRolePrefix(style.RoleUser, t.UserIcon, text)
}

// FormatAssistant 格式化助手消息
func (t *Theme) FormatAssistant(text string) string {
	return t.formatRolePrefix(style.RoleAssistant, t.AssistantIcon, text)
}

// FormatSystem 格式化系统消息
func (t *Theme) FormatSystem(text string) string {
	return t.formatRolePrefix(style.RoleSystem, t.SystemIcon, text)
}

// FormatError 格式化错误消息
func (t *Theme) FormatError(text string) string {
	return t.formatRolePrefix(style.RoleError, t.ErrorIcon, text)
}

// FormatWarning 格式化警告消息
func (t *Theme) FormatWarning(text string) string {
	return t.formatRolePrefix(style.RoleWarning, t.WarningIcon, text)
}

// FormatSuccess 格式化成功消息
func (t *Theme) FormatSuccess(text string) string {
	return t.formatRolePrefix(style.RoleSuccess, t.SuccessIcon, text)
}

// FormatInfo 格式化信息消息
func (t *Theme) FormatInfo(text string) string {
	return t.formatRolePrefix(style.RoleInfo, t.InfoIcon, text)
}

func (t *Theme) formatRolePrefix(role style.Role, icon string, text string) string {
	icon = strings.TrimSpace(SanitizeTerminalText(icon))
	text = SanitizeTerminalText(text)
	var visible string
	if strings.TrimSpace(icon) == "" {
		visible = text
	} else if text == "" {
		visible = icon + " "
	} else {
		visible = icon + " " + text
	}
	doc := RoleTextDocument(visible, role)
	return renderDocumentWithProfile(doc, t)
}

// ColorizeUser 用户消息颜色化
func (t *Theme) ColorizeUser(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleUser, t)
}

// ColorizeAssistant 助手消息颜色化
func (t *Theme) ColorizeAssistant(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleAssistant, t)
}

// ColorizeSystem 系统消息颜色化
func (t *Theme) ColorizeSystem(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleSystem, t)
}

// ColorizeError 错误消息颜色化
func (t *Theme) ColorizeError(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleError, t)
}

// ColorizeWarning 警告消息颜色化
func (t *Theme) ColorizeWarning(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleWarning, t)
}

// ColorizeSuccess 成功消息颜色化
func (t *Theme) ColorizeSuccess(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleSuccess, t)
}

// ColorizeInfo 信息消息颜色化
func (t *Theme) ColorizeInfo(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleInfo, t)
}

// Dimmed 变暗文本
func (t *Theme) Dimmed(text string) string {
	return RenderRoleTextWithTheme(text, style.RoleTextMuted, t)
}

// GetTerminalWidth 获取终端宽度（用于自适应布局）
func GetTerminalWidth() int {
	defaultWidth := 80
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return defaultWidth
}

// GetTerminalHeight 获取终端高度（用于自适应布局）
func GetTerminalHeight() int {
	defaultHeight := 24
	if _, height, err := term.GetSize(int(os.Stdout.Fd())); err == nil && height > 0 {
		return height
	}
	return defaultHeight
}

// CenterText 居中文本
func CenterText(text string, width int) string {
	textLen := len(text) // 简化的长度计算，不处理 ANSI 颜色码
	if width <= textLen {
		return text
	}
	padding := (width - textLen) / 2
	return strings.Repeat(" ", padding) + text
}

// RepeatChars 重复字符
func RepeatChars(char string, count int) string {
	return strings.Repeat(char, count)
}

// BoxText 给文本加框
func BoxText(text string, width int, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}

	// 分行处理文本
	lines := strings.Split(text, "\n")

	var box strings.Builder
	box.WriteString(strings.Repeat(theme.BorderHorizontal, width) + "\n")
	for _, line := range lines {
		box.WriteString(theme.BorderVertical)
		box.WriteString(fmt.Sprintf(" %-*s ", width-2, line))
		box.WriteString(theme.BorderVertical + "\n")
	}
	box.WriteString(strings.Repeat(theme.BorderHorizontal, width))

	return box.String()
}

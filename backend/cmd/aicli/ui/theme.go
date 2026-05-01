package ui

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/fatih/color"
	"golang.org/x/term"
)

// ThemeType 主题类型
type ThemeType int

const (
	ThemeAuto  ThemeType = iota // 自动检测
	ThemeLight                  // 亮色模式
	ThemeDark                   // 暗色模式
)

// Theme 主题定义
type Theme struct {
	Type ThemeType
	Name string

	// 用户消息颜色
	UserColor *color.Color
	UserIcon  string

	// 助手消息颜色
	AssistantColor *color.Color
	AssistantIcon  string

	// 系统消息颜色
	SystemColor *color.Color
	SystemIcon  string

	// 命令执行颜色
	CommandColor *color.Color
	CommandIcon  string

	// 输出颜色
	OutputColor *color.Color

	// 次级内容颜色
	SecondaryColor *color.Color

	// 弱化辅助信息颜色
	MutedColor *color.Color

	// 元信息标签颜色
	MetaLabelColor *color.Color

	// 时间线/辅助提示颜色
	TimelineColor *color.Color

	// 工具相关颜色
	ToolColor *color.Color

	// 推理相关颜色
	ReasoningColor *color.Color

	// 审批/确认相关颜色
	ApprovalColor *color.Color

	// 错误颜色
	ErrorColor *color.Color
	ErrorIcon  string

	// 警告颜色
	WarningColor *color.Color
	WarningIcon  string

	// 成功颜色
	SuccessColor *color.Color
	SuccessIcon  string

	// 信息颜色
	InfoColor *color.Color
	InfoIcon  string

	// 分隔线颜色
	SeparatorColor *color.Color

	// 进度条颜色
	ProgressColor *color.Color

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
)

// GetTheme 获取主题（单例）
func GetTheme(themeType ThemeType) *Theme {
	themeMutex.Lock()
	defer themeMutex.Unlock()

	if currentTheme != nil && (themeType == ThemeAuto || currentTheme.Type == themeType) && currentTheme.Name == normalizeThemePresetName(currentThemeName) {
		return currentTheme
	}

	currentTheme = createTheme(themeType)
	return currentTheme
}

// createTheme 创建主题
func createTheme(themeType ThemeType) *Theme {
	// 自动检测终端是否支持颜色
	// color.NoColor 是一个 bool 变量，当不支持颜色时为 true
	useColor := !color.NoColor

	// 如果是自动模式，通过环境变量判断
	actualType := themeType
	if themeType == ThemeAuto {
		// 检查 NO_COLOR 环境变量
		if os.Getenv("NO_COLOR") != "" {
			useColor = false
		}
		// 检测终端是否为暗色模式（粗略判断）
		if os.Getenv("TERM_PROGRAM") == "vscode" || os.Getenv("TERM_PROGRAM") == "iTerm.app" {
			actualType = ThemeDark
		} else {
			// 默认使用暗色模式
			actualType = ThemeDark
		}
	}

	theme := &Theme{
		Type:             actualType,
		Name:             normalizeThemePresetName(currentThemeName),
		UserColor:        color.New(color.FgCyan, color.Bold),
		UserIcon:         "👤",
		AssistantColor:   color.New(color.FgGreen),
		AssistantIcon:    "🤖",
		SystemColor:      color.New(color.FgHiYellow),
		SystemIcon:       "ℹ️",
		CommandColor:     color.New(color.FgMagenta),
		CommandIcon:      "❯",
		OutputColor:      color.New(color.Reset),
		SecondaryColor:   color.New(color.FgWhite),
		MutedColor:       color.New(color.FgHiBlack),
		MetaLabelColor:   color.New(color.FgHiBlack),
		TimelineColor:    color.New(color.FgHiBlack),
		ToolColor:        color.New(color.FgCyan, color.Bold),
		ReasoningColor:   color.New(color.FgYellow),
		ApprovalColor:    color.New(color.FgMagenta, color.Bold),
		ErrorColor:       color.New(color.FgRed, color.Bold),
		ErrorIcon:        "❌",
		WarningColor:     color.New(color.FgYellow, color.Bold),
		WarningIcon:      "⚠️",
		SuccessColor:     color.New(color.FgGreen, color.Bold),
		SuccessIcon:      "✅",
		InfoColor:        color.New(color.FgBlue),
		InfoIcon:         "💡",
		SeparatorColor:   color.New(color.FgHiBlack),
		ProgressColor:    color.New(color.FgGreen),
		ShellIcon:        "💻",
		BorderHorizontal: "═",
		BorderVertical:   "║",
		Separator:        "─",
	}

	applyThemePreset(theme, theme.Name)

	// 如果不支持颜色，禁用所有颜色
	if !useColor {
		disableColors(theme)
	}

	return theme
}

// disableColors 禁用主题中所有颜色
func disableColors(theme *Theme) {
	theme.UserColor = color.New()
	theme.AssistantColor = color.New()
	theme.SystemColor = color.New()
	theme.CommandColor = color.New()
	theme.OutputColor = color.New()
	theme.SecondaryColor = color.New()
	theme.MutedColor = color.New()
	theme.MetaLabelColor = color.New()
	theme.TimelineColor = color.New()
	theme.ToolColor = color.New()
	theme.ReasoningColor = color.New()
	theme.ApprovalColor = color.New()
	theme.ErrorColor = color.New()
	theme.WarningColor = color.New()
	theme.SuccessColor = color.New()
	theme.InfoColor = color.New()
	theme.SeparatorColor = color.New()
	theme.ProgressColor = color.New()
}

// SetTheme 设置主题
func SetTheme(themeType ThemeType) {
	themeMutex.Lock()
	defer themeMutex.Unlock()
	currentTheme = createTheme(themeType)
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
	t.SeparatorColor.Println(strings.Repeat(t.Separator, width))
}

// PrintBorder 打印边框分隔线
func (t *Theme) PrintBorder(width int) {
	t.SeparatorColor.Println(strings.Repeat(t.BorderHorizontal, width))
}

// FormatUser 格式化用户消息
func (t *Theme) FormatUser(text string) string {
	return t.UserColor.Sprintf("%s %s", t.UserIcon, text)
}

// FormatAssistant 格式化助手消息
func (t *Theme) FormatAssistant(text string) string {
	return t.AssistantColor.Sprintf("%s %s", t.AssistantIcon, text)
}

// FormatSystem 格式化系统消息
func (t *Theme) FormatSystem(text string) string {
	return t.SystemColor.Sprintf("%s %s", t.SystemIcon, text)
}

// FormatError 格式化错误消息
func (t *Theme) FormatError(text string) string {
	return t.ErrorColor.Sprintf("%s %s", t.ErrorIcon, text)
}

// FormatWarning 格式化警告消息
func (t *Theme) FormatWarning(text string) string {
	return t.WarningColor.Sprintf("%s %s", t.WarningIcon, text)
}

// FormatSuccess 格式化成功消息
func (t *Theme) FormatSuccess(text string) string {
	return t.SuccessColor.Sprintf("%s %s", t.SuccessIcon, text)
}

// FormatInfo 格式化信息消息
func (t *Theme) FormatInfo(text string) string {
	return t.InfoColor.Sprintf("%s %s", t.InfoIcon, text)
}

// ColorizeUser 用户消息颜色化
func (t *Theme) ColorizeUser(text string) string {
	return t.UserColor.Sprint(text)
}

// ColorizeAssistant 助手消息颜色化
func (t *Theme) ColorizeAssistant(text string) string {
	return t.AssistantColor.Sprint(text)
}

// ColorizeSystem 系统消息颜色化
func (t *Theme) ColorizeSystem(text string) string {
	return t.SystemColor.Sprint(text)
}

// ColorizeError 错误消息颜色化
func (t *Theme) ColorizeError(text string) string {
	return t.ErrorColor.Sprint(text)
}

// ColorizeWarning 警告消息颜色化
func (t *Theme) ColorizeWarning(text string) string {
	return t.WarningColor.Sprint(text)
}

// ColorizeSuccess 成功消息颜色化
func (t *Theme) ColorizeSuccess(text string) string {
	return t.SuccessColor.Sprint(text)
}

// ColorizeInfo 信息消息颜色化
func (t *Theme) ColorizeInfo(text string) string {
	return t.InfoColor.Sprint(text)
}

// Dimmed 变暗文本
func (t *Theme) Dimmed(text string) string {
	if t == nil {
		return text
	}
	return t.MutedColor.Sprint(text)
}

// GetTerminalWidth 获取终端宽度（用于自适应布局）
func GetTerminalWidth() int {
	defaultWidth := 80
	if width, _, err := term.GetSize(int(os.Stdout.Fd())); err == nil && width > 0 {
		return width
	}
	return defaultWidth
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

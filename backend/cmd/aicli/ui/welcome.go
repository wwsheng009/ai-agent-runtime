package ui

import (
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

const welcomeLabelWidth = 12

// WelcomeConfig 欢迎界面配置
type WelcomeConfig struct {
	AppName     string
	Version     string
	Description string
	ShowVersion bool
	ShowHint    bool
	Style       string // "simple", "detailed", "ascii"
}

// NewWelcomeConfig 创建欢迎界面配置
func NewWelcomeConfig() *WelcomeConfig {
	return &WelcomeConfig{
		AppName:     "AI Gateway CLI",
		Version:     "v1.0.0",
		Description: "智能 AI 对话终端",
		ShowVersion: true,
		ShowHint:    true,
		Style:       "detailed",
	}
}

// PrintWelcome 打印欢迎界面
func PrintWelcome() {
	PrintWelcomeWithConfig(NewWelcomeConfig())
}

// PrintWelcomeWithConfig 使用自定义配置打印欢迎界面
func PrintWelcomeWithConfig(config *WelcomeConfig) {
	if config == nil {
		config = NewWelcomeConfig()
	}
	theme := GetTheme(ThemeAuto)
	doc := WelcomeDocument(config, GetTerminalWidth())
	_, _ = WriteTerminalText(os.Stdout, "\n"+renderDocumentWithProfile(doc, theme)+"\n")
}

// WelcomeDocument 构建欢迎页的结构化模型。
func WelcomeDocument(config *WelcomeConfig, width int) render.Document {
	if config == nil {
		config = NewWelcomeConfig()
	}
	theme := GetTheme(ThemeAuto)
	appName := strings.Join(strings.Fields(SanitizeTerminalText(config.AppName)), " ")
	version := strings.Join(strings.Fields(SanitizeTerminalText(config.Version)), " ")
	description := strings.Join(strings.Fields(SanitizeTerminalText(config.Description)), " ")
	var lines []render.Line
	add := func(spans ...render.Span) { lines = append(lines, render.Line{Spans: spans}) }
	blank := func() { lines = append(lines, render.Line{}) }
	hints := func() {
		add(semanticSpan(theme.InfoIcon+" 快捷操作:", style.RoleInfo, true))
		for _, hint := range []string{
			"输入 /help 查看命令帮助",
			"输入 ! 前缀执行 Shell 命令",
			"输入 /exit 或 Ctrl+C 退出",
		} {
			add(semanticSpan("  "+theme.InfoIcon+" ", style.RoleInfo, false), semanticSpan(hint, style.RoleTextSecondary, false))
		}
	}
	switch config.Style {
	case "simple":
		add(semanticSpan(appName, style.RoleSuccess, true))
	case "ascii":
		asciiArt := "   _____ __  __  _____ \n  / ____\\ \\/ / |  __ \\\n | |     \\  /  | |__) |\n | |     /  \\  |  ___/ \n | |____/ /\\ \\ | |     \n  \\_____/_/  \\_\\_|     "
		for _, artLine := range strings.Split(asciiArt, "\n") {
			add(semanticSpan(artLine, style.RoleSuccess, false))
		}
		add(semanticSpan(theme.SuccessIcon+" ", style.RoleSuccess, true), semanticSpan(appName, style.RoleSuccess, true))
		blank()
		if config.ShowVersion {
			add(semanticSpan(version, style.RoleTextMuted, false))
		}
		if description != "" {
			add(semanticSpan(description, style.RoleTextMuted, false))
		}
		blank()
		if config.ShowHint {
			hints()
		}
	default:
		nameWidth := render.Width(theme.SuccessIcon + " " + appName)
		if width > 0 && nameWidth > width {
			nameWidth = width
		}
		separator := strings.Repeat("=", nameWidth)
		add(semanticSpan(separator, style.RoleBorder, false))
		add(semanticSpan(theme.SuccessIcon+" ", style.RoleSuccess, true), semanticSpan(appName, style.RoleSuccess, true))
		add(semanticSpan(separator, style.RoleBorder, false))
		if config.ShowVersion {
			add(welcomeKeyValueSpans("Version:", version, style.RoleTextMuted)...)
		}
		if description != "" {
			add(welcomeKeyValueSpans("Description:", description, style.RoleTextSecondary)...)
		}
		blank()
		if config.ShowHint {
			hints()
		}
	}
	return render.Document{Blocks: []render.Block{{Kind: render.BlockCustom, Lines: lines}}}
}

func welcomeKeyValueSpans(label, value string, role style.Role) []render.Span {
	label = SanitizeTerminalText(label)
	pad := welcomeLabelWidth - render.Width(label)
	if pad < 0 {
		pad = 0
	}
	return []render.Span{
		semanticSpan(label+strings.Repeat(" ", pad), style.RoleMetaLabel, true),
		semanticSpan(" "+value, role, false),
	}
}

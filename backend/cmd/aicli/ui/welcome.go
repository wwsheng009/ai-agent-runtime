package ui

import (
	"fmt"
	"strings"
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
	theme := GetTheme(ThemeAuto)

	fmt.Println()

	switch config.Style {
	case "simple":
		printSimpleWelcome(config, theme)
	case "ascii":
		printASCIIWelcome(config, theme)
	default:
		printDetailedWelcome(config, theme)
	}

	fmt.Println()
}

// printSimpleWelcome 打印简单版欢迎界面
func printSimpleWelcome(config *WelcomeConfig, theme *Theme) {
	fmt.Printf("%s\n", theme.SuccessColor.Sprint(config.AppName))
}

// printDetailedWelcome 打印详细版欢迎界面
func printDetailedWelcome(config *WelcomeConfig, theme *Theme) {
	// 应用名称和图标
	nameWithIcon := fmt.Sprintf("%s %s", theme.SuccessIcon, theme.SuccessColor.Sprint(config.AppName))

	// 分隔线
	separator := strings.Repeat("=", len(nameWithIcon))

	fmt.Printf("%s\n", separator)
	fmt.Printf("%s\n", nameWithIcon)
	fmt.Printf("%s\n", separator)

	// 版本
	if config.ShowVersion {
		printWelcomeKeyValue("Version:", theme.Dimmed(config.Version))
	}

	// 描述
	if config.Description != "" {
		printWelcomeKeyValue("Description:", config.Description)
	}

	fmt.Println()

	// 提示
	if config.ShowHint {
		fmt.Println(theme.InfoColor.Sprint("快捷操作:"))
		printHint("输入 /help 查看命令帮助", theme)
		printHint("输入 ! 前缀执行 Shell 命令", theme)
		printHint("输入 /exit 或 Ctrl+C 退出", theme)
	}

	fmt.Println()
}

// printASCIIWelcome 打印 ASCII 艺术字版欢迎界面
func printASCIIWelcome(config *WelcomeConfig, theme *Theme) {
	asciiArt := `
   _____ __  __  _____ 
  / ____\ \/ / |  __ \
 | |     \  /  | |__) |
 | |     /  \  |  ___/ 
 | |____/ /\ \ | |     
  \_____/_/  \_\_|     
`

	fmt.Print(theme.SuccessColor.Sprint(asciiArt))
	fmt.Printf("%s %s\n\n", theme.SuccessIcon, theme.SuccessColor.Sprint(config.AppName))

	if config.ShowVersion {
		fmt.Printf("%s\n", theme.Dimmed(config.Version))
	}

	if config.Description != "" {
		fmt.Printf("%s\n", theme.Dimmed(config.Description))
	}

	fmt.Println()

	if config.ShowHint {
		fmt.Println(theme.InfoColor.Sprintf("%s 快捷操作:", theme.InfoIcon))
		printHint("输入 /help 查看命令帮助", theme)
		printHint("输入 ! 前缀执行 Shell 命令", theme)
		printHint("输入 /exit 或 Ctrl+C 退出", theme)
	}

	fmt.Println()
}

// printHint打印提示项
func printHint(text string, theme *Theme) {
	prefix := fmt.Sprintf("  %s ", theme.InfoIcon)
	fmt.Printf("%s%s\n", prefix, text)
}

func printWelcomeKeyValue(label, value string) {
	fmt.Printf("%-*s %s\n", welcomeLabelWidth, label, value)
}

// PrintHelp 打印帮助信息
func PrintHelp() {
	theme := GetTheme(ThemeAuto)

	PrintSection("命令帮助")

	helpItems := [][]string{
		{"/help", "显示此帮助信息"},
		{"/clear", "清空会话历史"},
		{"/exit, /quit", "退出程序"},
		{"/provider [name]", "选择或查看提供商"},
		{"/model [name]", "选择或查看模型"},
		{"/stream [on|off]", "切换流式输出"},
		{"/token", "显示 token 使用情况"},
		{"/history", "显示消息历史"},
		{"/save", "保存当前会话"},
		{"![command]", "执行 Shell 命令"},
		{"/config", "显示当前配置"},
	}

	maxCmdLen := 0
	for _, item := range helpItems {
		if len(item[0]) > maxCmdLen {
			maxCmdLen = len(item[0])
		}
	}

	for _, item := range helpItems {
		cmd := theme.CommandColor.Sprint(item[0])
		desc := theme.Dimmed(item[1])
		fmt.Printf("  %-*s  %s\n", maxCmdLen, cmd, desc)
	}

	fmt.Println()
	PrintInfo("提示: 消息中 / 前缀表示系统命令，! 前缀表示 Shell 命令")
	fmt.Println()
}

// PrintGoodbye 打印道别信息
func PrintGoodbye() {
	theme := GetTheme(ThemeAuto)

	PrintEmptyLine()
	PrintSuccess("感谢使用 AI Gateway CLI！")
	fmt.Println(theme.InfoIcon, "会话已保存，日志已记录")
	PrintEmptyLine()
}

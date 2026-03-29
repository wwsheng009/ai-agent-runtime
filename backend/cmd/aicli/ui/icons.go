package ui

import "fmt"

// 图标名称常量
const (
	IconUser       = "user"
	IconAssistant  = "assistant"
	IconSystem     = "system"
	IconCommand    = "command"
	IconError      = "error"
	IconWarning    = "warning"
	IconSuccess    = "success"
	IconInfo       = "info"
	IconProgress   = "progress"
	IconSpinner    = "spinner"
	IconDatabase   = "database"
	IconFile       = "file"
	IconFolder     = "folder"
	IconNetwork    = "network"
	IconSettings   = "settings"
	IconTool       = "tool"
	IconShell      = "shell"
	IconCopy       = "copy"
	IconSave       = "save"
	IconClock      = "clock"
	IconToken      = "token"
)

// IconSet 图标集合
type IconSet struct {
	User      string
	Assistant string
	System    string
	Command   string
	Error     string
	Warning   string
	Success   string
	Info      string
	Spinner   []string
	Database  string
	File      string
	Folder    string
	Network   string
	Settings  string
	Tool      string
	Shell     string
	Copy      string
	Save      string
	Clock     string
	Token     string
}

var (
	// 默认图标集合（Unicode 图标）
	defaultIcons = &IconSet{
		User:      "👤",
		Assistant: "🤖",
		System:    "ℹ️",
		Command:   "❯",
		Error:     "❌",
		Warning:   "⚠️",
		Success:   "✅",
		Info:      "💡",
		Spinner:   []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
		Database:  "🗄️",
		File:      "📄",
		Folder:    "📁",
		Network:   "🌐",
		Settings:  "⚙️",
		Tool:      "🔧",
		Shell:     "💻",
		Copy:      "📋",
		Save:      "💾",
		Clock:     "⏱️",
		Token:     "📊",
	}

	// ASCII 图标集合（用于不支持 Unicode 的终端）
	asciiIcons = &IconSet{
		User:      "[U]",
		Assistant: "[A]",
		System:    "[S]",
		Command:   ">",
		Error:     "[!]",
		Warning:   "[W]",
		Success:   "[√]",
		Info:      "[i]",
		Spinner:   []string{"/", "-", "\\", "|"},
		Database:  "[DB]",
		File:      "[F]",
		Folder:    "[D]",
		Network:   "[N]",
		Settings:  "[S]",
		Tool:      "[T]",
		Shell:     "[$]",
		Copy:      "[C]",
		Save:      "[S]",
		Clock:     "[T]",
		Token:     "[+]",
	}

	// Simple 图标集合（最小化版本）
	simpleIcons = &IconSet{
		User:      "●",
		Assistant: "○",
		System:    "•",
		Command:   "»",
		Error:     "✘",
		Warning:   "▲",
		Success:   "✔",
		Info:      "›",
		Spinner:   []string{".", "o", "O", "0"},
		Database:  "::",
		File:      "~",
		Folder:    "[",
		Network:   "@",
		Settings:  "=",
		Tool:      "+",
		Shell:     "$",
		Copy:      ":",
		Save:      "v",
		Clock:     "t",
		Token:     "#",
	}
)

// IconStyle 图标样式
type IconStyle int

const (
	IconStyleUnicode IconStyle = iota // Unicode 图标（默认）
	IconStyleASCII                    // ASCII 图标
	IconStyleSimple                   // Simple 图标
)

var (
	currentIconSet *IconSet
	iconStyle      IconStyle = IconStyleUnicode
)

// GetIconSet 获取当前的图标集合
func GetIconSet() *IconSet {
	if currentIconSet == nil {
		SetIconStyle(IconStyleUnicode)
	}
	return currentIconSet
}

// SetIconStyle 设置图标样式
func SetIconStyle(style IconStyle) {
	iconStyle = style
	switch style {
	case IconStyleASCII:
		currentIconSet = asciiIcons
	case IconStyleSimple:
		currentIconSet = simpleIcons
	default:
		currentIconSet = defaultIcons
	}
}

// GetIcon 获取指定名称的图标
func GetIcon(name string) string {
	icons := GetIconSet()
	switch name {
	case IconUser:
		return icons.User
	case IconAssistant:
		return icons.Assistant
	case IconSystem:
		return icons.System
	case IconCommand:
		return icons.Command
	case IconError:
		return icons.Error
	case IconWarning:
		return icons.Warning
	case IconSuccess:
		return icons.Success
	case IconInfo:
		return icons.Info
	case IconDatabase:
		return icons.Database
	case IconFile:
		return icons.File
	case IconFolder:
		return icons.Folder
	case IconNetwork:
		return icons.Network
	case IconSettings:
		return icons.Settings
	case IconTool:
		return icons.Tool
	case IconShell:
		return icons.Shell
	case IconCopy:
		return icons.Copy
	case IconSave:
		return icons.Save
	case IconClock:
		return icons.Clock
	case IconToken:
		return icons.Token
	default:
		return "?"
	}
}

// GetSpinner 获取 spinner 图标
func GetSpinner(frame int) string {
	icons := GetIconSet()
	if frame < 0 || frame >= len(icons.Spinner) {
		frame = 0
	}
	return icons.Spinner[frame]
}

// GetSpinnerFrameCount 获取 spinner 图标总帧数
func GetSpinnerFrameCount() int {
	icons := GetIconSet()
	return len(icons.Spinner)
}

// FormatWithIcon 使用指定的图标格式化文本
func FormatWithIcon(iconName, text string) string {
	return fmt.Sprintf("%s %s", GetIcon(iconName), text)
}

// FormatUser 使用用户图标格式化文本
func FormatUser(text string) string {
	return FormatWithIcon(IconUser, text)
}

// FormatAssistant 使用助手图标格式化文本
func FormatAssistant(text string) string {
	return FormatWithIcon(IconAssistant, text)
}

// FormatSystem 使用系统图标格式化文本
func FormatSystem(text string) string {
	return FormatWithIcon(IconSystem, text)
}

// FormatCommand 使用命令图标格式化文本
func FormatCommand(text string) string {
	return FormatWithIcon(IconCommand, text)
}

// FormatError 使用错误图标格式化文本
func FormatError(text string) string {
	return FormatWithIcon(IconError, text)
}

// FormatWarning 使用警告图标格式化文本
func FormatWarning(text string) string {
	return FormatWithIcon(IconWarning, text)
}

// FormatSuccess 使用成功图标格式化文本
func FormatSuccess(text string) string {
	return FormatWithIcon(IconSuccess, text)
}

// FormatInfo 使用信息图标格式化文本
func FormatInfo(text string) string {
	return FormatWithIcon(IconInfo, text)
}

// GetIconStyleInfo 返回图标样式信息
func GetIconStyleInfo() string {
	return fmt.Sprintf("Icon Style: %v", iconStyle)
}

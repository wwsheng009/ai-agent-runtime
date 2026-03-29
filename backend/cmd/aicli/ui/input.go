package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// InputType 输入类型
type InputType int

const (
	InputDefault InputType = iota // 默认输入
	InputCommand                  // 命令输入
	InputPassword                 // 密码输入（暂不实现）
)

// Input 组件
type Input struct {
	theme       *Theme
	inputType   InputType
	prefix      string
	placeholder string
	readOnly    bool
}

// NewInput 创建新的输入组件
func NewInput(inputType InputType) *Input {
	return &Input{
		theme:     GetTheme(ThemeAuto),
		inputType: inputType,
		prefix:    "你> ",
	}
}

// SetTheme 设置主题
func (i *Input) SetTheme(theme *Theme) *Input {
	i.theme = theme
	return i
}

// SetPrefix 设置前缀
func (i *Input) SetPrefix(prefix string) *Input {
	i.prefix = prefix
	return i
}

// SetPlaceholder 设置占位符
func (i *Input) SetPlaceholder(placeholder string) *Input {
	i.placeholder = placeholder
	return i
}

// SetReadOnly 设置只读模式
func (i *Input) SetReadOnly(readOnly bool) *Input {
	i.readOnly = readOnly
	return i
}

// Show 显示输入提示符
func (i *Input) Show() {
	if i.placeholder != "" && i.inputType == InputDefault {
		fmt.Print(i.theme.Dimmed(fmt.Sprintf("(%s) ", i.placeholder)))
	}

	// 根据输入类型显示不同提示符
	switch i.inputType {
	case InputCommand:
		fmt.Printf("%s ", i.theme.CommandColor.Sprint(i.theme.CommandIcon))
	default:
		fmt.Printf("%s ", i.theme.UserColor.Sprint(i.userIconPrefix()))
	}
}

// userIconPrefix 用户图标前缀
func (i *Input) userIconPrefix() string {
	return fmt.Sprintf("%s你>", i.theme.UserIcon)
}

// Read 读取用户输入
func (i *Input) Read() (string, error) {
	i.Show()

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(input), nil
}

// ReadLine 读取一行输入（快捷方法）
func ReadLine() (string, error) {
	return NewInput(InputDefault).Read()
}

// Prompt 提示用户输入（带自定义消息）
func Prompt(prompt string) (string, error) {
	i := NewInput(InputDefault)
	i.SetPrefix(prompt)
	return i.Read()
}

// PromptUser 用户输入提示符
func PromptUser() string {
	i := NewInput(InputDefault)
	i.SetPrefix(fmt.Sprintf("%s你> ", GetTheme(ThemeAuto).UserColor.Sprint(GetTheme(ThemeAuto).UserIcon)))

	input, err := i.Read()
	if err != nil {
		return ""
	}
	return input
}

// PromptAssistant 助手输入辅助（用于测试或特殊场景）
func PromptAssistant(message string) {
	theme := GetTheme(ThemeAuto)
	fmt.Printf("%s ", theme.AssistantColor.Sprint(fmt.Sprintf("%s助手>", theme.AssistantIcon)))
	fmt.Println(message)
}

// FormatUserPrompt 格式化用户输入提示
func FormatUserPrompt() string {
	theme := GetTheme(ThemeAuto)
	return theme.UserColor.Sprintf("%s你> ", theme.UserIcon)
}

// FormatAssistantPrompt 格式化助手输出提示
func FormatAssistantPrompt() string {
	theme := GetTheme(ThemeAuto)
	return theme.AssistantColor.Sprintf("%s助手> ", theme.AssistantIcon)
}

// FormatCommandPrompt 格式化命令提示
func FormatCommandPrompt() string {
	theme := GetTheme(ThemeAuto)
	return theme.CommandColor.Sprintf("%s ", theme.CommandIcon)
}

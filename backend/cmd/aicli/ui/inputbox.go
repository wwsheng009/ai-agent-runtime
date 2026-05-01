package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// InputBox 输入框组件
type InputBox struct {
	layout     *Layout
	terminal   *Terminal
	theme      *Theme
	multiLine  bool
	maxLines   int
	history    []string
	historyPos int
}

// NewInputBox 创建新的输入框
func NewInputBox(layout *Layout) *InputBox {
	return &InputBox{
		layout:    layout,
		terminal:  NewTerminal(),
		theme:     GetTheme(ThemeAuto),
		multiLine: false,
		maxLines:  1,
		history:   make([]string, 0),
	}
}

// SetLayout 设置布局
func (ib *InputBox) SetLayout(layout *Layout) *InputBox {
	ib.layout = layout
	return ib
}

// SetTerminal 设置终端控制器
func (ib *InputBox) SetTerminal(term *Terminal) *InputBox {
	ib.terminal = term
	return ib
}

// SetTheme 设置主题
func (ib *InputBox) SetTheme(theme *Theme) *InputBox {
	ib.theme = theme
	return ib
}

// SetMultiLine 设置是否支持多行输入
func (ib *InputBox) SetMultiLine(multiLine bool) *InputBox {
	ib.multiLine = multiLine
	return ib
}

// SetMaxLines 设置最大行数
func (ib *InputBox) SetMaxLines(maxLines int) *InputBox {
	ib.maxLines = maxLines
	return ib
}

// Show 显示输入提示符
func (ib *InputBox) Show() {
	if ib.layout != nil && ib.layout.IsEnabled() {
		// 使用布局渲染
		ib.layout.RenderInputArea(ib.GetPrompt(), "")
	} else {
		// 直接打印
		fmt.Print(ib.theme.UserColor.Sprint(ib.GetPrompt()))
	}
}

// Read 读取用户输入
func (ib *InputBox) Read() (string, error) {
	ib.Show()

	reader := bufio.NewReader(os.Stdin)
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	input = strings.TrimSpace(input)

	// 添加到历史记录
	if input != "" {
		ib.history = append(ib.history, input)
		ib.historyPos = len(ib.history)
	}

	return input, nil
}

// ReadMultiLine 读取多行输入
func (ib *InputBox) ReadMultiLine() (string, error) {
	var builder strings.Builder
	lineCount := 0

	ib.Show()

	reader := bufio.NewReader(os.Stdin)

	for {
		prompt := ib.GetPrompt()
		if lineCount > 0 {
			prompt = "   ...> "
		}

		if ib.layout != nil && ib.layout.IsEnabled() {
			ib.layout.RenderInputArea(fmt.Sprintf("%s", prompt), "")
		} else {
			fmt.Print(ib.theme.UserColor.Sprintf("%s", prompt))
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			return "", err
		}

		// 空行表示结束
		if strings.TrimSpace(line) == "" {
			break
		}

		if lineCount > 0 {
			builder.WriteRune('\n')
		}

		builder.WriteString(strings.TrimSpace(line))
		lineCount++

		// 检查最大行数限制
		if ib.maxLines > 0 && lineCount >= ib.maxLines {
			break
		}
	}

	result := builder.String()

	// 添加到历史记录
	if result != "" {
		ib.history = append(ib.history, result)
		ib.historyPos = len(ib.history)
	}

	return result, nil
}

// ReadWithHistory 读取输入（支持历史记录导航）
func (ib *InputBox) ReadWithHistory() (string, error) {
	ib.Show()
	return ib.ReadWithHistoryPrompt(ib.GetPrompt(), nil)
}

// AddToHistory 添加到历史记录
func (ib *InputBox) AddToHistory(input string) {
	if input != "" {
		ib.history = append(ib.history, input)
		ib.historyPos = len(ib.history)
	}
}

// ClearHistory 清空历史记录
func (ib *InputBox) ClearHistory() {
	ib.history = make([]string, 0)
	ib.historyPos = 0
}

// GetHistory 获取历史记录
func (ib *InputBox) GetHistory() []string {
	return ib.history
}

// GetHistorySize 获取历史记录数量
func (ib *InputBox) GetHistorySize() int {
	return len(ib.history)
}

// GetHistoryAt 获取指定索引的历史记录
func (ib *InputBox) GetHistoryAt(index int) (string, bool) {
	if index < 0 || index >= len(ib.history) {
		return "", false
	}
	return ib.history[index], true
}

// PreviousHistory 获取上一条历史记录
func (ib *InputBox) PreviousHistory() (string, bool) {
	if ib.historyPos > 0 {
		ib.historyPos--
		return ib.history[ib.historyPos], true
	}
	return "", false
}

// NextHistory 获取下一条历史记录
func (ib *InputBox) NextHistory() (string, bool) {
	if ib.historyPos < len(ib.history)-1 {
		ib.historyPos++
		return ib.history[ib.historyPos], true
	}
	return "", false
}

// Clear 清除输入框
func (ib *InputBox) Clear() {
	if ib.layout != nil && ib.layout.IsEnabled() {
		ib.layout.RenderInputArea("", "")
	}
}

// Update 更新输入显示
func (ib *InputBox) Update(input string) {
	if ib.layout != nil && ib.layout.IsEnabled() {
		ib.layout.RenderInputArea(ib.GetPrompt(), input)
	}
}

// GetPrompt 获取提示符字符串
func (ib *InputBox) GetPrompt() string {
	return UserPromptText(0)
}

// Validate 验证输入
func (ib *InputBox) Validate(input string) bool {
	// 基础验证：非空
	return strings.TrimSpace(input) != ""
}

// Sanitize 清理输入
func (ib *InputBox) Sanitize(input string) string {
	// 清理特殊字符
	input = strings.TrimSpace(input)

	// 可以添加更多的清理逻辑
	// 例如：移除控制字符、标准化换行符等

	return input
}

// Complete 完成输入（例如自动补全）
func (ib *InputBox) Complete(input string, pos int) (string, []string) {
	// 简化版本，实际应该实现自动补全逻辑
	// 返回：
	//   - 补全后的输入
	//   - 候选列表（用于显示补全建议）
	return input, []string{}
}

// FormatPrompt 格式化带上下文的提示符
func (ib *InputBox) FormatPrompt(context string) string {
	prompt := ib.theme.UserColor.Sprint(ib.GetPrompt())
	if context != "" {
		prompt = fmt.Sprintf("%s ", ib.theme.Dimmed(context))
	}
	return prompt
}

// Cursor 移动光标到输入位置
func (ib *InputBox) Cursor(pos int) {
	if ib.layout != nil && ib.layout.IsEnabled() {
		// 计算实际位置
		promptLen := terminalVisibleWidth(ib.GetPrompt())
		actualPos := promptLen + pos + 1 // +1 因为列是 1-based

		ib.terminal.SaveCursor()
		ib.terminal.MoveTo(ib.layout.InputArea().Row, actualPos)
		ib.terminal.RestoreCursor()
	}
}

// Hide 隐藏输入框
func (ib *InputBox) Hide() {
	if ib.layout != nil && ib.layout.IsEnabled() {
		ib.layout.RenderInputArea("", "")
	}
}

// GetTerminal 获取终端控制器
func (ib *InputBox) GetTerminal() *Terminal {
	return ib.terminal
}

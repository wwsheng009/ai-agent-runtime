package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// MessageType 消息类型
type MessageType int

const (
	MessageUser      MessageType = iota // 用户消息
	MessageAssistant                    // 助手消息
	MessageSystem                       // 系统消息
	MessageTool                         // 工具消息
	MessageError                        // 错误消息
)

// Message 消息组件
type Message struct {
	theme         *Theme
	mType         MessageType
	content       string
	timestamp     time.Time
	showTimestamp bool
	showIcon      bool
}

// NewMessage 创建新消息
func NewMessage(mType MessageType, content string) *Message {
	return &Message{
		theme:         GetTheme(ThemeAuto),
		mType:         mType,
		content:       content,
		timestamp:     time.Now(),
		showTimestamp: false,
		showIcon:      true,
	}
}

// SetTheme 设置主题
func (m *Message) SetTheme(theme *Theme) *Message {
	m.theme = theme
	return m
}

// SetTimestamp 设置时间戳
func (m *Message) SetTimestamp(t time.Time) *Message {
	m.timestamp = t
	return m
}

// ShowTimestamp 设置是否显示时间戳
func (m *Message) ShowTimestamp(show bool) *Message {
	m.showTimestamp = show
	return m
}

// ShowIcon 设置是否显示图标
func (m *Message) ShowIcon(show bool) *Message {
	m.showIcon = show
	return m
}

// Format 格式化消息
func (m *Message) Format() string {
	var prefix string
	var plainPrefix string
	var coloredContent string
	contentPadding := ""

	switch m.mType {
	case MessageUser:
		if m.showIcon {
			plainPrefix = m.theme.UserIcon + " "
			prefix = m.theme.FormatUser("")
		} else {
			plainPrefix = "你> "
			prefix = "你> "
		}
		coloredContent = m.theme.ColorizeUser(m.content)

	case MessageAssistant:
		if m.showIcon {
			plainPrefix = m.theme.AssistantIcon + " "
			prefix = m.theme.FormatAssistant("")
		} else {
			plainPrefix = "助手> "
			prefix = "助手> "
		}
		coloredContent = m.theme.ColorizeAssistant(m.content)

	case MessageSystem:
		if m.showIcon {
			plainPrefix = m.theme.SystemIcon + " "
			prefix = m.theme.FormatSystem("")
		} else {
			plainPrefix = "系统> "
			prefix = "系统> "
		}
		coloredContent = m.theme.ColorizeSystem(m.content)

	case MessageTool:
		if m.showIcon {
			plainPrefix = fmt.Sprintf("%s工具> ", GetIcon(IconTool))
			prefix = fmt.Sprintf("%s工具> ", GetIcon(IconTool))
		} else {
			plainPrefix = "工具> "
			prefix = "工具> "
		}
		coloredContent = m.content

	case MessageError:
		if m.showIcon {
			plainPrefix = m.theme.ErrorIcon + " "
			prefix = m.theme.FormatError("")
		} else {
			plainPrefix = "错误> "
			prefix = "错误> "
		}
		coloredContent = m.theme.ColorizeError(m.content)

	default:
		plainPrefix = "> "
		prefix = "> "
		coloredContent = m.content
	}

	result := coloredContent
	if m.showIcon {
		contentPadding = " "
	}

	// 添加时间戳
	if m.showTimestamp {
		timeStr := m.timestamp.Format("15:04:05")
		result = fmt.Sprintf("[%s] %s", m.theme.Dimmed(timeStr), result)
	}

	// 如果内容很长，添加前缀换行
	if strings.Contains(m.content, "\n") {
		lines := strings.Split(result, "\n")
		if len(lines) > 0 {
			lines[0] = prefix + contentPadding + lines[0]
		}
		continuationIndent := strings.Repeat(" ", messageDisplayWidth(plainPrefix+contentPadding))
		for i := 1; i < len(lines); i++ {
			lines[i] = continuationIndent + lines[i]
		}
		result = strings.Join(lines, "\n")
	} else {
		result = prefix + contentPadding + result
	}

	return result
}

// Print 打印消息
func (m *Message) Print() {
	fmt.Println(m.Format())
}

// Printf 格式化并打印消息
func (m *Message) Printf(format string, args ...interface{}) {
	m.content = fmt.Sprintf(format, args...)
	m.Print()
}

// 快捷函数

// DisplayUserMessage 显示用户消息
func DisplayUserMessage(content string) {
	NewMessage(MessageUser, content).Print()
}

// DisplayAssistantMessage 显示助手消息
func DisplayAssistantMessage(content string) {
	NewMessage(MessageAssistant, content).Print()
}

// DisplaySystemMessage 显示系统消息
func DisplaySystemMessage(content string) {
	NewMessage(MessageSystem, content).Print()
}

// DisplayToolMessage 显示工具消息
func DisplayToolMessage(content string) {
	NewMessage(MessageTool, content).Print()
}

// DisplayErrorMessage 显示错误消息
func DisplayErrorMessage(content string) {
	NewMessage(MessageError, content).Print()
}

// FormatUserMessage 格式化用户消息
func FormatUserMessage(content string) string {
	return NewMessage(MessageUser, content).Format()
}

// FormatAssistantMessage 格式化助手消息
func FormatAssistantMessage(content string) string {
	return NewMessage(MessageAssistant, content).Format()
}

// FormatSystemMessage 格式化系统消息
func FormatSystemMessage(content string) string {
	return NewMessage(MessageSystem, content).Format()
}

// FormatToolMessage 格式化工具消息
func FormatToolMessage(content string) string {
	return NewMessage(MessageTool, content).Format()
}

// FormatErrorMessage 格式化错误消息
func FormatErrorMessage(content string) string {
	return NewMessage(MessageError, content).Format()
}

func AssistantContentIndent() string {
	theme := GetTheme(ThemeAuto)
	return strings.Repeat(" ", messageDisplayWidth(theme.AssistantIcon+"  "))
}

func IndentAssistantContent(content string) string {
	indent := AssistantContentIndent()
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		lines[i] = indent + line
	}
	return strings.Join(lines, "\n")
}

func DisplayWidth(text string) int {
	return messageDisplayWidth(text)
}

func messageDisplayWidth(text string) int {
	width := 0
	for _, r := range text {
		width += messageRuneWidth(r)
	}
	return width
}

func messageRuneWidth(r rune) int {
	if r == 0 {
		return 0
	}
	if r < 32 || r == 127 {
		return 0
	}
	if messageIsWideRune(r) {
		return 2
	}
	return 1
}

func messageIsWideRune(r rune) bool {
	if unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul) {
		return true
	}
	if r >= 0x1100 && r <= 0x115F {
		return true
	}
	if r >= 0x2E80 && r <= 0xA4CF {
		return true
	}
	if r >= 0xAC00 && r <= 0xD7A3 {
		return true
	}
	if r >= 0xF900 && r <= 0xFAFF {
		return true
	}
	if r >= 0xFE10 && r <= 0xFE6F {
		return true
	}
	if r >= 0xFF00 && r <= 0xFF60 {
		return true
	}
	if r >= 0xFFE0 && r <= 0xFFE6 {
		return true
	}
	if r >= 0x1F300 && r <= 0x1FAFF {
		return true
	}
	return false
}

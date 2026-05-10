package ui

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	leftToRightIsolate    = '\u2066'
	popDirectionalIsolate = '\u2069'
	arabicLetterMark      = '\u061C'
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
	safeContent := SanitizeTerminalText(m.content)

	switch m.mType {
	case MessageUser:
		if m.showIcon {
			plainPrefix = m.theme.UserIcon + " "
			prefix = m.theme.FormatUser("")
		} else {
			plainPrefix = "> "
			prefix = "> "
		}
		coloredContent = m.theme.ColorizeUser(safeContent)

	case MessageAssistant:
		if m.showIcon {
			plainPrefix = m.theme.AssistantIcon + " "
			prefix = m.theme.FormatAssistant("")
		} else {
			plainPrefix = "助手> "
			prefix = "助手> "
		}
		coloredContent = m.theme.ColorizeAssistant(safeContent)

	case MessageSystem:
		if m.showIcon {
			plainPrefix = m.theme.SystemIcon + " "
			prefix = m.theme.FormatSystem("")
		} else {
			plainPrefix = "系统> "
			prefix = "系统> "
		}
		coloredContent = m.theme.ColorizeSystem(safeContent)

	case MessageTool:
		if m.showIcon {
			plainPrefix = fmt.Sprintf("%s工具> ", GetIcon(IconTool))
			prefix = fmt.Sprintf("%s工具> ", GetIcon(IconTool))
		} else {
			plainPrefix = "工具> "
			prefix = "工具> "
		}
		coloredContent = safeContent

	case MessageError:
		if m.showIcon {
			plainPrefix = m.theme.ErrorIcon + " "
			prefix = m.theme.FormatError("")
		} else {
			plainPrefix = "错误> "
			prefix = "错误> "
		}
		coloredContent = m.theme.ColorizeError(safeContent)

	default:
		plainPrefix = "> "
		prefix = "> "
		coloredContent = safeContent
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
	if strings.Contains(safeContent, "\n") {
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

// SanitizeTerminalText removes terminal control sequences and unsafe bidi
// formatting controls, then isolates strong RTL runs so mixed-direction content
// does not reorder adjacent CJK/LTR text in terminal renderers.
func SanitizeTerminalText(text string) string {
	if text == "" {
		return ""
	}
	text = stripUnsafeTerminalControls(text)
	if text == "" {
		return ""
	}
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case isUnsafeBidiControlRune(r):
			return -1
		default:
			return r
		}
	}, text)
	if cleaned == "" {
		return ""
	}

	var builder strings.Builder
	builder.Grow(len(cleaned) + 8)
	inRTLRun := false
	for _, r := range cleaned {
		if isStrongRTL(r) {
			if !inRTLRun {
				builder.WriteRune(leftToRightIsolate)
				inRTLRun = true
			}
			builder.WriteRune(r)
			continue
		}
		if inRTLRun {
			builder.WriteRune(popDirectionalIsolate)
			inRTLRun = false
		}
		builder.WriteRune(r)
	}
	if inRTLRun {
		builder.WriteRune(popDirectionalIsolate)
	}
	return builder.String()
}

func stripUnsafeTerminalControls(text string) string {
	if text == "" {
		return ""
	}
	var builder strings.Builder
	builder.Grow(len(text))
	for i := 0; i < len(text); {
		if text[i] == '\x1b' {
			consumed := consumeTerminalEscapeSequence(text[i:])
			if consumed <= 0 {
				consumed = 1
			}
			i += consumed
			continue
		}
		r, size := utf8.DecodeRuneInString(text[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		switch {
		case r == '\r':
			builder.WriteRune('\n')
			if i+size < len(text) && text[i+size] == '\n' {
				size++
			}
		case r == '\n':
			builder.WriteRune('\n')
		case r == '\t':
			builder.WriteString("    ")
		case r < 32 || r == 127:
			// Drop C0 controls such as BEL, backspace, form feed and raw ESC.
		case r >= 0x80 && r <= 0x9f:
			// Drop C1 controls, including 8-bit CSI/OSC variants.
		default:
			builder.WriteRune(r)
		}
		i += size
	}
	return builder.String()
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
	if unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) {
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

func isUnsafeBidiControlRune(r rune) bool {
	switch r {
	case '\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
		'\u2066', '\u2067', '\u2068', '\u2069',
		arabicLetterMark:
		return true
	default:
		return false
	}
}

func isStrongRTL(r rune) bool {
	if !utf8.ValidRune(r) {
		return false
	}
	return unicode.Is(unicode.Arabic, r) ||
		unicode.Is(unicode.Hebrew, r) ||
		unicode.Is(unicode.Syriac, r) ||
		unicode.Is(unicode.Thaana, r) ||
		unicode.Is(unicode.Nko, r) ||
		unicode.Is(unicode.Samaritan, r) ||
		unicode.Is(unicode.Mandaic, r) ||
		unicode.Is(unicode.Adlam, r)
}

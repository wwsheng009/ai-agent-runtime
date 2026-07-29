package ui

import (
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
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

// messageChrome returns plain prefix/padding and semantic roles for Document layout.
// Layout must stay byte-compatible with historical Format() tests (icon + padding widths).
func (m *Message) messageChrome() (plainPrefix, contentPadding, prefixRole, contentRole string, colorPrefix bool) {
	theme := m.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	switch m.mType {
	case MessageUser:
		plainPrefix = "> "
		contentRole = string(style.RoleUser)
		if m.showIcon {
			prefixRole = string(style.RoleUser)
			colorPrefix = true
		}
	case MessageAssistant:
		contentRole = string(style.RoleAssistant)
	case MessageSystem:
		contentRole = string(style.RoleSystem)
		if m.showIcon {
			plainPrefix = theme.SystemIcon + " "
			contentPadding = " "
			prefixRole = string(style.RoleSystem)
			colorPrefix = true
		} else {
			plainPrefix = "系统> "
		}
	case MessageTool:
		// Tool body stays uncolored (parity with legacy Format).
		if m.showIcon {
			plainPrefix = fmt.Sprintf("%s工具> ", GetIcon(IconTool))
			contentPadding = " "
		} else {
			plainPrefix = "工具> "
		}
	case MessageError:
		contentRole = string(style.RoleError)
		if m.showIcon {
			plainPrefix = theme.ErrorIcon + " "
			contentPadding = " "
			prefixRole = string(style.RoleError)
			colorPrefix = true
		} else {
			plainPrefix = "错误> "
		}
	default:
		plainPrefix = "> "
	}
	return plainPrefix, contentPadding, prefixRole, contentRole, colorPrefix
}

// Document builds a role-tagged message model (sanitized text, no pre-colored ANSI).
func (m *Message) Document() render.Document {
	safeContent := SanitizeTerminalText(m.content)
	plainPrefix, contentPadding, prefixRole, contentRole, colorPrefix := m.messageChrome()

	contentLines := strings.Split(safeContent, "\n")
	if len(contentLines) == 0 {
		contentLines = []string{""}
	}

	indent := ""
	if plainPrefix != "" || contentPadding != "" {
		indent = strings.Repeat(" ", messageDisplayWidth(plainPrefix+contentPadding))
	}

	lines := make([]render.Line, 0, len(contentLines))
	for i, contentLine := range contentLines {
		var spans []render.Span
		if i == 0 {
			// Order matches legacy Format: prefix + padding + optional [time] + body.
			if plainPrefix != "" {
				sp := render.Span{Text: plainPrefix}
				if colorPrefix && prefixRole != "" {
					sp.Style = render.Style{Role: prefixRole}
				}
				spans = append(spans, sp)
			}
			if contentPadding != "" {
				spans = append(spans, render.Span{Text: contentPadding})
			}
			if m.showTimestamp {
				timeStr := m.timestamp.Format("15:04:05")
				spans = append(spans,
					render.Span{Text: "["},
					render.Span{Text: timeStr, Style: render.Style{Role: string(style.RoleTextMuted)}},
					render.Span{Text: "] "},
				)
			}
		} else if indent != "" {
			spans = append(spans, render.Span{Text: indent})
		}
		sp := render.Span{Text: contentLine}
		if contentRole != "" {
			sp.Style = render.Style{Role: contentRole}
		}
		spans = append(spans, sp)
		lines = append(lines, render.Line{Spans: spans})
	}
	return render.LinesDoc(lines...)
}

// Format 格式化消息，并按当前终端能力解析语义颜色。
func (m *Message) Format() string {
	doc := m.Document()
	theme := m.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	return renderDocumentWithProfile(doc, theme)
}

// Print 打印消息
func (m *Message) Print() {
	text := m.Format()
	_, _ = WriteTerminalLine(os.Stdout, text)
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

// DisplayAssistantMessage prints an assistant message.
// Pre-rendered backend output (contains ESC) keeps SGR; raw text is theme-colored.
func DisplayAssistantMessage(content string) {
	if strings.ContainsRune(content, '\x1b') {
		_, _ = WriteTerminalLine(os.Stdout, FormatAssistantRendered(content))
		return
	}
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

// FormatAssistantMessage 格式化助手消息。
// 会对内容做控制序列清理，并套用 Assistant 主题色；不适合已经过
// markdown/syntax backend 着色的字符串（会丢掉 token 颜色）。
func FormatAssistantMessage(content string) string {
	return NewMessage(MessageAssistant, content).Format()
}

// FormatAssistantRendered 用于已经由 render backend 生成的助手输出。
// 保留安全的 SGR / OSC 8，剥离光标/清屏等危险序列，不再整段重染色。
func FormatAssistantRendered(content string) string {
	if content == "" {
		return ""
	}
	safe := render.SanitizeKeepSGR(content)
	return IndentAssistantContent(safe)
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
	return ""
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

package ui

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// InputType 输入类型
type InputType int

const (
	InputDefault  InputType = iota // 默认输入
	InputCommand                   // 命令输入
	InputPassword                  // 密码输入（暂不实现）
)

// Input 组件
type Input struct {
	theme       *Theme
	inputType   InputType
	prefix      string
	placeholder string
	readOnly    bool
}

const defaultUserPrompt = "> "

// UserPromptText returns the plain user prompt text.
func UserPromptText(attachmentCount int) string {
	if attachmentCount <= 0 {
		return defaultUserPrompt
	}
	return fmt.Sprintf("> [📎%d] ", attachmentCount)
}

// promptSpanDocument builds a single-span Document tagged with a semantic role.
func promptSpanDocument(text string, role style.Role) render.Document {
	if text == "" {
		return render.Document{}
	}
	return render.LinesDoc(render.Line{Spans: []render.Span{{
		Text:  text,
		Style: render.Style{Role: string(role)},
	}}})
}

// UserPromptDocument builds a role-tagged Document for the user prompt.
func UserPromptDocument(attachmentCount int) render.Document {
	return promptSpanDocument(UserPromptText(attachmentCount), style.RoleUser)
}

// PromptLineDocument builds a role-tagged Document for an arbitrary prompt string.
func PromptLineDocument(prompt string) render.Document {
	if strings.TrimSpace(prompt) == "" {
		prompt = defaultUserPrompt
	}
	return promptSpanDocument(prompt, style.RoleUser)
}

// CommandPromptDocument builds a role-tagged Document for the command prompt.
func CommandPromptDocument(icon string) render.Document {
	if strings.TrimSpace(icon) == "" {
		icon = GetTheme(ThemeAuto).CommandIcon
	}
	return promptSpanDocument(icon+" ", style.RoleCommand)
}

// InputPlaceholderDocument builds a muted placeholder prefix Document.
func InputPlaceholderDocument(placeholder string) render.Document {
	if placeholder == "" {
		return render.Document{}
	}
	return promptSpanDocument(fmt.Sprintf("(%s) ", placeholder), style.RoleTextMuted)
}

// AssistantMessageDocument builds a role-tagged Document for assistant helper text.
// Untrusted content is sanitized so control sequences cannot escape into the TTY.
func AssistantMessageDocument(message string) render.Document {
	return promptSpanDocument(SanitizeTerminalText(message), style.RoleAssistant)
}

// InputShowDocument composes the full prompt Document for an Input component.
func InputShowDocument(inputType InputType, prefix, placeholder, commandIcon string) render.Document {
	var spans []render.Span
	if placeholder != "" && inputType == InputDefault {
		spans = append(spans, render.Span{
			Text:  fmt.Sprintf("(%s) ", placeholder),
			Style: render.Style{Role: string(style.RoleTextMuted)},
		})
	}
	switch inputType {
	case InputCommand:
		icon := commandIcon
		if strings.TrimSpace(icon) == "" {
			icon = GetTheme(ThemeAuto).CommandIcon
		}
		spans = append(spans, render.Span{
			Text:  icon + " ",
			Style: render.Style{Role: string(style.RoleCommand)},
		})
	default:
		prompt := prefix
		if strings.TrimSpace(prompt) == "" {
			prompt = defaultUserPrompt
		}
		spans = append(spans, render.Span{
			Text:  prompt,
			Style: render.Style{Role: string(style.RoleUser)},
		})
	}
	if len(spans) == 0 {
		return render.Document{}
	}
	return render.LinesDoc(render.Line{Spans: spans})
}

func renderInputDocument(doc render.Document, theme *Theme) string {
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	return strings.TrimRight(renderDocumentWithProfile(doc, theme), "\n")
}

func writeInputDocument(doc render.Document, theme *Theme) {
	text := renderInputDocument(doc, theme)
	if text == "" {
		return
	}
	_, _ = WriteTerminalText(os.Stdout, text)
}

// NewInput 创建新的输入组件
func NewInput(inputType InputType) *Input {
	return &Input{
		theme:     GetTheme(ThemeAuto),
		inputType: inputType,
		prefix:    defaultUserPrompt,
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

// Document builds the structured prompt model for this Input.
func (i *Input) Document() render.Document {
	theme := i.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	return InputShowDocument(i.inputType, i.prefix, i.placeholder, theme.CommandIcon)
}

// Show 显示输入提示符（无尾随换行，走 Document + 终端写锁）
func (i *Input) Show() {
	theme := i.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	writeInputDocument(i.Document(), theme)
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
	input, err := NewInput(InputDefault).Read()
	if err != nil {
		return ""
	}
	return input
}

// PromptAssistant 助手输入辅助（用于测试或特殊场景）
func PromptAssistant(message string) {
	theme := GetTheme(ThemeAuto)
	text := renderInputDocument(AssistantMessageDocument(message), theme)
	if text == "" {
		return
	}
	_, _ = WriteTerminalLine(os.Stdout, text)
}

// FormatUserPrompt 格式化用户输入提示
func FormatUserPrompt() string {
	return FormatUserPromptWithAttachments(0)
}

// FormatUserPromptWithAttachments 格式化带附件数量的用户输入提示
func FormatUserPromptWithAttachments(attachmentCount int) string {
	return renderInputDocument(UserPromptDocument(attachmentCount), GetTheme(ThemeAuto))
}

// FormatAssistantPrompt 格式化助手输出提示
func FormatAssistantPrompt() string {
	return ""
}

// FormatCommandPrompt 格式化命令提示
func FormatCommandPrompt() string {
	theme := GetTheme(ThemeAuto)
	return renderInputDocument(CommandPromptDocument(theme.CommandIcon), theme)
}

// FormatPromptLine formats an arbitrary prompt string with the User role.
func FormatPromptLine(prompt string) string {
	return renderInputDocument(PromptLineDocument(prompt), GetTheme(ThemeAuto))
}

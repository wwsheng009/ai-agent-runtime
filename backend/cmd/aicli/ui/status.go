package ui

import (
	"fmt"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/style"
)

// StatusType 状态类型
type StatusType int

const (
	StatusSuccess StatusType = iota // 成功
	StatusError                     // 错误
	StatusWarning                   // 警告
	StatusInfo                      // 信息
	StatusDebug                     // 调试
)

// Status 状态显示组件
type Status struct {
	theme   *Theme
	msg     string
	sType   StatusType
	newline bool
	prefix  string
	suffix  string
}

// NewStatus 创建新的状态
func NewStatus(sType StatusType, message string) *Status {
	return &Status{
		theme:   GetTheme(ThemeAuto),
		msg:     message,
		sType:   sType,
		newline: true,
	}
}

// SetTheme 设置主题
func (s *Status) SetTheme(theme *Theme) *Status {
	s.theme = theme
	return s
}

// SetNewLine 设置是否换行
func (s *Status) SetNewLine(newline bool) *Status {
	s.newline = newline
	return s
}

// SetPrefix 设置前缀
func (s *Status) SetPrefix(prefix string) *Status {
	s.prefix = prefix
	return s
}

// SetSuffix 设置后缀
func (s *Status) SetSuffix(suffix string) *Status {
	s.suffix = suffix
	return s
}

// Document builds a role-tagged status line (message sanitized).
func (s *Status) Document() render.Document {
	theme := s.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	safeMsg := SanitizeTerminalText(s.msg)

	var body string
	var role string
	switch s.sType {
	case StatusSuccess:
		body = fmt.Sprintf("%s %s", theme.SuccessIcon, safeMsg)
		role = string(style.RoleSuccess)
	case StatusError:
		body = fmt.Sprintf("%s %s", theme.ErrorIcon, safeMsg)
		role = string(style.RoleError)
	case StatusWarning:
		body = fmt.Sprintf("%s %s", theme.WarningIcon, safeMsg)
		role = string(style.RoleWarning)
	case StatusInfo:
		body = fmt.Sprintf("%s %s", theme.InfoIcon, safeMsg)
		role = string(style.RoleInfo)
	case StatusDebug:
		body = fmt.Sprintf("[调试] %s", safeMsg)
	default:
		body = safeMsg
	}

	var spans []render.Span
	if s.prefix != "" {
		spans = append(spans, render.Span{Text: s.prefix})
	}
	sp := render.Span{Text: body}
	if role != "" {
		sp.Style = render.Style{Role: role}
	}
	spans = append(spans, sp)
	if s.suffix != "" {
		spans = append(spans, render.Span{Text: s.suffix})
	}
	return render.LinesDoc(render.Line{Spans: spans})
}

// Build 构建状态字符串，并按当前终端能力解析语义颜色。
func (s *Status) Build() string {
	doc := s.Document()
	theme := s.theme
	if theme == nil {
		theme = GetTheme(ThemeAuto)
	}
	return renderDocumentWithProfile(doc, theme)
}

// Print 打印状态
func (s *Status) Print() {
	text := s.Build()
	if s.newline {
		_, _ = WriteTerminalLine(os.Stdout, text)
		return
	}
	_, _ = WriteTerminalText(os.Stdout, text)
}

// Printf 格式化打印状态
func (s *Status) Printf(format string, args ...interface{}) {
	s.msg = fmt.Sprintf(format, args...)
	s.Print()
}

// PrintfError 格式化打印错误状态
func (s *Status) PrintfError(format string, args ...interface{}) {
	s.sType = StatusError
	s.msg = fmt.Sprintf(format, args...)
	s.Print()
}

// PrintfWarning 格式化打印警告状态
func (s *Status) PrintfWarning(format string, args ...interface{}) {
	s.sType = StatusWarning
	s.msg = fmt.Sprintf(format, args...)
	s.Print()
}

// PrintfSuccess 格式化打印成功状态
func (s *Status) PrintfSuccess(format string, args ...interface{}) {
	s.sType = StatusSuccess
	s.msg = fmt.Sprintf(format, args...)
	s.Print()
}

// PrintfInfo 格式化打印信息状态
func (s *Status) PrintfInfo(format string, args ...interface{}) {
	s.sType = StatusInfo
	s.msg = fmt.Sprintf(format, args...)
	s.Print()
}

// PrintTo 打印到指定输出流
func (s *Status) PrintTo(writer *os.File) {
	text := s.Build()
	if writer == nil {
		writer = os.Stdout
	}
	if s.newline {
		_, _ = WriteTerminalLine(writer, text)
		return
	}
	_, _ = WriteTerminalText(writer, text)
}

// 快捷函数

// PrintSuccess 打印成功消息
func PrintSuccess(format string, args ...interface{}) {
	NewStatus(StatusSuccess, fmt.Sprintf(format, args...)).Print()
}

// PrintError 打印错误消息
func PrintError(format string, args ...interface{}) {
	NewStatus(StatusError, fmt.Sprintf(format, args...)).Print()
}

// PrintWarning 打印警告消息
func PrintWarning(format string, args ...interface{}) {
	NewStatus(StatusWarning, fmt.Sprintf(format, args...)).Print()
}

// PrintInfo 打印信息消息
func PrintInfo(format string, args ...interface{}) {
	NewStatus(StatusInfo, fmt.Sprintf(format, args...)).Print()
}

// PrintDebug 打印调试消息
func PrintDebug(format string, args ...interface{}) {
	NewStatus(StatusDebug, fmt.Sprintf(format, args...)).Print()
}

// PrintSuccessTo 打印成功消息到指定输出流
func PrintSuccessTo(writer *os.File, format string, args ...interface{}) {
	NewStatus(StatusSuccess, fmt.Sprintf(format, args...)).PrintTo(writer)
}

// PrintErrorTo 打印错误消息到指定输出流
func PrintErrorTo(writer *os.File, format string, args ...interface{}) {
	NewStatus(StatusError, fmt.Sprintf(format, args...)).PrintTo(writer)
}

// PrintWarningTo 打印警告消息到指定输出流
func PrintWarningTo(writer *os.File, format string, args ...interface{}) {
	NewStatus(StatusWarning, fmt.Sprintf(format, args...)).PrintTo(writer)
}

// PrintInfoTo 打印信息消息到指定输出流
func PrintInfoTo(writer *os.File, format string, args ...interface{}) {
	NewStatus(StatusInfo, fmt.Sprintf(format, args...)).PrintTo(writer)
}

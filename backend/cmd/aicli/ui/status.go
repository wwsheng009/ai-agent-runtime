package ui

import (
	"fmt"
	"os"
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
	theme    *Theme
	msg      string
	sType    StatusType
	newline  bool
	prefix   string
	suffix   string
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

// Build 构建状态字符串
func (s *Status) Build() string {
	var formattedMsg string
	switch s.sType {
	case StatusSuccess:
		formattedMsg = s.theme.FormatSuccess(s.msg)
	case StatusError:
		formattedMsg = s.theme.FormatError(s.msg)
	case StatusWarning:
		formattedMsg = s.theme.FormatWarning(s.msg)
	case StatusInfo:
		formattedMsg = s.theme.FormatInfo(s.msg)
	case StatusDebug:
		formattedMsg = fmt.Sprintf("[调试] %s", s.msg)
	default:
		formattedMsg = s.msg
	}

	return s.prefix + formattedMsg + s.suffix
}

// Print 打印状态
func (s *Status) Print() {
	if s.newline {
		fmt.Println(s.Build())
	} else {
		fmt.Print(s.Build())
	}
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
	if s.newline {
		fmt.Fprintln(writer, s.Build())
	} else {
		fmt.Fprint(writer, s.Build())
	}
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

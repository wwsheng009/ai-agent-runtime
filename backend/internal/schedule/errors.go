package schedule

import (
	"errors"
	"fmt"
)

// Code 是面向模型的稳定错误码。
type Code string

const (
	// CodeInvalidPrompt 表示 prompt 或 title 非法。
	CodeInvalidPrompt Code = "invalid_prompt"
	// CodeInvalidSelector 表示选择器数量/形状非法。
	CodeInvalidSelector Code = "invalid_selector"
	// CodeInvalidRule 表示规则内容非法。
	CodeInvalidRule Code = "invalid_rule"
	// CodeInvalidTimeZone 表示时区无法解析。
	CodeInvalidTimeZone Code = "invalid_time_zone"
	// CodeNotFuture 表示目标不在未来。
	CodeNotFuture Code = "not_future"
	// CodeTimeOutOfRange 表示没有可表示的 UTC 目标。
	CodeTimeOutOfRange Code = "time_out_of_range"
	// CodeFrequencyTooHigh 表示固定速率间隔低于下限。
	CodeFrequencyTooHigh Code = "frequency_too_high"
	// CodeSubagentSession 表示被委派子 Agent 拥有的会话不能设置提醒。
	CodeSubagentSession Code = "subagent_session"
	// CodeNotFound 表示任务不存在或绑定不符。
	CodeNotFound Code = "schedule_not_found"
	// CodeEnded 表示任务已结束（inactive）。
	CodeEnded Code = "schedule_ended"
	// CodeConflict 表示 expected 与当前记录不一致（乐观并发）。
	CodeConflict Code = "schedule_conflict"
	// CodeCursorNotFound 表示投递历史分页游标未知。
	CodeCursorNotFound Code = "delivery_cursor_not_found"
	// CodeInternal 表示不应外泄存储细节的内部失败。
	CodeInternal Code = "internal_error"
)

// InputError 是调用方可修正的输入错误；Code 直接面向模型。
type InputError struct {
	Code    Code
	Message string
}

func (e *InputError) Error() string { return string(e.Code) + ": " + e.Message }

// LogError 表示持久数据非法（权威数据：一条非法记录拒绝打开整个库）。
type LogError struct {
	Message string
}

func (e *LogError) Error() string { return "schedule log: " + e.Message }

// IsInputError 报告 err 是否为 InputError 并返回它。
func IsInputError(err error) (*InputError, bool) {
	var e *InputError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsLogError 报告 err 是否为 LogError 并返回它。
func IsLogError(err error) (*LogError, bool) {
	var e *LogError
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

func inputError(code Code, format string, args ...any) *InputError {
	return &InputError{Code: code, Message: fmt.Sprintf(format, args...)}
}

func logError(format string, args ...any) *LogError {
	return &LogError{Message: fmt.Sprintf(format, args...)}
}

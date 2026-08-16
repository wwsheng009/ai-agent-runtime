package adapter

import (
	"fmt"
	"strings"
)

// MalformedToolCall 描述一次参数非法的工具调用（模型生成的 arguments 不是合法 JSON）。
// 携带调用原文，供上层执行层降级为工具反馈回注（re-prompt），而不是终止整个 turn。
type MalformedToolCall struct {
	// Index 是该调用在本次响应 tool_calls 中的序号。
	Index int
	// ID 是模型给出的调用 ID（可能为空）。
	ID string
	// Name 是工具名。
	Name string
	// Arguments 是模型生成的原始 arguments 文本（非法 JSON）。
	Arguments string
}

// MalformedToolCallError 表示模型生成了无法解析为 JSON 的工具调用参数。
//
// 与 openAIProtocolError / codexResponseError 不同，这不是传输或协议故障，而是
// 模型输出内容本身非法：流是完整读完的、finish_reason 也正常，只是参数文本
// 语法错误（例如 `{"timeout": 60s}`）。这种错误无法通过重试同一请求修复，
// 但可以在执行层降级：把「参数非法 + 工具 schema」作为工具执行反馈注入下一轮，
// 让模型按 schema 重新输出参数。
type MalformedToolCallError struct {
	// Kind 是来源 adapter 的错误前缀，保持与旧错误消息一致
	// （"openai_stream_protocol_error" / "codex response invalid"）。
	Kind string
	// Code 保持与旧错误 code 一致（"invalid_tool_arguments"），
	// 让 retry policy 的消息匹配与诊断分类不受影响。
	Code string
	// Message 是完整错误消息（保持与旧格式一致）。
	Message string
	// ToolCalls 是所有参数非法的调用（一次响应可能有多条）。
	ToolCalls []MalformedToolCall
}

// Error 实现 error 接口。消息格式与旧 openAIProtocolError/codexResponseError
// 完全一致，兼容现有测试、日志与 retry policy 的消息匹配。
func (e *MalformedToolCallError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("%s: code=%s: tool call arguments are not valid JSON", e.Kind, e.Code)
}

// RetryErrorCode 保持 invalid_tool_arguments 的 retry 分类标识（malformed_tool_call）；
// 注意该错误不可重试（重放无法修复非法 JSON），恢复通道是执行层降级 re-prompt。
func (e *MalformedToolCallError) RetryErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// newOpenAIMalformedToolCallError 构造 openai 协议风格的 MalformedToolCallError。
func newOpenAIMalformedToolCallError(calls []MalformedToolCall) *MalformedToolCallError {
	return newMalformedToolCallError("openai_stream_protocol_error", calls)
}

// newCodexMalformedToolCallError 构造 codex 协议风格的 MalformedToolCallError。
func newCodexMalformedToolCallError(calls []MalformedToolCall) *MalformedToolCallError {
	return newMalformedToolCallError("codex response invalid", calls)
}

func newMalformedToolCallError(kind string, calls []MalformedToolCall) *MalformedToolCallError {
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		parts = append(parts, fmt.Sprintf("tool call %d (%s) has incomplete or non-object JSON arguments", call.Index, call.Name))
	}
	code := "invalid_tool_arguments"
	return &MalformedToolCallError{
		Kind:      kind,
		Code:      code,
		Message:   fmt.Sprintf("%s: code=%s: %s", kind, code, strings.Join(parts, "; ")),
		ToolCalls: calls,
	}
}

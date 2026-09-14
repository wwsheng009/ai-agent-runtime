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

// MalformedToolCallError 表示模型生成了无法解析为 JSON 对象格式的工具调用参数。
//
// 与 openAIProtocolError / codexResponseError 不同，这不是传输或协议故障，而是
// 模型输出内容本身非法：流是完整读完的，只是参数文本不能解析为 JSON 对象。
// 取证把这一类再分成两种成因（见 FinishReason / Truncated）：
//   - 语法退化：模型写出了非法 JSON 字面量（例如 `{"timeout": 120s}`，裸 token
//     未加引号），finish_reason=tool_calls，换一次采样有机会拿到合法参数；
//   - 预算截断：finish_reason=length，参数在聚合前被 completion 预算切断，
//     同样预算重采样必然再截断，必须先扩大输出预算或拆分 payload。
//
// 两种成因都可恢复：retry policy 按 invalid_tool_arguments 做有界重试
// （短退避 + 连续退化上限 + 预算扩展）；重试耗尽后由执行层降级：把「参数非法 +
// 工具 schema」作为工具执行反馈注入下一轮，让模型按 schema 重新输出参数。
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
	// FinishReason 是本次响应的 finish_reason（TrimSpace 后原样保留）。
	// 适配器在丢弃响应前把它带出来，供上层区分「预算截断」与「语法退化」，
	// 并让 next_action / 事件载荷给出正确建议。可能为空（provider 未返回，
	// 或 codex 响应既非 incomplete 也无 finish 语义）。
	FinishReason string
	// Truncated 表示 finish_reason 命中输出预算上限（length/max_tokens 等）：
	// 参数是被切断的，不是 JSON 语法退化。上层据此走「扩大输出预算」而不是
	// 「等价重采样」——同预算重放必然再次截断。
	Truncated bool
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

// RetryErrorCode 保持 invalid_tool_arguments 的 retry 分类标识
// （retry reason：invalid_tool_arguments）。该错误现在按退化采样做有界重试，
// 重试耗尽后由执行层降级 re-prompt 兜底。
func (e *MalformedToolCallError) RetryErrorCode() string {
	if e == nil {
		return ""
	}
	return e.Code
}

// newOpenAIMalformedToolCallError 构造 openai 协议风格的 MalformedToolCallError。
// finishReason 来自响应/流状态的 finish_reason，用于标记 Truncated。
func newOpenAIMalformedToolCallError(finishReason string, calls []MalformedToolCall) *MalformedToolCallError {
	return newMalformedToolCallError("openai_stream_protocol_error", finishReason, calls)
}

// newCodexMalformedToolCallError 构造 codex 协议风格的 MalformedToolCallError。
func newCodexMalformedToolCallError(finishReason string, calls []MalformedToolCall) *MalformedToolCallError {
	return newMalformedToolCallError("codex response invalid", finishReason, calls)
}

func newMalformedToolCallError(kind string, finishReason string, calls []MalformedToolCall) *MalformedToolCallError {
	trimmedFinishReason := strings.TrimSpace(finishReason)
	truncated := isOutputBudgetFinishReason(trimmedFinishReason)
	parts := make([]string, 0, len(calls))
	for _, call := range calls {
		if truncated {
			parts = append(parts, fmt.Sprintf(
				"tool call %d (%s) arguments were cut off by the completion budget (finish_reason=%s); the call was not executed; retry with a larger output budget or split the payload into smaller chunks",
				call.Index, call.Name, trimmedFinishReason))
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"tool call %d (%s) has incomplete or non-object JSON arguments (call not executed; retried with bounded backoff, then re-prompted with the tool schema)",
			call.Index, call.Name))
	}
	code := "invalid_tool_arguments"
	return &MalformedToolCallError{
		Kind:         kind,
		Code:         code,
		Message:      fmt.Sprintf("%s: code=%s: %s", kind, code, strings.Join(parts, "; ")),
		ToolCalls:    calls,
		FinishReason: trimmedFinishReason,
		Truncated:    truncated,
	}
}

// isOutputBudgetFinishReason 判断 finish_reason 是否命中输出预算上限。
// 语义与 llm.IsMaxOutputTokensStop 一致；adapter 是被 llm 导入的子包，
// 不能反向导入父包，因此这里保留一份实现。
func isOutputBudgetFinishReason(finishReason string) bool {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "length", "max_tokens", "max_output_tokens", "max_output_tokens_exceeded":
		return true
	default:
		return false
	}
}

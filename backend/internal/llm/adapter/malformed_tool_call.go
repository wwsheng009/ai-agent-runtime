package adapter

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// 参数解析失败的稳定分类（写入错误消息与运行时事件，供离线聚合）。
// 只记录分类/长度/哈希，不落原始命令或密钥（见分析文档 P0-2 安全约束）。
const (
	// MalformedArgumentsParseNotObject：JSON 合法但顶层不是对象（数组/字符串/
	// 数字/布尔/null），模型把参数写成了别的类型。
	MalformedArgumentsParseNotObject = "not_object"
	// MalformedArgumentsParseUnterminatedString：文本在字符串字面量中间结束
	// （未闭合引号），既可能是语法退化，也可能是被预算截断。
	MalformedArgumentsParseUnterminatedString = "unterminated_string"
	// MalformedArgumentsParseUnterminatedContainer：括号/花括号未闭合。
	MalformedArgumentsParseUnterminatedContainer = "unterminated_container"
	// MalformedArgumentsParseBareLiteral：裸字面量（如 `{"timeout": 60s}`、
	// `{"a": undefined}`），2026-09-27 样本中的主因。
	MalformedArgumentsParseBareLiteral = "bare_literal"
	// MalformedArgumentsParseInvalidEscape：非法转义序列。
	MalformedArgumentsParseInvalidEscape = "invalid_escape"
	// MalformedArgumentsParseSyntax：其它 JSON 语法错误。
	MalformedArgumentsParseSyntax = "syntax_error"
	// MalformedArgumentsParseOther：非语法类解析失败。
	MalformedArgumentsParseOther = "other"
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
	// ParseClass 是解析失败的稳定分类（MalformedArgumentsParse* 之一）。
	ParseClass string
	// ParseOffset 是 syntax error 的字节偏移（不可用时为 0）。
	ParseOffset int64
	// ArgumentBytes 是原始 arguments 的字节长度。
	ArgumentBytes int
	// ArgumentSHA256 是原始 arguments 的 SHA-256 前 16 位十六进制，
	// 用于离线聚合同一阵型而无需落原始文本。
	ArgumentSHA256 string
	// ArgumentFragments 是流式 arguments 的增量片段数（0 表示非流式/未知），
	// 用于离线区分「单个大 delta」与「多片拼接」。
	ArgumentFragments int
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

// ToolCallArgumentsTruncated 报告参数是否因 completion 预算被截断
// （finish_reason=length/max_tokens 等），而不是 JSON 语法退化。
//
// 上层重试策略用它区分两条恢复通道（2026-09-27 证据：65/66 条
// invalid_tool_arguments 是裸 duration 语法退化，finish_reason=tool_calls，
// 与输出预算无关）：
//   - true：被预算切断，同预算重采样必然再截断，先扩大预算；
//   - false：语法退化，扩大预算无效，应做有界重采样并让执行层 re-prompt。
func (e *MalformedToolCallError) ToolCallArgumentsTruncated() bool {
	if e == nil {
		return false
	}
	return e.Truncated
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
				"tool call %d (%s) arguments were cut off by the completion budget (finish_reason=%s%s); the call was not executed; retry with a larger output budget or split the payload into smaller chunks",
				call.Index, call.Name, trimmedFinishReason, malformedCallEvidenceSuffix(call)))
			continue
		}
		parts = append(parts, fmt.Sprintf(
			"tool call %d (%s) has incomplete or non-object JSON arguments (%s; call not executed; retried with bounded backoff, then re-prompted with the tool schema)",
			call.Index, call.Name, malformedCallEvidence(call)))
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

// malformedCallEvidence 渲染一条调用的脱敏诊断证据（分类/偏移/长度/哈希）。
func malformedCallEvidence(call MalformedToolCall) string {
	class := strings.TrimSpace(call.ParseClass)
	if class == "" {
		class = MalformedArgumentsParseOther
	}
	evidence := fmt.Sprintf("parse_class=%s arg_bytes=%d", class, call.ArgumentBytes)
	if call.ParseOffset > 0 {
		evidence += fmt.Sprintf(" offset=%d", call.ParseOffset)
	}
	if call.ArgumentSHA256 != "" {
		evidence += " arg_sha256=" + call.ArgumentSHA256
	}
	if call.ArgumentFragments > 0 {
		evidence += fmt.Sprintf(" delta_fragments=%d", call.ArgumentFragments)
	}
	return evidence
}

func malformedCallEvidenceSuffix(call MalformedToolCall) string {
	return "; " + malformedCallEvidence(call)
}

// newMalformedToolCall 组装带解析证据的非法调用描述。
func newMalformedToolCall(index int, id, name, arguments string, fragments int, parseErr error, decoded map[string]interface{}) MalformedToolCall {
	call := MalformedToolCall{
		Index:             index,
		ID:                id,
		Name:              name,
		Arguments:         arguments,
		ArgumentBytes:     len(arguments),
		ArgumentFragments: fragments,
	}
	call.ParseClass, call.ParseOffset = classifyMalformedArguments(arguments, parseErr, decoded)
	if arguments != "" {
		sum := sha256.Sum256([]byte(arguments))
		call.ArgumentSHA256 = hex.EncodeToString(sum[:8])
	}
	return call
}

// classifyMalformedArguments 把 json.Unmarshal 的失败归入稳定分类。
// 分类只依赖错误类型与文本形态，不依赖具体工具名，避免维护工具白名单。
func classifyMalformedArguments(arguments string, parseErr error, decoded map[string]interface{}) (string, int64) {
	if parseErr == nil {
		// 合法 JSON（decoded 为 nil 表示顶层是 null）但不是对象。
		return MalformedArgumentsParseNotObject, 0
	}
	if typeErr, ok := parseErr.(*json.UnmarshalTypeError); ok {
		return MalformedArgumentsParseNotObject, typeErr.Offset
	}
	syntaxErr, ok := parseErr.(*json.SyntaxError)
	if !ok {
		return MalformedArgumentsParseOther, 0
	}
	message := strings.ToLower(syntaxErr.Error())
	switch {
	case strings.Contains(message, "unexpected end of json input"):
		return classifyMalformedTruncation(arguments), syntaxErr.Offset
	case strings.Contains(message, "invalid escape"):
		return MalformedArgumentsParseInvalidEscape, syntaxErr.Offset
	case strings.Contains(message, "after object key:value pair"),
		strings.Contains(message, "after top-level value"),
		strings.Contains(message, "looking for beginning of value"):
		if isBareLiteralRune(syntaxErrorRune(syntaxErr.Error())) {
			return MalformedArgumentsParseBareLiteral, syntaxErr.Offset
		}
		return MalformedArgumentsParseSyntax, syntaxErr.Offset
	default:
		return MalformedArgumentsParseSyntax, syntaxErr.Offset
	}
}

// classifyMalformedTruncation 在 json 报「unexpected end of JSON input」时进一步
// 区分未闭合字符串与未闭合容器（引号/括号计数只作启发式，足够离线聚合）。
func classifyMalformedTruncation(arguments string) string {
	trimmed := strings.TrimRight(arguments, " \t\r\n")
	if countUnescapedQuotes(trimmed)%2 == 1 {
		return MalformedArgumentsParseUnterminatedString
	}
	if strings.Count(trimmed, "{") > strings.Count(trimmed, "}") ||
		strings.Count(trimmed, "[") > strings.Count(trimmed, "]") {
		return MalformedArgumentsParseUnterminatedContainer
	}
	return MalformedArgumentsParseSyntax
}

// countUnescapedQuotes 统计未被反斜杠转义的引号数量。
func countUnescapedQuotes(text string) int {
	count := 0
	escaped := false
	for _, r := range text {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == '"':
			count++
		}
	}
	return count
}

// syntaxErrorRune 从 encoding/json 的错误文本中取出问题字符，
// 形如 `invalid character 's' after object key:value pair`。
func syntaxErrorRune(message string) rune {
	const marker = "invalid character "
	index := strings.Index(message, marker)
	if index < 0 {
		return 0
	}
	rest := message[index+len(marker):]
	if len(rest) == 0 {
		return 0
	}
	quote := rest[0]
	if quote != '\'' && quote != '"' {
		return 0
	}
	rest = rest[1:]
	if len(rest) == 0 {
		return 0
	}
	runes := []rune(rest)
	if runes[0] == '\\' && len(runes) > 1 {
		return runes[1]
	}
	return runes[0]
}

// isBareLiteralRune 判断问题字符是否像「裸字面量」的开头：ASCII 字母或下划线
// （`60s` 的 s、`undefined` 的 u、`nan` 的 n）。纯标点/数字的语法错误归为一般
// syntax_error，避免把 `{"a": 1,}` 这类问题误报成裸字面量。
func isBareLiteralRune(r rune) bool {
	return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
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

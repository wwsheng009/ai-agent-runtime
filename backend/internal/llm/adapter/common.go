package adapter

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const assistantReasoningDetailsKey = "reasoning_details"

// NormalizeToolCalls 将各协议出口的 tool_calls 形状统一为规范格式（入库形状）。
//
// 支持的输入形状：
//   - OpenAI 嵌套：{id, type, function:{name, arguments}}
//   - Responses/codex 扁平：{id|call_id, name, arguments}（type 可缺省或为 function_call）
//   - codex custom：{id, type:"custom_tool_call", name, input}
//   - Anthropic tool_use：{type:"tool_use", id, name, input}（input 即参数）
//   - Gemini 原生：{name, args|arguments}（无 id/type）
//
// 输出（与统一格式层 encodeProviderToolCalls 一致）：
//   - function: {id, type:"function", function:{name, arguments}}
//   - custom:   {id, type:"custom_tool_call", name, input}
//
// 非标准 type（如 Responses 协议的 "function_call"）一律收敛为 "function"；
// 缺 id 时生成 call_<index>；arguments/input 统一为 JSON 字符串。
func NormalizeToolCalls(rawCalls []map[string]interface{}) []map[string]interface{} {
	if len(rawCalls) == 0 {
		return nil
	}
	result := make([]map[string]interface{}, 0, len(rawCalls))
	for i, call := range rawCalls {
		if len(call) == 0 {
			continue
		}
		toolType := strings.ToLower(strings.TrimSpace(asCodexString(call["type"])))
		if toolType == "custom_tool_call" {
			result = append(result, map[string]interface{}{
				"id":    toolCallID(call, i),
				"type":  "custom_tool_call",
				"name":  toolCallName(call),
				"input": toolCallInput(call),
			})
			continue
		}
		result = append(result, map[string]interface{}{
			"id":   toolCallID(call, i),
			"type": "function",
			"function": map[string]interface{}{
				"name":      toolCallName(call),
				"arguments": toolCallArgumentsJSON(call),
			},
		})
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// toolCallID 提取 id，兼容 id/call_id 两个字段；都缺失时生成 call_<index>。
func toolCallID(call map[string]interface{}, index int) string {
	if id := strings.TrimSpace(asCodexString(call["id"])); id != "" {
		return id
	}
	if id := strings.TrimSpace(asCodexString(call["call_id"])); id != "" {
		return id
	}
	return fmt.Sprintf("call_%d", index)
}

// toolCallName 提取工具名，兼容顶层 name 与 function.name。
func toolCallName(call map[string]interface{}) string {
	if name := strings.TrimSpace(asCodexString(call["name"])); name != "" {
		return name
	}
	if fn, ok := call["function"].(map[string]interface{}); ok {
		if name := strings.TrimSpace(asCodexString(fn["name"])); name != "" {
			return name
		}
	}
	return ""
}

// toolCallArgumentsJSON 提取普通工具调用的参数并统一为 JSON 字符串。
// 兼容 function.arguments、顶层 arguments/args，以及 Anthropic tool_use 的 input。
func toolCallArgumentsJSON(call map[string]interface{}) string {
	var raw interface{}
	if fn, ok := call["function"].(map[string]interface{}); ok {
		raw = fn["arguments"]
	} else if v, ok := call["arguments"]; ok {
		raw = v
	} else if v, ok := call["args"]; ok {
		raw = v
	} else if v, ok := call["input"]; ok {
		// Anthropic tool_use 的 input 即参数对象
		raw = v
	}
	return normalizeJSONArgumentsValue(raw)
}

// toolCallInput 提取 custom_tool_call 的 input，兼容顶层 input/arguments 与嵌套 function.arguments。
func toolCallInput(call map[string]interface{}) string {
	if v, ok := call["input"]; ok {
		return normalizeFreeformValue(v)
	}
	if v, ok := call["arguments"]; ok {
		return normalizeFreeformValue(v)
	}
	if fn, ok := call["function"].(map[string]interface{}); ok {
		return normalizeFreeformValue(fn["arguments"])
	}
	return ""
}

// normalizeJSONArgumentsValue 将普通工具调用的参数统一为 JSON 字符串。
// 字符串按 JSON 语义解析（toolargs.DecodeJSON 会解包 {"_raw": "..."} 形状，
// 与 anthropic/openai 适配器旧行为一致）；map 先做 _raw 解包再序列化。
func normalizeJSONArgumentsValue(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return marshalArgsMap(toolargs.DecodeJSON(v))
	case map[string]interface{}:
		return marshalArgsMap(toolargs.Normalize(v))
	}
	return "{}"
}

// normalizeFreeformValue 将 custom_tool_call 的 input 原样保留（freeform 语义，
// 如 apply_patch 的 patch 内容不是 JSON，不能解析）；map 序列化为 JSON。
func normalizeFreeformValue(raw interface{}) string {
	switch v := raw.(type) {
	case string:
		return v
	case map[string]interface{}:
		return marshalArgsMap(v)
	}
	return ""
}

func marshalArgsMap(args map[string]interface{}) string {
	if len(args) == 0 {
		return "{}"
	}
	if data, err := json.Marshal(args); err == nil {
		return string(data)
	}
	return "{}"
}

// reasoningModels 推理模型列表
var reasoningModels = []string{
	"o1", "o1-preview", "o1-mini",
	"o3", "o3-mini",
	"gpt-5", "gpt-5-preview",
}

// getReasoningModels 获取推理模型列表
func getReasoningModels() []string {
	return reasoningModels
}

// isReasoningModelPrefix 检查是否为推理模型前缀
func isReasoningModelPrefix(model string) bool {
	for _, prefix := range reasoningModels {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// extractReasoningContent 从响应中提取推理内容
func extractReasoningContent(result map[string]interface{}) string {
	// 尝试从 message 中获取 reasoning_content
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
					return reasoning
				}
			}
		}
	}

	// 尝试从 delta 中获取推理内容（流式）
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
					return reasoning
				}
			}
		}
	}

	return ""
}

func attachReasoningBlock(msg map[string]interface{}, reasoning *runtimetypes.ReasoningBlock) map[string]interface{} {
	if len(msg) == 0 || reasoning == nil {
		return msg
	}
	if strings.TrimSpace(reasoning.DisplayText()) == "" && strings.TrimSpace(reasoning.OpaqueState) == "" && len(reasoning.Metadata) == 0 {
		return msg
	}
	if display := strings.TrimSpace(reasoning.DisplayText()); display != "" {
		msg["reasoning_content"] = display
	}
	if outputItems, ok := reasoning.Metadata[codexResponseOutputItemsKey]; ok {
		msg[codexResponseOutputItemsKey] = outputItems
	}
	if encoded := reasoning.ToMap(); len(encoded) > 0 {
		msg[assistantReasoningDetailsKey] = encoded
	}
	return msg
}

// checkFinishReason 检查完成原因
func checkFinishReason(result map[string]interface{}) string {
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if finishReason, ok := choice["finish_reason"].(string); ok {
				return finishReason
			}
		}
	}
	return ""
}

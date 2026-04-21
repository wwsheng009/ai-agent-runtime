package functions

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	llmadapter "github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
)

// FunctionCallBuilder Function Call 构建器接口
type FunctionCallBuilder interface {
	// BuildFunctions 构建用于请求的 functions/tool 格式
	BuildFunctions(schemas []map[string]interface{}) interface{}

	// ExtractToolCalls 从响应中提取 Function Call
	ExtractToolCalls(result map[string]interface{}) []ToolCall

	// BuildAssistantMessage 构建 assistant 消息（包含 Function Call）
	BuildAssistantMessage(toolCalls []ToolCall) map[string]interface{}

	// BuildAssistantMessageWithReasoning 构建 assistant 消息（包含 Function Call 和 reasoning_content）
	BuildAssistantMessageWithReasoning(toolCalls []ToolCall, reasoning string) map[string]interface{}

	// BuildToolResponse 构建 tool response 消息
	BuildToolResponse(toolCallID string, result string) map[string]interface{}
}

// ToolCall 工具调用信息
type ToolCall struct {
	ID       string                 `json:"id"`
	Function string                 `json:"function"`
	Args     map[string]interface{} `json:"arguments"`
	Raw      map[string]interface{} `json:"-"`
}

// GetFunctionCallBuilder 根据协议类型获取对应的 Function Call 构建器
func GetFunctionCallBuilder(protocol string) FunctionCallBuilder {
	switch protocol {
	case "openai", "gemini":
		return &OpenAIFunctionCallBuilder{}
	case "codex":
		return &CodexFunctionCallBuilder{}
	case "anthropic":
		return &AnthropicFunctionCallBuilder{}
	default:
		return &OpenAIFunctionCallBuilder{} // 默认使用 OpenAI 格式
	}
}

// isValidJSON 检查字符串是否是有效的JSON
func isValidJSON(s string) bool {
	if s == "" {
		return false
	}
	trimmed := strings.TrimSpace(s)
	var js interface{}
	err := json.Unmarshal([]byte(trimmed), &js)
	return err == nil
}

// ValidateToolCalls 验证ToolCalls是否完整（用于流式响应）
func ValidateToolCalls(toolCalls []ToolCall) ([]ToolCall, error) {
	validCalls := make([]ToolCall, 0, len(toolCalls))
	var incompleteCalls []ToolCall

	for _, tc := range toolCalls {
		// 检查Raw中是否有arguments字段
		if raw, ok := tc.Raw["function"].(map[string]interface{}); ok {
			if argsStr, ok := raw["arguments"].(string); ok && argsStr != "" {
				// 检查arguments是否是有效的JSON
				if !isValidJSON(argsStr) {
					// arguments不完整，等待更多数据
					incompleteCalls = append(incompleteCalls, tc)
					continue
				}
				// arguments是有效的JSON，可以执行（即使是空对象{}）
				validCalls = append(validCalls, tc)
				continue
			}
		}

		// 如果没有arguments字符串，检查Args是否已经有解析后的数据
		if tc.Args != nil {
			// Args可能为空对象{}，这也是有效的
			validCalls = append(validCalls, tc)
			continue
		}

		// 没有有效的arguments字段，跳过这个tool call
		fmt.Printf("[警告] ToolCall %s 没有有效的参数，跳过执行\n", tc.ID)
	}

	if len(incompleteCalls) > 0 {
		fmt.Fprintf(os.Stderr, "[错误] 检测到 %d 个不完整的工具调用（可能是流式响应数据损坏），无法执行\n", len(incompleteCalls))
		for _, tc := range incompleteCalls {
			if raw, ok := tc.Raw["function"].(map[string]interface{}); ok {
				if argsStr, ok := raw["arguments"].(string); ok {
					fmt.Fprintf(os.Stderr, "  - ToolCall %s (%s):\n", tc.ID, tc.Function)
					fmt.Fprintf(os.Stderr, "    长度: %d 字节\n", len(argsStr))
					fmt.Fprintf(os.Stderr, "    内容: %s\n", argsStr)

					// 诊断 JSON 结构
					openBraces := strings.Count(argsStr, "{")
					closeBraces := strings.Count(argsStr, "}")
					if openBraces != closeBraces {
						fmt.Fprintf(os.Stderr, "    ⚠️  大括号不匹配: 开=%d, 闭=%d\n", openBraces, closeBraces)
					}

					if !strings.HasSuffix(strings.TrimSpace(argsStr), "}") {
						fmt.Fprintf(os.Stderr, "    ⚠️  JSON 未闭合\n")
					}

					fmt.Fprintf(os.Stderr, "    💡 建议: 检查网络连接和代理配置，尝试重试请求\n")
				}
			}
		}
	}

	return validCalls, nil
}

// ==================== OpenAI/Gemini Function Call 构建器 ====================

// OpenAIFunctionCallBuilder OpenAI/Gemini 协议的 Function Call 构建器
type OpenAIFunctionCallBuilder struct{}

// BuildFunctions 构建用于请求的 tools 格式（OpenAI/Gemini 格式）
func (b *OpenAIFunctionCallBuilder) BuildFunctions(schemas []map[string]interface{}) interface{} {
	tools := make([]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		tools = append(tools, map[string]interface{}{
			"type": "function",
			"function": map[string]interface{}{
				"description": schema["description"],
				"name":        schema["name"],
				"parameters":  schema["parameters"].(map[string]interface{}),
			},
		})
	}
	return tools
}

// ExtractToolCalls 从响应中提取 Function Call
func (b *OpenAIFunctionCallBuilder) ExtractToolCalls(result map[string]interface{}) []ToolCall {
	toolCalls := []ToolCall{}

	// 尝试 []map[string]interface{} 类型（用于内部传递）
	if choicesMap, ok := result["choices"].([]map[string]interface{}); ok && len(choicesMap) > 0 {
		if msg, ok := choicesMap[0]["message"].(map[string]interface{}); ok {
			return b.extractToolCallsFromMessage(msg)
		}
	}

	// 尝试 []interface{} 类型（JSON 解析的标准格式）
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				return b.extractToolCallsFromMessage(msg)
			}
		}
	}

	return toolCalls
}

// extractToolCallsFromMessage 从 message 中提取工具调用
func (b *OpenAIFunctionCallBuilder) extractToolCallsFromMessage(msg map[string]interface{}) []ToolCall {
	toolCalls := []ToolCall{}

	// 调试：打印收到的message
	if toolCallsRawMap, ok := msg["tool_calls"].([]map[string]interface{}); ok && len(toolCallsRawMap) > 0 {
		msgJSON, _ := json.Marshal(msg)
		fmt.Printf("[调试] 收到的message: %s\n", string(msgJSON))
	}

	// 尝试 []map[string]interface{} 类型
	if toolCallsRawMap, ok := msg["tool_calls"].([]map[string]interface{}); ok {
		for _, tcMap := range toolCallsRawMap {
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				args := make(map[string]interface{})
				if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
					// 尝试解析JSON，如果是流式响应中的不完整片段，解析失败是正常的
					if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
						// 流式响应中arguments可能分片到达，不完整JSON是预期行为
						// 只有当arguments看起来像完整JSON但解析失败时才记录警告
						if strings.HasSuffix(argsStr, "}") || strings.HasSuffix(argsStr, "}") {
							fmt.Printf("[警告] JSON解析失败: %v, arguments: %s\n", err, argsStr)
						}
						// args保持空map，后续如果有完整数据会再次解析
					}
				} else if argsMap, ok := fn["arguments"].(map[string]interface{}); ok {
					args = argsMap
				}

				id, _ := tcMap["id"].(string)
				name, _ := fn["name"].(string)

				toolCalls = append(toolCalls, ToolCall{
					ID:       id,
					Function: name,
					Args:     args,
					Raw:      tcMap,
				})
			}
		}
	} else if toolCallsRaw, ok := msg["tool_calls"].([]interface{}); ok {
		// 备用：尝试 []interface{} 类型
		for _, tc := range toolCallsRaw {
			if tcMap, ok := tc.(map[string]interface{}); ok {
				if fn, ok := tcMap["function"].(map[string]interface{}); ok {
					args := make(map[string]interface{})
					if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
						// 尝试解析JSON，如果是流式响应中的不完整片段，解析失败是正常的
						if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
							// 流式响应中arguments可能分片到达，不完整JSON是预期行为
							// 只有当arguments看起来像完整JSON但解析失败时才记录警告
							if strings.HasSuffix(argsStr, "}") || strings.HasSuffix(argsStr, "}") {
								fmt.Printf("[警告] JSON解析失败: %v, arguments: %s\n", err, argsStr)
							}
							// args保持空map，后续如果有完整数据会再次解析
						}
					} else if argsMap, ok := fn["arguments"].(map[string]interface{}); ok {
						args = argsMap
					}

					id, _ := tcMap["id"].(string)
					name, _ := fn["name"].(string)

					toolCalls = append(toolCalls, ToolCall{
						ID:       id,
						Function: name,
						Args:     args,
						Raw:      tcMap,
					})
				}
			}
		}
	}

	return toolCalls
}

// BuildAssistantMessage 构建 assistant 消息（OpenAI/Gemini 格式，包含 tool_calls）
func (b *OpenAIFunctionCallBuilder) BuildAssistantMessage(toolCalls []ToolCall) map[string]interface{} {
	if len(toolCalls) == 0 {
		return map[string]interface{}{
			"role":    "assistant",
			"content": "",
		}
	}

	rawCalls := make([]map[string]interface{}, len(toolCalls))
	for i, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		rawCalls[i] = map[string]interface{}{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      tc.Function,
				"arguments": string(argsJSON),
			},
		}
	}

	return map[string]interface{}{
		"role":       "assistant",
		"content":    "",
		"tool_calls": rawCalls,
	}
}

// BuildAssistantMessageWithReasoning 构建 assistant 消息（包含 reasoning_content）
func (b *OpenAIFunctionCallBuilder) BuildAssistantMessageWithReasoning(toolCalls []ToolCall, reasoning string) map[string]interface{} {
	msg := b.BuildAssistantMessage(toolCalls)
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	return msg
}

// BuildToolResponse 构建 tool response 消息
func (b *OpenAIFunctionCallBuilder) BuildToolResponse(toolCallID string, result string) map[string]interface{} {
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"role":         "tool",
		"content":      result,
	}
}

// ==================== Anthropic Function Call 构建器 ====================

// AnthropicFunctionCallBuilder Anthropic 协议的 Function Call 构建器
type AnthropicFunctionCallBuilder struct{}

// BuildFunctions 构建用于请求的 tools 格式（Anthropic 格式）
func (b *AnthropicFunctionCallBuilder) BuildFunctions(schemas []map[string]interface{}) interface{} {
	tools := make([]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		tools = append(tools, map[string]interface{}{
			"name":         schema["name"],
			"description":  schema["description"],
			"input_schema": schema["parameters"].(map[string]interface{}),
		})
	}
	return tools
}

// ExtractToolCalls 从响应中提取 Function Call
func (b *AnthropicFunctionCallBuilder) ExtractToolCalls(result map[string]interface{}) []ToolCall {
	toolCalls := []ToolCall{}

	if contentRaw, ok := result["content"].([]interface{}); ok {
		for _, item := range contentRaw {
			if block, ok := item.(map[string]interface{}); ok {
				if blockType, ok := block["type"].(string); ok && blockType == "tool_use" {
					id, _ := block["id"].(string)
					name, _ := block["name"].(string)
					args := make(map[string]interface{})

					if rawArgs, ok := block["input"].(map[string]interface{}); ok {
						args = rawArgs
					}

					toolCalls = append(toolCalls, ToolCall{
						ID:       id,
						Function: name,
						Args:     args,
						Raw:      block,
					})
				}
			}
		}
	}

	return toolCalls
}

// BuildAssistantMessage 构建 assistant 消息（Anthropic 格式）
func (b *AnthropicFunctionCallBuilder) BuildAssistantMessage(toolCalls []ToolCall) map[string]interface{} {
	if len(toolCalls) == 0 {
		return map[string]interface{}{
			"role":    "assistant",
			"content": []interface{}{},
		}
	}

	content := make([]interface{}, 0, len(toolCalls))
	for _, tc := range toolCalls {
		content = append(content, map[string]interface{}{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Function,
			"input": tc.Args,
		})
	}

	return map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
}

// BuildAssistantMessageWithReasoning 构建 assistant 消息（包含 thinking）
func (b *AnthropicFunctionCallBuilder) BuildAssistantMessageWithReasoning(toolCalls []ToolCall, reasoning string) map[string]interface{} {
	msg := b.BuildAssistantMessage(toolCalls)
	content := msg["content"].([]interface{})

	// 在 content 数组开头添加 thinking 块
	if reasoning != "" {
		thinkingBlock := map[string]interface{}{
			"type":    "thinking",
			"content": reasoning,
		}
		newContent := make([]interface{}, len(content)+1)
		newContent[0] = thinkingBlock
		for i, c := range content {
			newContent[i+1] = c
		}
		msg["content"] = newContent
	}

	return msg
}

// BuildToolResponse 构建 tool response 消息
func (b *AnthropicFunctionCallBuilder) BuildToolResponse(toolCallID string, result string) map[string]interface{} {
	return map[string]interface{}{
		"role": "user",
		"content": []interface{}{
			map[string]interface{}{
				"type":        "tool_result",
				"tool_use_id": toolCallID,
				"content":     result,
			},
		},
	}
}

// ==================== Gemini Function Call 构建器 ====================

// GeminiFunctionCallBuilder Gemini 协议的 Function Call 构建器
type GeminiFunctionCallBuilder struct{}

// BuildFunctions 构建用于请求的 tools 格式（Gemini 格式）
func (b *GeminiFunctionCallBuilder) BuildFunctions(schemas []map[string]interface{}) interface{} {
	functionDeclarations := make([]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		functionDeclarations = append(functionDeclarations, map[string]interface{}{
			"name":        schema["name"],
			"description": schema["description"],
			"parameters":  schema["parameters"],
		})
	}
	return functionDeclarations
}

// ExtractToolCalls 从响应中提取 Function Call
func (b *GeminiFunctionCallBuilder) ExtractToolCalls(result map[string]interface{}) []ToolCall {
	toolCalls := []ToolCall{}

	// Gemini 使用 function_calls 字段
	if functionCalls, ok := result["functionCalls"].([]interface{}); ok {
		for _, fc := range functionCalls {
			if fcMap, ok := fc.(map[string]interface{}); ok {
				name, _ := fcMap["name"].(string)
				args := make(map[string]interface{})

				if rawArgs, ok := fcMap["args"].(map[string]interface{}); ok {
					args = rawArgs
				}

				toolCalls = append(toolCalls, ToolCall{
					ID:       "", // Gemini 不需要 ID
					Function: name,
					Args:     args,
					Raw:      fcMap,
				})
			}
		}
	}

	return toolCalls
}

// BuildAssistantMessage 构建 assistant 消息（Gemini 格式）
func (b *GeminiFunctionCallBuilder) BuildAssistantMessage(toolCalls []ToolCall) map[string]interface{} {
	if len(toolCalls) == 0 {
		return map[string]interface{}{
			"role":  "model",
			"parts": []interface{}{},
		}
	}

	parts := make([]interface{}, 0, len(toolCalls))
	for _, tc := range toolCalls {
		parts = append(parts, map[string]interface{}{
			"functionCall": map[string]interface{}{
				"name": tc.Function,
				"args": tc.Args,
			},
		})
	}

	return map[string]interface{}{
		"role":  "model",
		"parts": parts,
	}
}

// BuildAssistantMessageWithReasoning 构建 assistant 消息（ Gemini 格式暂不支持 reasoning）
func (b *GeminiFunctionCallBuilder) BuildAssistantMessageWithReasoning(toolCalls []ToolCall, reasoning string) map[string]interface{} {
	// Gemini 暂不支持 reasoning_content
	return b.BuildAssistantMessage(toolCalls)
}

// BuildToolResponse 构建 tool response 消息
func (b *GeminiFunctionCallBuilder) BuildToolResponse(toolCallID string, result string) map[string]interface{} {
	return map[string]interface{}{
		"role": "function",
		"parts": []interface{}{
			map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name": toolCallID,
					"response": map[string]interface{}{
						"content": result,
					},
				},
			},
		},
	}
}

// ==================== Codex/Responses API Function Call 构建器 ====================

// CodexFunctionCallBuilder Codex/Responses API 协议的 Function Call 构建器
// Codex/Responses API 使用扁平的工具定义格式，没有嵌套的 function 对象
type CodexFunctionCallBuilder struct{}

// BuildFunctions 构建用于请求的 tools 格式（Codex/Responses API 格式）
// Codex 格式（扁平）:
//
//	{
//	  "type": "function",
//	  "name": "...",
//	  "description": "...",
//	  "parameters": {...},
//	  "strict": true  // optional
//	}
func (b *CodexFunctionCallBuilder) BuildFunctions(schemas []map[string]interface{}) interface{} {
	tools := make([]map[string]interface{}, 0, len(schemas))
	for _, schema := range schemas {
		tool := map[string]interface{}{
			"type":        "function",
			"name":        schema["name"],
			"description": schema["description"],
			"parameters":  schema["parameters"].(map[string]interface{}),
		}
		// 添加 strict 字段（默认 true）
		tool["strict"] = true
		tools = append(tools, tool)
	}
	return llmadapter.NormalizeCodexToolsForRequest(tools)
}

// ExtractToolCalls 从响应中提取 Function Call（兼容 OpenAI tool_calls 格式）
func (b *CodexFunctionCallBuilder) ExtractToolCalls(result map[string]interface{}) []ToolCall {
	if toolCallsRawMap, ok := result["tool_calls"].([]map[string]interface{}); ok && len(toolCallsRawMap) > 0 {
		calls := make([]ToolCall, 0, len(toolCallsRawMap))
		for _, tcMap := range toolCallsRawMap {
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				args := make(map[string]interface{})
				if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
					_ = json.Unmarshal([]byte(argsStr), &args)
				} else if argsMap, ok := fn["arguments"].(map[string]interface{}); ok {
					args = argsMap
				}

				id, _ := tcMap["id"].(string)
				name, _ := fn["name"].(string)

				calls = append(calls, ToolCall{
					ID:       id,
					Function: name,
					Args:     args,
					Raw:      tcMap,
				})
			}
		}
		return calls
	}
	return nil
}

// BuildAssistantMessage 构建 assistant 消息（Codex 需要转换成 input 数组格式）
// Codex 不使用 assistant 消息格式，而是通过 BuildRequest 转换
func (b *CodexFunctionCallBuilder) BuildAssistantMessage(toolCalls []ToolCall) map[string]interface{} {
	if len(toolCalls) == 0 {
		return map[string]interface{}{
			"role":    "assistant",
			"content": "",
		}
	}

	// 构建标准的 OpenAI tool_calls 格式（会被 convertMessagesToCodexInput 转换）
	rawCalls := make([]map[string]interface{}, len(toolCalls))
	for i, tc := range toolCalls {
		argsJSON, _ := json.Marshal(tc.Args)
		rawCalls[i] = map[string]interface{}{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      tc.Function,
				"arguments": string(argsJSON),
			},
		}
	}

	return map[string]interface{}{
		"role":       "assistant",
		"content":    "",
		"tool_calls": rawCalls,
	}
}

// BuildAssistantMessageWithReasoning 构建 assistant 消息（包含 reasoning_content）
func (b *CodexFunctionCallBuilder) BuildAssistantMessageWithReasoning(toolCalls []ToolCall, reasoning string) map[string]interface{} {
	msg := b.BuildAssistantMessage(toolCalls)
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
		msg["reasoning_details"] = map[string]interface{}{
			"format":     "openai_responses",
			"summary":    reasoning,
			"streamable": true,
			"visibility": "summary",
		}
	}
	return msg
}

// BuildToolResponse 构建 tool response 消息（保持 OpenAI tool 消息格式）
func (b *CodexFunctionCallBuilder) BuildToolResponse(toolCallID string, result string) map[string]interface{} {
	return map[string]interface{}{
		"tool_call_id": toolCallID,
		"role":         "tool",
		"content":      result,
	}
}

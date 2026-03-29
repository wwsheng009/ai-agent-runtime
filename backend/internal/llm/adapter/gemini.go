package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// GeminiAdapter Gemini 协议适配器
type GeminiAdapter struct{}

// Name 返回适配器名称
func (a *GeminiAdapter) Name() string {
	return "gemini"
}

// BuildRequest 构建请求体
// Gemini API 使用 contents 数组格式不同于 OpenAI 的 messages
func (a *GeminiAdapter) BuildRequest(config RequestConfig) map[string]interface{} {
	// 将 OpenAI 格式的 messages 转换为 Gemini 格式的 contents
	contents := make([]map[string]interface{}, 0, len(config.Messages))

	for _, msg := range config.Messages {
		// 类型断言
		roleVal, ok := msg["role"].(string)
		if !ok {
			continue
		}
		role := a.convertRole(roleVal)

		content := msg["content"]

		// 检查是否已经是 Gemini 格式（包含 parts 字段）
		if parts, hasParts := msg["parts"]; hasParts {
			// 直接使用 Gemini 格式的消息
			geminiMsg := map[string]interface{}{
				"role":  role,
				"parts": parts,
			}
			contents = append(contents, geminiMsg)
			continue
		}

		// 对于字符串内容，检查是否是 JSON (Function Response)
		if contentStr, ok := content.(string); ok {
			// 特殊处理 Function Response (tool role)
			// Function Response 具有 parts.functionResponse 结构
			if strings.Contains(contentStr, "functionResponse") {
				// 尝试解析为 FunctionResponse 格式（来自 BuildToolResponse）
				var toolResponse map[string]interface{}
				if err := json.Unmarshal([]byte(contentStr), &toolResponse); err == nil {
					// 验证是否包含 functionResponse
					if val, ok := toolResponse["parts"]; ok {
						if parts, ok := val.([]interface{}); ok && len(parts) > 0 {
							if part, ok := parts[0].(map[string]interface{}); ok {
								if _, hasFuncResponse := part["functionResponse"]; hasFuncResponse {
									// 如果解析成功且包含 functionResponse，直接使用
									contents = append(contents, toolResponse)
									continue
								}
							}
						}
					}
				}
			}

			// 普通文本消息
			contents = append(contents, map[string]interface{}{
				"role":  role,
				"parts": []map[string]interface{}{{"text": contentStr}},
			})
		}
	}

	request := map[string]interface{}{
		"contents": contents,
	}

	// 添加 generationConfig（可选参数）
	generationConfig := make(map[string]interface{})

	// Gemini 不使用 max_tokens，而是使用 maxOutputTokens
	if config.MaxTokens > 0 {
		generationConfig["maxOutputTokens"] = config.MaxTokens
	}

	// temperature
	generationConfig["temperature"] = config.Temperature

	// topP 默认值
	generationConfig["topP"] = 0.95

	if len(generationConfig) > 0 {
		request["generationConfig"] = generationConfig
	}

	// 添加 Function Call (Gemini 使用 tools)
	if config.Functions != nil {
		request["tools"] = config.Functions
	}

	return request
}

// BuildHeaders 构建请求头
func (a *GeminiAdapter) BuildHeaders(cfg AdapterConfig) map[string]string {
	return mergeHeaderMaps(map[string]string{
		"Content-Type":   "application/json",
		"x-goog-api-key": cfg.APIKey,
	}, cfg.Headers)
}

// ExtractResponse 从响应中提取内容
func (a *GeminiAdapter) ExtractResponse(result map[string]interface{}) string {
	// Gemini 非流式响应格式：candidates[0].content.parts[0].text
	if candidates, ok := result["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if part, ok := parts[0].(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok {
							return text
						}
					}
				}
			}
		}
	}

	// 错误处理：检查是否有错误信息
	if errorMsg, ok := result["error"].(map[string]interface{}); ok {
		if message, ok := errorMsg["message"].(string); ok {
			return fmt.Sprintf("Error: %s", message)
		}
	}

	return ""
}

// ExtractReasoning 从响应中提取推理内容（thinking）
// Gemini 目前不支持独立的 reasoning_content 字段
func (a *GeminiAdapter) ExtractReasoning(result map[string]interface{}) string {
	return ""
}

// ProcessResponse 统一处理响应，提取 reasoning、content 和 tool_calls
func (a *GeminiAdapter) ProcessResponse(result map[string]interface{}) ProcessResult {
	procResult := ProcessResult{
		Reasoning:    "",
		Content:      "",
		HasToolCalls: false,
		ToolCalls:    []map[string]interface{}{},
	}

	// 提取 candidates
	if candidates, ok := result["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					for _, part := range parts {
						if partMap, ok := part.(map[string]interface{}); ok {
							// 检查 text 内容
							if text, ok := partMap["text"].(string); ok {
								procResult.Content += text
							}
							// 检查 Function Call
							if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
								procResult.HasToolCalls = true
								procResult.ToolCalls = append(procResult.ToolCalls, functionCall)
							}
						}
					}
				}
			}
		}
	}

	return procResult
}

// ExtractStreamContent 从流式响应中提取内容
// Gemini SSE 流式响应格式：candidates[0].content.parts[0].text
func (a *GeminiAdapter) ExtractStreamContent(result map[string]interface{}) string {
	return extractGeminiText(result)
}

// ExtractStreamReasoning 从流式响应中提取推理内容
// Gemini 推理模型如果支持，在这里实现
func (a *GeminiAdapter) ExtractStreamReasoning(result map[string]interface{}) string {
	return ""
}

// GeminiStreamState Gemini 流式响应累积状态
// Gemini 比 OpenAI 简单：functionCall 是完整的，不需要拼接 arguments
type GeminiStreamState struct {
	Content      strings.Builder
	ToolCalls    []map[string]interface{} // Gemini functionCall 直接完整
	FinishReason string
}

// NewGeminiStreamState 创建新的 Gemini 流式状态
func NewGeminiStreamState() *GeminiStreamState {
	return &GeminiStreamState{
		ToolCalls: make([]map[string]interface{}, 0),
	}
}

// ToMap 将累积结果转换为 map 格式
func (s *GeminiStreamState) ToMap() map[string]interface{} {
	result := map[string]interface{}{
		"content": s.Content.String(),
	}

	if s.FinishReason != "" {
		result["finish_reason"] = s.FinishReason
	}

	if len(s.ToolCalls) > 0 {
		// 转换为 OpenAI 兼容格式
		toolCalls := make([]map[string]interface{}, 0, len(s.ToolCalls))
		for i, fc := range s.ToolCalls {
			name, _ := fc["name"].(string)
			args := fc["args"]
			if args == nil {
				args = fc["arguments"]
			}

			// 将 args 序列化为字符串（如果它是 map）
			argsStr := ""
			switch v := args.(type) {
			case map[string]interface{}:
				if data, err := json.Marshal(v); err == nil {
					argsStr = string(data)
				}
			case string:
				argsStr = v
			}

			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   fmt.Sprintf("call_%d", i), // Gemini 没有提供 id，生成一个
				"type": "function",
				"function": map[string]interface{}{
					"name":      name,
					"arguments": argsStr,
				},
			})
		}
		result["tool_calls"] = toolCalls
	}

	return result
}

// HandleResponse 处理完整响应（流式或非流式）
func (a *GeminiAdapter) HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	if isStream {
		return a.handleStreamResponse(respBody, onContent)
	}

	// 非流式处理
	var result map[string]interface{}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, err
	}

	procResult := a.ProcessResponse(result)
	return a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), nil
}

// handleStreamResponse 处理流式响应
func (a *GeminiAdapter) handleStreamResponse(respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	state := NewGeminiStreamState()

	scanner := bufio.NewScanner(respBody)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024) // 最大 20MB

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		a.parseGeminiChunk(state, chunk, onContent)

		// 检查是否结束
		if state.FinishReason != "" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取流式响应失败: %w", err)
	}

	streamData := state.ToMap()
	toolCalls, _ := streamData["tool_calls"].([]map[string]interface{})
	content, _ := streamData["content"].(string)

	return a.BuildAssistantMessage(content, toolCalls, ""), nil
}

// parseGeminiChunk 解析 Gemini 流式 chunk
func (a *GeminiAdapter) parseGeminiChunk(state *GeminiStreamState, chunk map[string]interface{}, onContent func(string)) {
	candidates, ok := chunk["candidates"].([]interface{})
	if !ok || len(candidates) == 0 {
		return
	}

	candidate, ok := candidates[0].(map[string]interface{})
	if !ok {
		return
	}

	// 检查 finishReason（注意是 camelCase）
	if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "" {
		state.FinishReason = finishReason
		return
	}

	content, ok := candidate["content"].(map[string]interface{})
	if !ok {
		return
	}

	parts, ok := content["parts"].([]interface{})
	if !ok {
		return
	}

	for _, p := range parts {
		part, ok := p.(map[string]interface{})
		if !ok {
			continue
		}

		// 解析文本
		a.parseGeminiText(state, part, onContent)

		// 解析 Function Call（完整的，不需要拼接）
		a.parseGeminiFunctionCall(state, part)
	}
}

// parseGeminiText 解析 Gemini parts 中的 text
func (a *GeminiAdapter) parseGeminiText(state *GeminiStreamState, part map[string]interface{}, onContent func(string)) {
	text, ok := part["text"].(string)
	if !ok || text == "" {
		return
	}

	state.Content.WriteString(text)
	if onContent != nil {
		onContent(text)
	}
}

// parseGeminiFunctionCall 解析 Gemini parts 中的 functionCall
// Gemini 的 functionCall 是完整的，不需要流式拼接
func (a *GeminiAdapter) parseGeminiFunctionCall(state *GeminiStreamState, part map[string]interface{}) {
	functionCall, ok := part["functionCall"].(map[string]interface{})
	if !ok {
		return
	}

	// 直接追加完整的 functionCall
	state.ToolCalls = append(state.ToolCalls, functionCall)
}

// extractGeminiText 从 Gemini 响应中提取文本
func extractGeminiText(result map[string]interface{}) string {
	if candidates, ok := result["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					if part, ok := parts[0].(map[string]interface{}); ok {
						if text, ok := part["text"].(string); ok && text != "" {
							return text
						}
					}
				}
			}
		}
	}
	return ""
}

// ExtractStreamToolCalls 从流式累积数据中提取 ToolCalls
func (a *GeminiAdapter) ExtractStreamToolCalls(streamData map[string]interface{}) []map[string]interface{} {
	if toolCalls, ok := streamData["tool_calls"].([]map[string]interface{}); ok {
		return toolCalls
	}
	return nil
}

// BuildAssistantMessage 构建 assistant 消息（Gemini 格式）
func (a *GeminiAdapter) BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{} {
	msg := map[string]interface{}{
		"role":    "assistant",
		"content": content,
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if reasoning != "" {
		msg["reasoning_content"] = reasoning
	}
	return msg
}

// ExtractToolCallsFromRawCalls 从已解析的 tool_calls 数组直接构造 ToolCall 列表
// Gemini 格式：{name: string, args: object} （没有 id，args 是 object 不是 string）
// OpenAI 格式：{id: string, type: "function", function: {name: string, arguments: string}}
func (a *GeminiAdapter) ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []ToolCall {
	toolCalls := make([]ToolCall, 0, len(rawCalls))

	for i, tcMap := range rawCalls {
		// 检查是否是 Gemini 原生格式（直接有 name 和 args）
		if name, ok := tcMap["name"].(string); ok {
			args := make(map[string]interface{})

			// Gemini 的 args 可能是 "args" 或 "arguments"
			if argsMap, ok := tcMap["args"].(map[string]interface{}); ok {
				args = argsMap
			} else if argsMap, ok := tcMap["arguments"].(map[string]interface{}); ok {
				args = argsMap
			} else if argsStr, ok := tcMap["arguments"].(string); ok && argsStr != "" {
				// 兼容已转换为 OpenAI 格式的情况
				json.Unmarshal([]byte(argsStr), &args)
			}

			argsJSON, _ := json.Marshal(args)
			toolCalls = append(toolCalls, ToolCall{
				ID: fmt.Sprintf("call_%d", i), // Gemini 没有提供 id，生成一个
				Function: ToolCallFunction{
					Name:      name,
					Arguments: string(argsJSON),
				},
				Type: "function",
			})
			continue
		}

		// 兼容 OpenAI 格式（从 ToMap 转换后的格式）
		if fn, ok := tcMap["function"].(map[string]interface{}); ok {
			args := make(map[string]interface{})
			if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
				json.Unmarshal([]byte(argsStr), &args)
			} else if argsMap, ok := fn["arguments"].(map[string]interface{}); ok {
				args = argsMap
			}

			id, _ := tcMap["id"].(string)
			name, _ := fn["name"].(string)

			argsJSON, _ := json.Marshal(args)
			toolCalls = append(toolCalls, ToolCall{
				ID: id,
				Function: ToolCallFunction{
					Name:      name,
					Arguments: string(argsJSON),
				},
				Type: "function",
			})
		}
	}

	return toolCalls
}

// IsReasoningModel 判断是否为推理模型
func (a *GeminiAdapter) IsReasoningModel(model string) bool {
	// Gemini 目前没有专门的推理模型类型
	return false
}

// AccumulateStreamData 累积流式数据块
// 保留向后兼容
func (a *GeminiAdapter) AccumulateStreamData(streamData map[string]interface{}, chunkData map[string]interface{}) bool {
	// 初始化
	if _, exists := streamData["content"]; !exists {
		streamData["content"] = ""
		streamData["finish_reason"] = ""
		streamData["tool_calls"] = []map[string]interface{}{}
	}

	// 提取 candidates
	if candidates, ok := chunkData["candidates"].([]interface{}); ok && len(candidates) > 0 {
		if candidate, ok := candidates[0].(map[string]interface{}); ok {
			// 检查 finishReason
			if finishReason, ok := candidate["finishReason"].(string); ok {
				streamData["finish_reason"] = finishReason
				if finishReason != "" {
					return true
				}
			}

			if content, ok := candidate["content"].(map[string]interface{}); ok {
				if parts, ok := content["parts"].([]interface{}); ok && len(parts) > 0 {
					for _, part := range parts {
						if partMap, ok := part.(map[string]interface{}); ok {
							// 提取 text 内容
							if text, ok := partMap["text"].(string); ok {
								streamData["content"] = streamData["content"].(string) + text
							}
							// 提取 Function Call（单次完整传输，不需要增量合并）
							if functionCall, ok := partMap["functionCall"].(map[string]interface{}); ok {
								existingCalls := streamData["tool_calls"].([]map[string]interface{})
								streamData["tool_calls"] = append(existingCalls, functionCall)
							}
						}
					}
				}
			}
		}
	}

	return false
}

// GetAPIPath 获取默认 API 路径
func (a *GeminiAdapter) GetAPIPath() string {
	// Gemini 使用 v1beta API
	return ""
}

// convertRole 转换角色名称（从 OpenAI 格式到 Gemini 格式）
func (a *GeminiAdapter) convertRole(role string) string {
	// OpenAI: user/assistant/system
	// Gemini: user/model
	switch strings.ToLower(role) {
	case "user":
		return "user"
	case "assistant", "system":
		return "model"
	default:
		return "user"
	}
}

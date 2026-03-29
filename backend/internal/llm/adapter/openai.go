package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// OpenAIAdapter OpenAI/Gemini protocol adapter
type OpenAIAdapter struct{}

// Name returns adapter name
func (a *OpenAIAdapter) Name() string {
	return "openai"
}

// BuildRequest builds request body
func (a *OpenAIAdapter) BuildRequest(config RequestConfig) map[string]interface{} {
	request := map[string]interface{}{
		"model":    config.Model,
		"messages": config.Messages,
		"stream":   config.Stream,
	}

	// Set max_tokens
	if config.MaxTokens > 0 {
		request["max_tokens"] = config.MaxTokens
	}

	if effort := deriveOpenAIReasoningEffort(config.Model, config.ReasoningEffort, config.Thinking); effort != "" {
		request["reasoning_effort"] = effort
	}

	// Reasoning models use temperature 0
	if !a.IsReasoningModel(config.Model) {
		request["temperature"] = config.Temperature
	}

	// Add Function Call
	if config.Functions != nil {
		request["tools"] = config.Functions
		request["tool_choice"] = "auto"
	}

	return request
}

// BuildHeaders builds request headers
func (a *OpenAIAdapter) BuildHeaders(cfg AdapterConfig) map[string]string {
	return mergeHeaderMaps(map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + cfg.APIKey,
	}, cfg.Headers)
}

// ExtractResponse extracts content from response
func (a *OpenAIAdapter) ExtractResponse(result map[string]interface{}) string {
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if _, hasToolCalls := msg["tool_calls"]; hasToolCalls {
					return ""
				}
				if content, ok := msg["content"].(string); ok && content != "" {
					return content
				}
			}
		}
	}
	return ""
}

// ExtractReasoning extracts reasoning content from response
func (a *OpenAIAdapter) ExtractReasoning(result map[string]interface{}) string {
	return extractReasoningContent(result)
}

// ProcessResponse processes response and extracts reasoning, content, and tool_calls
func (a *OpenAIAdapter) ProcessResponse(result map[string]interface{}) ProcessResult {
	procResult := ProcessResult{
		Reasoning:    "",
		Content:      "",
		HasToolCalls: false,
		ToolCalls:    []map[string]interface{}{},
	}

	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := choice["message"].(map[string]interface{}); ok {
				if reasoning, ok := msg["reasoning_content"].(string); ok {
					procResult.Reasoning = reasoning
				}
				if content, ok := msg["content"].(string); ok {
					procResult.Content = content
				}
				if toolCallsRaw, ok := msg["tool_calls"].([]interface{}); ok {
					procResult.HasToolCalls = true
					for _, tc := range toolCallsRaw {
						if tcMap, ok := tc.(map[string]interface{}); ok {
							procResult.ToolCalls = append(procResult.ToolCalls, tcMap)
						}
					}
				}
				if fn, ok := msg["function_call"].(map[string]interface{}); ok {
					procResult.HasToolCalls = true
					procResult.ToolCalls = append(procResult.ToolCalls, fn)
				}
			}
		}
	}

	return procResult
}

// ExtractStreamContent extracts content from streaming response
func (a *OpenAIAdapter) ExtractStreamContent(result map[string]interface{}) string {
	if choices, ok := result["choices"].([]interface{}); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]interface{}); ok {
			if delta, ok := choice["delta"].(map[string]interface{}); ok {
				if content, ok := delta["content"].(string); ok && content != "" {
					return content
				}
			}
		}
	}
	return ""
}

// ExtractStreamReasoning extracts reasoning content from streaming response
func (a *OpenAIAdapter) ExtractStreamReasoning(result map[string]interface{}) string {
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

// StreamToolCall 流式工具调用（内部使用，避免与公共 ToolCall 冲突）
type StreamToolCall struct {
	ID   string
	Type string
	Name string
	Args strings.Builder
}

// StreamState 管理流式响应的累积状态
type StreamState struct {
	Content      strings.Builder
	ToolCalls    map[int]*StreamToolCall // key 是 tool_call 的 index
	FinishReason string
}

// NewStreamState 创建新的累积器
func NewStreamState() *StreamState {
	return &StreamState{
		ToolCalls: make(map[int]*StreamToolCall),
	}
}

// getToolCall 获取或创建指定 index 的 tool call
func (s *StreamState) getToolCall(index int) *StreamToolCall {
	if tc, exists := s.ToolCalls[index]; exists {
		return tc
	}
	tc := &StreamToolCall{}
	s.ToolCalls[index] = tc
	return tc
}

// BuildMessage 构建 AssistantMessage
func (s *StreamState) BuildMessage() *AssistantMessage {
	msg := &AssistantMessage{
		Content: s.Content.String(),
	}
	if len(s.ToolCalls) == 0 {
		return msg
	}
	// 按 index 顺序输出
	for i := 0; i < len(s.ToolCalls); i++ {
		tc := s.ToolCalls[i]
		if tc == nil {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, tc.ToToolCall())
	}
	return msg
}

// AssistantMessage 表示助手消息
type AssistantMessage struct {
	Content   string
	ToolCalls []*ToolCall
}

// ParseArguments 解析 StreamToolCall 的参数，自动修复不完整 JSON
func (tc *StreamToolCall) ParseArguments() map[string]interface{} {
	argStr := repairJSON(tc.Args.String())
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(argStr), &result); err != nil {
		return map[string]interface{}{
			"_raw": tc.Args.String(),
		}
	}
	return result
}

// ToToolCall 将 StreamToolCall 转换为 ToolCall
func (tc *StreamToolCall) ToToolCall() *ToolCall {
	return &ToolCall{
		ID:   tc.ID,
		Type: tc.Type,
		Function: ToolCallFunction{
			Name:      tc.Name,
			Arguments: tc.Args.String(),
		},
	}
}

// repairJSON 修复不完整的 JSON（如缺少闭合括号）
func repairJSON(s string) string {
	open := strings.Count(s, "{")
	close := strings.Count(s, "}")
	if open > close {
		s += strings.Repeat("}", open-close)
	}
	return s
}

// ToMap 将累积结果转换为 map 格式
func (s *StreamState) ToMap() map[string]interface{} {
	result := map[string]interface{}{
		"content": s.Content.String(),
	}

	if s.FinishReason != "" {
		result["finish_reason"] = s.FinishReason
	}

	// 将 tool_calls 转换为数组格式，按 index 顺序
	if len(s.ToolCalls) > 0 {
		maxIndex := 0
		for idx := range s.ToolCalls {
			if idx > maxIndex {
				maxIndex = idx
			}
		}

		toolCalls := make([]map[string]interface{}, 0, maxIndex+1)
		for i := 0; i <= maxIndex; i++ {
			if tc, exists := s.ToolCalls[i]; exists {
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   tc.ID,
					"type": tc.Type,
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Args.String(),
					},
				})
			}
		}
		result["tool_calls"] = toolCalls
	}

	return result
}

// HandleResponse handles complete response (stream or non-stream)
func (a *OpenAIAdapter) HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	if isStream {
		state := NewStreamState()

		scanner := bufio.NewScanner(respBody)
		buf := make([]byte, 0, 1024*1024)
		scanner.Buffer(buf, 20*1024*1024) // 最大 20MB

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" || data == "" {
				continue
			}

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue
			}

			parseChunk(state, chunk, onContent)

			if state.FinishReason != "" {
				break
			}
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("读取流式响应失败: %w", err)
		}

		// 转换为最终结果
		streamData := state.ToMap()
		toolCalls, _ := streamData["tool_calls"].([]map[string]interface{})
		content, _ := streamData["content"].(string)

		return a.BuildAssistantMessage(content, toolCalls, ""), nil
	}

	var result map[string]interface{}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, err
	}

	procResult := a.ProcessResponse(result)
	return a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), nil
}

// parseChunk 解析单个流式 chunk
func parseChunk(state *StreamState, chunk map[string]interface{}, onContent func(string)) {
	choices, ok := chunk["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return
	}

	choice, ok := choices[0].(map[string]interface{})
	if !ok {
		return
	}

	// 处理 finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		state.FinishReason = fr
	}

	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return
	}

	parseContent(state, delta, onContent)
	parseToolCalls(state, delta)
}

// parseContent 解析 delta 中的 content
func parseContent(state *StreamState, delta map[string]interface{}, onContent func(string)) {
	content, ok := delta["content"].(string)
	if !ok || content == "" {
		return
	}
	state.Content.WriteString(content)
	if onContent != nil {
		onContent(content)
	}
}

// parseToolCalls 解析 delta 中的 tool_calls
func parseToolCalls(state *StreamState, delta map[string]interface{}) {
	raw, ok := delta["tool_calls"]
	if !ok {
		return
	}

	arr, ok := raw.([]interface{})
	if !ok {
		return
	}

	for _, item := range arr {
		tcMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		indexFloat, ok := tcMap["index"].(float64)
		if !ok {
			continue // 没有 index 则跳过
		}
		index := int(indexFloat)

		tc := state.getToolCall(index)

		if id, ok := tcMap["id"].(string); ok {
			tc.ID = id
		}
		if typ, ok := tcMap["type"].(string); ok {
			tc.Type = typ
		}

		parseFunction(tc, tcMap)
	}
}

// parseFunction 解析 tool_call 中的 function 字段
func parseFunction(tc *StreamToolCall, tcMap map[string]interface{}) {
	fn, ok := tcMap["function"].(map[string]interface{})
	if !ok {
		return
	}

	if name, ok := fn["name"].(string); ok {
		tc.Name = name
	}
	if args, ok := fn["arguments"].(string); ok {
		tc.Args.WriteString(args)
	}
}

// ExtractStreamToolCalls extracts tool_calls from accumulated streaming data
func (a *OpenAIAdapter) ExtractStreamToolCalls(streamData map[string]interface{}) []map[string]interface{} {
	if toolCalls, ok := streamData["tool_calls"].([]map[string]interface{}); ok {
		return toolCalls
	}
	return nil
}

// BuildAssistantMessage builds assistant message (OpenAI/Gemini format)
func (a *OpenAIAdapter) BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{} {
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

// ExtractToolCallsFromRawCalls 直接从已解析的 tool_calls 数组构造 ToolCall 列表
// 用于流式响应，自动修复不完整 JSON
func (a *OpenAIAdapter) ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []ToolCall {
	toolCalls := make([]ToolCall, 0, len(rawCalls))
	for _, tcMap := range rawCalls {
		if fn, ok := tcMap["function"].(map[string]interface{}); ok {
			args := make(map[string]interface{})
			if argsStr, ok := fn["arguments"].(string); ok && argsStr != "" {
				// 使用 JSON 修复后再解析
				fixedArgs := repairJSON(argsStr)
				if err := json.Unmarshal([]byte(fixedArgs), &args); err != nil {
					args = map[string]interface{}{"_raw": argsStr}
				}
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

// IsReasoningModel checks if model is a reasoning model
func (a *OpenAIAdapter) IsReasoningModel(model string) bool {
	return isReasoningModelPrefix(model)
}

// GetAPIPath returns default API path
func (a *OpenAIAdapter) GetAPIPath() string {
	return "/v1/chat/completions"
}

// AccumulateStreamData 累积流式数据块（向后兼容）
func (a *OpenAIAdapter) AccumulateStreamData(streamData map[string]interface{}, chunkData map[string]interface{}) bool {
	// 初始化
	if _, exists := streamData["content"]; !exists {
		streamData["content"] = ""
		streamData["finish_reason"] = ""
		streamData["tool_calls"] = []interface{}{}
	}

	// 检查 choices
	choices, ok := chunkData["choices"].([]interface{})
	if !ok || len(choices) == 0 {
		return false
	}

	choice := choices[0].(map[string]interface{})

	// 处理 finish_reason
	if fr, ok := choice["finish_reason"].(string); ok && fr != "" {
		streamData["finish_reason"] = fr
		return true
	}

	// 处理 content
	delta, ok := choice["delta"].(map[string]interface{})
	if !ok {
		return false
	}

	if content, ok := delta["content"].(string); ok && content != "" {
		streamData["content"] = streamData["content"].(string) + content
	}

	// 处理 tool_calls
	if rawToolCalls, ok := delta["tool_calls"].([]interface{}); ok {
		existingCalls := streamData["tool_calls"].([]interface{})
		for _, tc := range rawToolCalls {
			if tcMap, ok := tc.(map[string]interface{}); ok {
				existingCalls = append(existingCalls, tcMap)
			}
		}
		streamData["tool_calls"] = existingCalls
	}

	return false
}

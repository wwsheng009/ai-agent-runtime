package adapter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
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

	if effort := strings.TrimSpace(config.ReasoningEffort); effort != "" {
		request["reasoning_effort"] = effort
	}

	// Temperature only gets suppressed when the caller has explicitly marked
	// the request as targeting a reasoning-capable model.
	if !config.ReasoningModel {
		request["temperature"] = config.Temperature
	}

	// Add Function Call
	if config.Functions != nil {
		request["tools"] = config.Functions
	}

	applyOpenAICompatibleRequestMetadata(request, config)

	if config.Thinking != nil {
		request["thinking"] = config.Thinking
	}

	if config.Functions != nil {
		if _, exists := request["tool_choice"]; !exists {
			request["tool_choice"] = "auto"
		}
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
					cleanContent, toolCalls, _ := parseToolCallMarkupContent(content, true, 0)
					if len(toolCalls) > 0 {
						return cleanContent
					}
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
					procResult.ReasoningPresent = true
				} else if reasoning, ok := msg["reasoning"].(string); ok {
					procResult.Reasoning = reasoning
					procResult.ReasoningPresent = true
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
				if procResult.Content != "" {
					cleanContent, markupToolCalls, _ := parseToolCallMarkupContent(procResult.Content, true, len(procResult.ToolCalls))
					if len(markupToolCalls) > 0 {
						procResult.Content = cleanContent
						if !procResult.HasToolCalls {
							procResult.HasToolCalls = true
							procResult.ToolCalls = append(procResult.ToolCalls, markupToolCalls...)
						}
					}
				}
			}
		}
	}
	if strings.TrimSpace(procResult.Reasoning) != "" {
		procResult.ReasoningBlock = &runtimetypes.ReasoningBlock{
			Format:     "openai_compatible",
			Summary:    strings.TrimSpace(procResult.Reasoning),
			Streamable: true,
			Visibility: runtimetypes.ReasoningVisibilitySummary,
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
				if reasoning, ok := delta["reasoning"].(string); ok && reasoning != "" {
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
	Content          strings.Builder
	Reasoning        strings.Builder
	ReasoningPresent bool
	ToolCalls        map[int]*StreamToolCall // key 是 tool_call 的 index
	MarkupTail       string
	MarkupCalls      []map[string]interface{}
	FinishReason     string
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
	rawToolCalls := s.rawToolCalls()
	if len(rawToolCalls) == 0 {
		return msg
	}
	for _, tcMap := range rawToolCalls {
		toolCall := rawToolCallToAssistantToolCall(tcMap)
		if toolCall == nil {
			continue
		}
		msg.ToolCalls = append(msg.ToolCalls, toolCall)
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

const (
	toolCallStartTag = "<tool_call>"
	toolCallEndTag   = "</tool_call>"
	argKeyStartTag   = "<arg_key>"
	argKeyEndTag     = "</arg_key>"
	argValueStartTag = "<arg_value>"
	argValueEndTag   = "</arg_value>"
)

var toolCallMarkupTags = []string{
	toolCallStartTag,
	toolCallEndTag,
	argKeyStartTag,
	argKeyEndTag,
	argValueStartTag,
	argValueEndTag,
}

func rawToolCallToAssistantToolCall(tcMap map[string]interface{}) *ToolCall {
	if tcMap == nil {
		return nil
	}
	toolCall := &ToolCall{
		Type: "function",
	}
	if id, ok := tcMap["id"].(string); ok {
		toolCall.ID = id
	}
	if typ, ok := tcMap["type"].(string); ok && typ != "" {
		toolCall.Type = typ
	}
	fn, ok := tcMap["function"].(map[string]interface{})
	if !ok {
		return nil
	}
	if name, ok := fn["name"].(string); ok {
		toolCall.Function.Name = name
	}
	if args, ok := fn["arguments"].(string); ok {
		toolCall.Function.Arguments = args
	}
	if toolCall.Function.Name == "" {
		return nil
	}
	return toolCall
}

func parseToolCallMarkupContent(raw string, final bool, offset int) (string, []map[string]interface{}, string) {
	var text strings.Builder
	toolCalls := make([]map[string]interface{}, 0)
	remaining := raw

	for len(remaining) > 0 {
		tagIndex := strings.IndexByte(remaining, '<')
		if tagIndex == -1 {
			text.WriteString(remaining)
			remaining = ""
			break
		}
		if tagIndex > 0 {
			text.WriteString(remaining[:tagIndex])
			remaining = remaining[tagIndex:]
			continue
		}
		if strings.HasPrefix(remaining, toolCallStartTag) {
			endIndex := strings.Index(remaining[len(toolCallStartTag):], toolCallEndTag)
			if endIndex == -1 {
				if final {
					text.WriteString(remaining)
					remaining = ""
				}
				break
			}

			blockEnd := len(toolCallStartTag) + endIndex
			blockBody := remaining[len(toolCallStartTag):blockEnd]
			fullEnd := blockEnd + len(toolCallEndTag)
			if toolCall, ok := parseToolCallMarkupBlock(blockBody, offset+len(toolCalls)); ok {
				toolCalls = append(toolCalls, toolCall)
			} else {
				text.WriteString(remaining[:fullEnd])
			}
			remaining = remaining[fullEnd:]
			continue
		}

		if !final && hasPartialToolCallMarkupPrefix(remaining) {
			break
		}

		text.WriteByte('<')
		remaining = remaining[1:]
	}

	return text.String(), toolCalls, remaining
}

func hasPartialToolCallMarkupPrefix(raw string) bool {
	for _, tag := range toolCallMarkupTags {
		if strings.HasPrefix(tag, raw) {
			return true
		}
	}
	return false
}

func parseToolCallMarkupBlock(body string, index int) (map[string]interface{}, bool) {
	remaining := body
	name := strings.TrimSpace(remaining)
	if argIndex := strings.Index(remaining, argKeyStartTag); argIndex >= 0 {
		name = strings.TrimSpace(remaining[:argIndex])
		remaining = remaining[argIndex:]
	} else {
		remaining = ""
	}
	if name == "" {
		return nil, false
	}

	args := make(map[string]interface{})
	for strings.TrimSpace(remaining) != "" {
		remaining = strings.TrimSpace(remaining)
		if !strings.HasPrefix(remaining, argKeyStartTag) {
			return nil, false
		}
		remaining = remaining[len(argKeyStartTag):]

		keyEnd := strings.Index(remaining, argKeyEndTag)
		if keyEnd == -1 {
			return nil, false
		}
		key := strings.TrimSpace(remaining[:keyEnd])
		remaining = remaining[keyEnd+len(argKeyEndTag):]
		if key == "" {
			return nil, false
		}

		remaining = strings.TrimSpace(remaining)
		if !strings.HasPrefix(remaining, argValueStartTag) {
			return nil, false
		}
		remaining = remaining[len(argValueStartTag):]

		valueEnd := strings.Index(remaining, argValueEndTag)
		if valueEnd == -1 {
			return nil, false
		}
		value := remaining[:valueEnd]
		remaining = remaining[valueEnd+len(argValueEndTag):]
		args[key] = parseToolCallMarkupValue(value)
	}

	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, false
	}

	return map[string]interface{}{
		"id":   fmt.Sprintf("markup_tool_call_%d", index+1),
		"type": "function",
		"function": map[string]interface{}{
			"name":      name,
			"arguments": string(argsJSON),
		},
	}, true
}

func parseToolCallMarkupValue(raw string) interface{} {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	var decoded interface{}
	if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
		return decoded
	}
	return trimmed
}

// ToMap 将累积结果转换为 map 格式
func (s *StreamState) ToMap() map[string]interface{} {
	result := map[string]interface{}{
		"content": s.Content.String(),
	}
	if s.ReasoningPresent {
		result["reasoning"] = s.Reasoning.String()
	}

	if s.FinishReason != "" {
		result["finish_reason"] = s.FinishReason
	}

	if toolCalls := s.rawToolCalls(); len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}

	return result
}

func (s *StreamState) rawToolCalls() []map[string]interface{} {
	toolCalls := make([]map[string]interface{}, 0, len(s.ToolCalls)+len(s.MarkupCalls))
	if len(s.ToolCalls) > 0 {
		maxIndex := 0
		for idx := range s.ToolCalls {
			if idx > maxIndex {
				maxIndex = idx
			}
		}
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
	}
	if len(s.MarkupCalls) > 0 {
		toolCalls = append(toolCalls, s.MarkupCalls...)
	}
	return toolCalls
}

// HandleResponse handles complete response (stream or non-stream)
func (a *OpenAIAdapter) HandleResponse(isStream bool, respBody io.Reader, callbacks StreamCallbacks) (map[string]interface{}, error) {
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

			parseChunk(state, chunk, callbacks)

			if state.FinishReason != "" {
				break
			}
		}

		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("读取流式响应失败: %w", err)
		}
		flushPendingMarkupContent(state, callbacks)

		// 转换为最终结果
		streamData := state.ToMap()
		toolCalls, _ := streamData["tool_calls"].([]map[string]interface{})
		content, _ := streamData["content"].(string)
		reasoning, _ := streamData["reasoning"].(string)
		_, reasoningPresent := streamData["reasoning"]
		assistantMsg := a.buildAssistantMessageWithReasoningPresence(content, toolCalls, reasoning, reasoningPresent)
		if finishReason, _ := streamData["finish_reason"].(string); strings.TrimSpace(finishReason) != "" {
			assistantMsg["finish_reason"] = finishReason
		}
		var reasoningBlock *runtimetypes.ReasoningBlock
		if strings.TrimSpace(reasoning) != "" {
			reasoningBlock = &runtimetypes.ReasoningBlock{
				Format:     "openai_compatible",
				Summary:    strings.TrimSpace(reasoning),
				Streamable: true,
				Visibility: runtimetypes.ReasoningVisibilitySummary,
			}
		}
		return attachReasoningBlock(assistantMsg, reasoningBlock), nil
	}

	var result map[string]interface{}
	if err := json.NewDecoder(respBody).Decode(&result); err != nil {
		return nil, err
	}

	procResult := a.ProcessResponse(result)
	assistantMsg := a.buildAssistantMessageWithReasoningPresence(procResult.Content, procResult.ToolCalls, procResult.Reasoning, procResult.ReasoningPresent)
	return attachReasoningBlock(assistantMsg, procResult.ReasoningBlock), nil
}

// parseChunk 解析单个流式 chunk
func parseChunk(state *StreamState, chunk map[string]interface{}, callbacks StreamCallbacks) {
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

	parseReasoning(state, delta, callbacks)
	parseContent(state, delta, callbacks)
	parseToolCalls(state, delta)
}

// parseContent 解析 delta 中的 content
func parseContent(state *StreamState, delta map[string]interface{}, callbacks StreamCallbacks) {
	content, ok := delta["content"].(string)
	if !ok || content == "" {
		return
	}
	state.MarkupTail += content
	text, toolCalls, pending := parseToolCallMarkupContent(state.MarkupTail, false, len(state.MarkupCalls))
	if text != "" {
		state.Content.WriteString(text)
		callbacks.EmitText(text)
	}
	if len(toolCalls) > 0 {
		state.MarkupCalls = append(state.MarkupCalls, toolCalls...)
	}
	state.MarkupTail = pending
}

func flushPendingMarkupContent(state *StreamState, callbacks StreamCallbacks) {
	if state == nil || state.MarkupTail == "" {
		return
	}
	text, toolCalls, _ := parseToolCallMarkupContent(state.MarkupTail, true, len(state.MarkupCalls))
	if text != "" {
		state.Content.WriteString(text)
		callbacks.EmitText(text)
	}
	if len(toolCalls) > 0 {
		state.MarkupCalls = append(state.MarkupCalls, toolCalls...)
	}
	state.MarkupTail = ""
}

func parseReasoning(state *StreamState, delta map[string]interface{}, callbacks StreamCallbacks) {
	if reasoning, ok := delta["reasoning_content"].(string); ok {
		state.ReasoningPresent = true
		if reasoning != "" {
			state.Reasoning.WriteString(reasoning)
			callbacks.EmitReasoning(reasoning)
		}
		return
	}
	if reasoning, ok := delta["reasoning"].(string); ok {
		state.ReasoningPresent = true
		if reasoning != "" {
			state.Reasoning.WriteString(reasoning)
			callbacks.EmitReasoning(reasoning)
		}
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

		if id, ok := tcMap["id"].(string); ok && id != "" {
			tc.ID = id
		}
		if typ, ok := tcMap["type"].(string); ok && typ != "" {
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

	if name, ok := fn["name"].(string); ok && name != "" {
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

func (a *OpenAIAdapter) buildAssistantMessageWithReasoningPresence(content string, toolCalls []map[string]interface{}, reasoning string, reasoningPresent bool) map[string]interface{} {
	msg := a.BuildAssistantMessage(content, toolCalls, reasoning)
	if reasoningPresent {
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

// IsReasoningModel is retained for legacy callers only. Request assembly and
// session display now prefer explicit capability flags from configuration.
func (a *OpenAIAdapter) IsReasoningModel(model string) bool {
	return providercompat.IsDeepSeekModel(model) ||
		providercompat.LooksLikeOpenAIReasoningModel(model) ||
		isReasoningModelPrefix(model)
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

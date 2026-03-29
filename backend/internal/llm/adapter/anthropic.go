package adapter

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"

	anthropictypes "github.com/ai-gateway/ai-agent-runtime/internal/types/anthropic"
)

// AnthropicAdapter Anthropic 协议适配器
type AnthropicAdapter struct{}

// Name 返回适配器名称
func (a *AnthropicAdapter) Name() string {
	return "anthropic"
}

// BuildRequest 构建请求体
func (a *AnthropicAdapter) BuildRequest(config RequestConfig) map[string]interface{} {
	request := map[string]interface{}{
		"model":    config.Model,
		"messages": config.Messages,
		"stream":   config.Stream,
	}

	// Anthropic 必须设置 max_tokens
	if config.MaxTokens > 0 {
		request["max_tokens"] = config.MaxTokens
	} else {
		request["max_tokens"] = 4096
	}

	// 添加 Function Call (Anthropic 使用 tools)
	if config.Functions != nil {
		request["tools"] = config.Functions
	}

	thinking := cloneAnthropicThinking(config.Thinking)
	if thinking == nil && normalizeRuntimeReasoningEffort(config.ReasoningEffort) != "" {
		thinking = mapReasoningEffortToAnthropicThinking(config.Model, config.ReasoningEffort)
	}
	if thinking != nil && (normalizeAnthropicThinkingType(thinking.Type) != "" || normalizeAnthropicThinkingEffort(thinking.Effort) != "") {
		request["thinking"] = thinking
	}

	return request
}

// BuildHeaders 构建请求头
func (a *AnthropicAdapter) BuildHeaders(cfg AdapterConfig) map[string]string {
	headers := mergeHeaderMaps(map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         cfg.APIKey,
		"anthropic-version": "2023-06-01",
	}, cfg.Headers)

	if requiresAnthropicInterleavedThinkingBeta(cfg.Model, cfg.RequestBody) {
		existing := getHeaderValueCaseInsensitive(headers, "anthropic-beta")
		merged := mergeCommaSeparatedHeaderValue(existing, anthropicInterleavedThinkingBeta)
		setHeaderValueCaseInsensitive(headers, "anthropic-beta", merged)
	}

	return headers
}

func requiresAnthropicInterleavedThinkingBeta(model string, body map[string]interface{}) bool {
	if !isAnthropicSonnet46Model(model) {
		return false
	}

	thinking := extractAnthropicThinkingFromRequestBody(body)
	if thinking == nil {
		return false
	}

	return strings.EqualFold(strings.TrimSpace(thinking.Type), "enabled")
}

func isAnthropicSonnet46Model(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return strings.Contains(model, "claude-sonnet-4-6")
}

type anthropicThinkingHeaderProbe struct {
	Type         string
	BudgetTokens *int
	Effort       string
}

func extractAnthropicThinkingFromRequestBody(body map[string]interface{}) *anthropicThinkingHeaderProbe {
	if len(body) == 0 {
		return nil
	}

	rawThinking, ok := body["thinking"]
	if !ok || rawThinking == nil {
		return nil
	}

	switch thinking := rawThinking.(type) {
	case *anthropictypes.Thinking:
		if thinking == nil {
			return nil
		}
		return &anthropicThinkingHeaderProbe{
			Type:         thinking.Type,
			BudgetTokens: thinking.BudgetTokens,
			Effort:       thinking.Effort,
		}
	case anthropictypes.Thinking:
		return &anthropicThinkingHeaderProbe{
			Type:         thinking.Type,
			BudgetTokens: thinking.BudgetTokens,
			Effort:       thinking.Effort,
		}
	}

	thinking, ok := rawThinking.(map[string]interface{})
	if !ok {
		return nil
	}

	probe := &anthropicThinkingHeaderProbe{}
	if typ, ok := thinking["type"].(string); ok {
		probe.Type = typ
	}
	if effort, ok := thinking["effort"].(string); ok {
		probe.Effort = effort
	}
	switch value := thinking["budget_tokens"].(type) {
	case int:
		probe.BudgetTokens = &value
	case int32:
		v := int(value)
		probe.BudgetTokens = &v
	case int64:
		v := int(value)
		probe.BudgetTokens = &v
	case float64:
		v := int(value)
		probe.BudgetTokens = &v
	}

	if probe.Type == "" && probe.Effort == "" && probe.BudgetTokens == nil {
		return nil
	}
	return probe
}

// ExtractResponse 从响应中提取内容
func (a *AnthropicAdapter) ExtractResponse(result map[string]interface{}) string {
	// Anthropic 格式：content 是一个数组
	if content, ok := result["content"].([]interface{}); ok && len(content) > 0 {
		for _, c := range content {
			if item, ok := c.(map[string]interface{}); ok {
				if item["type"] == "text" {
					if text, ok := item["text"].(string); ok {
						return text
					}
				}
			}
		}
	}

	return ""
}

// ExtractReasoning 从响应中提取推理内容（thinking）
func (a *AnthropicAdapter) ExtractReasoning(result map[string]interface{}) string {
	return ""
}

// ProcessResponse 统一处理响应，提取 reasoning、content 和 tool_calls
func (a *AnthropicAdapter) ProcessResponse(result map[string]interface{}) ProcessResult {
	procResult := ProcessResult{
		Reasoning:    "",
		Content:      "",
		HasToolCalls: false,
		ToolCalls:    []map[string]interface{}{},
	}

	// 提取 content
	if content, ok := result["content"].([]interface{}); ok && len(content) > 0 {
		for _, item := range content {
			if itemMap, ok := item.(map[string]interface{}); ok {
				typ, _ := itemMap["type"].(string)

				if typ == "text" {
					// 提取文本内容
					if text, ok := itemMap["text"].(string); ok {
						procResult.Content += text
					}
				} else if typ == "tool_use" {
					// Function Call
					procResult.HasToolCalls = true
					procResult.ToolCalls = append(procResult.ToolCalls, itemMap)
				}
			}
		}
	}

	return procResult
}

// ExtractStreamContent 从流式响应中提取内容
func (a *AnthropicAdapter) ExtractStreamContent(result map[string]interface{}) string {
	// Anthropic 流式响应中的 content_block_delta 事件
	if delta, ok := result["delta"].(map[string]interface{}); ok {
		if text, ok := delta["text"].(string); ok {
			return text
		}
	}
	return ""
}

// ExtractStreamReasoning 从流式响应中提取推理内容
func (a *AnthropicAdapter) ExtractStreamReasoning(result map[string]interface{}) string {
	return ""
}

// AnthropicToolCall Anthropic 工具调用（流式累积）
type AnthropicToolCall struct {
	Index int
	ID    string
	Name  string
	Args  strings.Builder
}

// AnthropicStreamState Anthropic 流式响应累积状态
// Anthropic 是事件驱动的，需要跟踪 content_block
type AnthropicStreamState struct {
	Content    strings.Builder
	ToolCalls  []*AnthropicToolCall
	blocks     map[int]*AnthropicToolCall // key 是 content_block 的 index
	StopReason string
}

// NewAnthropicStreamState 创建新的 Anthropic 流式状态
func NewAnthropicStreamState() *AnthropicStreamState {
	return &AnthropicStreamState{
		ToolCalls: make([]*AnthropicToolCall, 0),
		blocks:    make(map[int]*AnthropicToolCall),
	}
}

// getOrCreateBlock 获取或创建指定 index 的 tool call block
func (s *AnthropicStreamState) getOrCreateBlock(index int) *AnthropicToolCall {
	if tc, exists := s.blocks[index]; exists {
		return tc
	}
	tc := &AnthropicToolCall{Index: index}
	s.blocks[index] = tc
	return tc
}

// ToMap 将累积结果转换为 map 格式（OpenAI 兼容）
func (s *AnthropicStreamState) ToMap() map[string]interface{} {
	result := map[string]interface{}{
		"content": s.Content.String(),
	}

	if s.StopReason != "" {
		result["finish_reason"] = s.StopReason
	}

	// 转换为 OpenAI 兼容格式
	if len(s.ToolCalls) > 0 {
		toolCalls := make([]map[string]interface{}, 0, len(s.ToolCalls))
		for _, tc := range s.ToolCalls {
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      tc.Name,
					"arguments": tc.Args.String(),
				},
			})
		}
		result["tool_calls"] = toolCalls
	}

	return result
}

// HandleResponse 处理完整响应（流式或非流式）
func (a *AnthropicAdapter) HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
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

// handleStreamResponse 处理 Anthropic 事件驱动的流式响应
// Anthropic SSE 格式：
//
//	event: content_block_start
//	data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}
//
//	event: content_block_start
//	data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","name":"get_weather","id":"toolu_123"}}
//
//	event: content_block_delta
//	data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\""}}
//
//	event: message_stop
func (a *AnthropicAdapter) handleStreamResponse(respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	state := NewAnthropicStreamState()

	scanner := bufio.NewScanner(respBody)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024) // 最大 20MB

	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		// 解析 event 行
		if strings.HasPrefix(line, "event:") {
			currentEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		}

		// 解析 data 行
		if !strings.HasPrefix(line, "data:") {
			continue
		}

		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}

		var payload map[string]interface{}
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			continue
		}

		// 根据事件类型处理
		a.handleAnthropicEvent(state, currentEvent, payload, onContent)

		// 检查是否结束
		if currentEvent == "message_stop" {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	// 转换为最终结果
	streamData := state.ToMap()
	toolCalls, _ := streamData["tool_calls"].([]map[string]interface{})
	content, _ := streamData["content"].(string)

	return a.BuildAssistantMessage(content, toolCalls, ""), nil
}

// handleAnthropicEvent 处理 Anthropic SSE 事件
func (a *AnthropicAdapter) handleAnthropicEvent(state *AnthropicStreamState, event string, payload map[string]interface{}, onContent func(string)) {
	switch event {
	case "content_block_start":
		a.handleContentBlockStart(state, payload)
	case "content_block_delta":
		a.handleContentBlockDelta(state, payload, onContent)
	case "message_delta":
		a.handleMessageDelta(state, payload)
	}
}

// handleContentBlockStart 处理 content_block_start 事件
// 初始化 tool_use block
func (a *AnthropicAdapter) handleContentBlockStart(state *AnthropicStreamState, payload map[string]interface{}) {
	indexFloat, ok := payload["index"].(float64)
	if !ok {
		return
	}
	index := int(indexFloat)

	cb, ok := payload["content_block"].(map[string]interface{})
	if !ok {
		return
	}

	typ, _ := cb["type"].(string)
	if typ != "tool_use" {
		return // 只处理 tool_use 类型
	}

	name, _ := cb["name"].(string)
	id, _ := cb["id"].(string)

	tc := &AnthropicToolCall{
		Index: index,
		ID:    id,
		Name:  name,
	}

	state.blocks[index] = tc
	state.ToolCalls = append(state.ToolCalls, tc)
}

// handleContentBlockDelta 处理 content_block_delta 事件
// 处理 text_delta 和 input_json_delta
func (a *AnthropicAdapter) handleContentBlockDelta(state *AnthropicStreamState, payload map[string]interface{}, onContent func(string)) {
	indexFloat, ok := payload["index"].(float64)
	if !ok {
		return
	}
	index := int(indexFloat)

	delta, ok := payload["delta"].(map[string]interface{})
	if !ok {
		return
	}

	deltaType, _ := delta["type"].(string)

	switch deltaType {
	case "text_delta":
		// 文本增量
		text, _ := delta["text"].(string)
		state.Content.WriteString(text)
		if onContent != nil {
			onContent(text)
		}

	case "input_json_delta":
		// Tool arguments 增量（流式拼接）
		tc := state.blocks[index]
		if tc == nil {
			return
		}
		part, _ := delta["partial_json"].(string)
		tc.Args.WriteString(part)
	}
}

// handleMessageDelta 处理 message_delta 事件
// 提取 stop_reason
func (a *AnthropicAdapter) handleMessageDelta(state *AnthropicStreamState, payload map[string]interface{}) {
	delta, ok := payload["delta"].(map[string]interface{})
	if !ok {
		return
	}
	if stopReason, ok := delta["stop_reason"].(string); ok {
		state.StopReason = stopReason
	}
}

// ExtractStreamToolCalls 从流式累积数据中提取 ToolCalls
func (a *AnthropicAdapter) ExtractStreamToolCalls(streamData map[string]interface{}) []map[string]interface{} {
	if toolCalls, ok := streamData["tool_calls"].([]map[string]interface{}); ok {
		return toolCalls
	}
	return nil
}

// BuildAssistantMessage 构建 assistant 消息
func (a *AnthropicAdapter) BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{} {
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
// Anthropic 原生格式：{type: "tool_use", id: string, name: string, input: object}
// OpenAI 兼容格式：{id: string, type: "function", function: {name: string, arguments: string}}
func (a *AnthropicAdapter) ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []ToolCall {
	toolCalls := make([]ToolCall, 0, len(rawCalls))

	for _, tcMap := range rawCalls {
		// 检查是否是 Anthropic 原生格式
		if typ, ok := tcMap["type"].(string); ok && typ == "tool_use" {
			id, _ := tcMap["id"].(string)
			name, _ := tcMap["name"].(string)

			args := make(map[string]interface{})
			if input, ok := tcMap["input"].(map[string]interface{}); ok {
				args = input
			}

			argsJSON, _ := json.Marshal(args)
			toolCalls = append(toolCalls, ToolCall{
				ID: id,
				Function: ToolCallFunction{
					Name:      name,
					Arguments: string(argsJSON),
				},
				Type: "function",
			})
			continue
		}

		// OpenAI 兼容格式
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

// IsReasoningModel 判断是否为推理模型
func (a *AnthropicAdapter) IsReasoningModel(model string) bool {
	return false
}

// AccumulateStreamData 累积流式数据块（向后兼容）
func (a *AnthropicAdapter) AccumulateStreamData(streamData map[string]interface{}, chunkData map[string]interface{}) bool {
	// 初始化
	if _, exists := streamData["content"]; !exists {
		streamData["content"] = ""
		streamData["finish_reason"] = ""
		streamData["tool_calls"] = []map[string]interface{}{}
	}

	// 检查事件类型
	eventType, _ := chunkData["type"].(string)
	switch eventType {
	case "content_block_delta":
		if delta, ok := chunkData["delta"].(map[string]interface{}); ok {
			if text, ok := delta["text"].(string); ok {
				streamData["content"] = streamData["content"].(string) + text
			}
		}
	case "message_delta":
		if delta, ok := chunkData["delta"].(map[string]interface{}); ok {
			if stopReason, ok := delta["stop_reason"].(string); ok {
				streamData["finish_reason"] = stopReason
				return stopReason != ""
			}
		}
	case "content_block_stop", "message_stop":
		return true
	}

	return false
}

// GetAPIPath 获取默认 API 路径
func (a *AnthropicAdapter) GetAPIPath() string {
	return "/v1/messages"
}

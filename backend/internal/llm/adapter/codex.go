package adapter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// CodexAdapter Codex (OpenAI Responses API) 协议适配器
// Codex 使用 Responses API 格式，事件驱动的 SSE 流式响应
// 关键区别：使用 input/output 替代 messages，事件类型为 response.xxx
type CodexAdapter struct{}

const codexResponseOutputItemsKey = "response_output_items"

// Name 返回适配器名称
func (a *CodexAdapter) Name() string {
	return "codex"
}

// CodexStreamState Codex 流式状态管理
// 事件驱动，需要跟踪 output_item 和 reasoning_summary
type CodexStreamState struct {
	ResponseID   string
	Model        string
	Content      strings.Builder
	Reasoning    strings.Builder
	ToolCalls    map[int]*CodexToolCall // index -> tool call
	FinishReason string
	Usage        map[string]int64

	// 追踪当前 output item
	CurrentItemIndex   int
	CurrentItemType    string // "message", "function_call", "reasoning"
	CurrentItemStarted bool

	// 追踪 reasoning summary
	SummaryIndex   int
	SummaryStarted bool
	SummaryContent strings.Builder
}

// CodexToolCall 工具调用状态
type CodexToolCall struct {
	CallID    string
	Name      string
	Arguments strings.Builder
}

var validCodexReasoningEfforts = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
}

// NewCodexStreamState 创建新的 Codex 流式状态
func NewCodexStreamState() *CodexStreamState {
	return &CodexStreamState{
		ToolCalls: make(map[int]*CodexToolCall),
		Usage:     make(map[string]int64),
	}
}

// BuildRequest 构建请求体
// Codex Responses API 使用 input 字段替代 messages
// 需要将 OpenAI 格式的 messages 转换为 Codex 格式的 input 数组
func (a *CodexAdapter) BuildRequest(config RequestConfig) map[string]interface{} {
	// Responses API prefers system/developer guidance in top-level instructions.
	instructions, input := a.buildCodexInstructionsAndInput(config.Messages)

	request := map[string]interface{}{
		"model":  config.Model,
		"input":  input, // Codex 使用 input 数组格式
		"stream": config.Stream,
	}
	if instructions != "" {
		request["instructions"] = instructions
	}

	// 设置 max_output_tokens (Codex 使用 max_output_tokens 而非 max_tokens)
	if config.MaxTokens > 0 {
		request["max_output_tokens"] = config.MaxTokens
	} else {
		request["max_output_tokens"] = 4096
	}

	effort := NormalizeCodexReasoningEffort(config.ReasoningEffort)
	if !IsValidCodexReasoningEffort(effort) && config.Thinking != nil {
		effort = NormalizeCodexReasoningEffort(mapAnthropicThinkingToReasoningEffort(config.Model, config.Thinking))
	}
	if IsValidCodexReasoningEffort(effort) {
		request["reasoning"] = map[string]interface{}{
			"effort":  effort,
			"summary": "auto",
		}
	}

	// 添加 Function Call
	if config.Functions != nil {
		tools := mergeCodexTools(config.Functions, BuildMCPMetaTools())
		if len(tools) > 0 {
			request["tools"] = tools
			request["tool_choice"] = "auto"
		}
	}

	return request
}

func (a *CodexAdapter) buildCodexInstructionsAndInput(messages []map[string]interface{}) (string, []map[string]interface{}) {
	if len(messages) == 0 {
		return "", nil
	}

	instructionParts := make([]string, 0, 2)
	inputMessages := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		if outputItems := decodeSliceOfMaps(msg[codexResponseOutputItemsKey]); len(outputItems) > 0 {
			inputMessages = append(inputMessages, msg)
			continue
		}

		role, _ := msg["role"].(string)
		switch role {
		case "system", "developer":
			if content := extractCodexMessageText(msg); content != "" {
				instructionParts = append(instructionParts, content)
			}
		default:
			inputMessages = append(inputMessages, msg)
		}
	}

	return strings.Join(instructionParts, "\n\n"), a.convertMessagesToCodexInput(inputMessages)
}

// NormalizeCodexReasoningEffort 规范化 Codex reasoning effort。
func NormalizeCodexReasoningEffort(effort string) string {
	return strings.ToLower(strings.TrimSpace(effort))
}

// IsValidCodexReasoningEffort 判断是否为支持的 Codex reasoning effort。
func IsValidCodexReasoningEffort(effort string) bool {
	_, ok := validCodexReasoningEfforts[NormalizeCodexReasoningEffort(effort)]
	return ok
}

// convertMessagesToCodexInput 将 OpenAI 格式的 messages 转换为 Codex 格式的 input 数组。
// system/developer messages are handled as top-level instructions in BuildRequest.
// OpenAI 格式: {"role": "user/assistant/tool", "content": "...", "tool_calls": [...]}
// Codex 格式: {"type": "message", "role": "user/assistant", "content": [...]}
//
//	或 {"type": "function_call", "call_id": "...", "name": "...", "arguments": "..."}
//	或 {"type": "function_call_output", "call_id": "...", "output": "..."}
func (a *CodexAdapter) convertMessagesToCodexInput(messages []map[string]interface{}) []map[string]interface{} {
	input := make([]map[string]interface{}, 0, len(messages))

	for _, msg := range messages {
		if outputItems := decodeSliceOfMaps(msg[codexResponseOutputItemsKey]); len(outputItems) > 0 {
			input = append(input, outputItems...)
			continue
		}

		role, _ := msg["role"].(string)

		switch role {
		case "user", "assistant":
			content := extractCodexMessageText(msg)
			reasoning := extractCodexReasoningText(msg)
			if role == "assistant" {
				if reasoning != "" {
					input = append(input, buildCodexReasoningItem(reasoning))
				}
				if content != "" {
					input = append(input, buildCodexAssistantMessageItem(content))
				}
			} else {
				inputItem := map[string]interface{}{
					"type": "message",
					"role": role,
				}
				if content != "" {
					inputItem["content"] = []map[string]interface{}{
						{
							"type": "input_text",
							"text": content,
						},
					}
				} else {
					inputItem["content"] = []map[string]interface{}{}
				}
				input = append(input, inputItem)
			}

			// 处理 assistant 消息中的 tool_calls，展开为独立的 function_call input item
			if role == "assistant" {
				if toolCallsRaw := decodeSliceOfMaps(msg["tool_calls"]); len(toolCallsRaw) > 0 {
					for _, tc := range toolCallsRaw {
						if item := buildCodexFunctionCallItem(tc); item != nil {
							input = append(input, item)
						}
					}
				}
			}

		case "tool":
			// 处理工具调用结果消息
			toolCallID, _ := msg["tool_call_id"].(string)
			content, _ := msg["content"].(string)

			input = append(input, map[string]interface{}{
				"type":    "function_call_output",
				"call_id": toolCallID,
				"output":  content,
			})
		}
	}

	return input
}

// BuildHeaders 构建请求头
func (a *CodexAdapter) BuildHeaders(cfg AdapterConfig) map[string]string {
	return mergeHeaderMaps(map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + cfg.APIKey,
	}, cfg.Headers)
}

// GetAPIPath 获取默认 API 路径
// Codex Responses API 路径
func (a *CodexAdapter) GetAPIPath() string {
	return "/v1/responses"
}

// GetDefaultModel 获取默认模型
func (a *CodexAdapter) GetDefaultModel() string {
	return "gpt-4.1"
}

// IsReasoningModel 判断是否为推理模型
func (a *CodexAdapter) IsReasoningModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "o1") ||
		strings.Contains(model, "o3") ||
		strings.Contains(model, "gpt-4.1")
}

// HandleResponse 处理完整响应（流式或非流式）
func (a *CodexAdapter) HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	rawBody, err := io.ReadAll(respBody)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 某些 Codex 网关在 stream=false 时仍会返回 SSE；这里按响应体内容自适应解析。
	if isStream || looksLikeCodexSSEResponse(rawBody) {
		result, err := a.handleCodexStreamResponse(bytes.NewReader(rawBody), onContent)
		if err != nil {
			return nil, err
		}
		procResult := a.ProcessResponse(result)
		return a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), nil
	}
	result, err := a.handleCodexNonStreamResponse(bytes.NewReader(rawBody), onContent)
	if err != nil {
		return nil, err
	}
	procResult := a.ProcessResponse(result)
	return a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), nil
}

func looksLikeCodexSSEResponse(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	return strings.HasPrefix(trimmed, "event: ") || strings.HasPrefix(trimmed, "data: ")
}

// handleCodexStreamResponse 处理 Codex 流式响应
// Codex SSE 格式：
//
//	event: response.created
//	data: {"type":"response.created","response":{"id":"resp_xxx","model":"gpt-4.1","status":"in_progress"}}
//
//	event: response.output_text.delta
//	data: {"type":"response.output_text.delta","delta":"Hello"}
//
//	event: response.completed
//	data: {"type":"response.completed","response":{"id":"resp_xxx","status":"completed","stop_reason":"end_turn"}}
func (a *CodexAdapter) handleCodexStreamResponse(respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	state := NewCodexStreamState()

	scanner := bufio.NewScanner(respBody)
	buf := make([]byte, 0, 1024*1024)
	scanner.Buffer(buf, 20*1024*1024) // 最大 20MB

	var currentEvent string

	for scanner.Scan() {
		line := scanner.Text()

		// 解析 SSE 格式
		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			continue
		}

		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "" {
				continue
			}

			var event map[string]interface{}
			if err := json.Unmarshal([]byte(data), &event); err != nil {
				continue
			}

			a.processCodexEvent(state, currentEvent, event, onContent)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("读取流式响应失败: %w", err)
	}

	// 转换为最终结果
	return state.ToMap(), nil
}

// processCodexEvent 处理单个 Codex 事件
func (a *CodexAdapter) processCodexEvent(state *CodexStreamState, eventType string, event map[string]interface{}, onContent func(string)) {
	// 获取事件类型
	if eventType == "" {
		if t, ok := event["type"].(string); ok {
			eventType = t
		}
	}

	switch eventType {
	case "response.created":
		a.handleResponseCreated(state, event)

	case "response.output_item.added":
		a.handleOutputItemAdded(state, event)

	case "response.output_text.delta":
		a.handleOutputTextDelta(state, event, onContent)

	case "response.function_call_arguments.delta":
		a.handleFunctionCallArgumentsDelta(state, event)

	case "response.reasoning_summary_part.added":
		a.handleReasoningSummaryPartAdded(state, event)

	case "response.reasoning_summary_text.delta":
		a.handleReasoningSummaryTextDelta(state, event)

	case "response.reasoning_summary_part.done":
		// 推理块结束，无需特殊处理

	case "response.reasoning_text.delta":
		a.handleReasoningTextDelta(state, event)

	case "response.output_item.done":
		a.handleOutputItemDone(state, event)

	case "response.completed":
		a.handleResponseCompleted(state, event)

	case "response.failed":
		a.handleResponseFailed(state, event)

	case "response.incomplete":
		a.handleResponseIncomplete(state, event)

	case "response.done":
		// 增量响应结束信号
		if usage, ok := event["usage"].(map[string]interface{}); ok {
			if inputTokens, ok := usage["input_tokens"].(float64); ok {
				state.Usage["input_tokens"] = int64(inputTokens)
			}
			if outputTokens, ok := usage["output_tokens"].(float64); ok {
				state.Usage["output_tokens"] = int64(outputTokens)
			}
		}
	}
}

// handleResponseCreated 处理 response.created 事件
func (a *CodexAdapter) handleResponseCreated(state *CodexStreamState, event map[string]interface{}) {
	if resp, ok := event["response"].(map[string]interface{}); ok {
		if id, ok := resp["id"].(string); ok {
			state.ResponseID = id
		}
		if model, ok := resp["model"].(string); ok {
			state.Model = model
		}
	}
}

// handleOutputItemAdded 处理 response.output_item.added 事件
func (a *CodexAdapter) handleOutputItemAdded(state *CodexStreamState, event map[string]interface{}) {
	index := getIntIndex(event)
	if index == -1 {
		return
	}

	item, ok := event["item"].(map[string]interface{})
	if !ok {
		return
	}

	itemType, _ := item["type"].(string)
	state.CurrentItemIndex = index
	state.CurrentItemType = itemType
	state.CurrentItemStarted = true

	// 如果是 function_call，初始化 ToolCall
	if itemType == "function_call" {
		tc := &CodexToolCall{}
		if callID, ok := item["call_id"].(string); ok {
			tc.CallID = callID
		}
		if name, ok := item["name"].(string); ok {
			tc.Name = name
		}
		state.ToolCalls[index] = tc
	}
}

// handleOutputTextDelta 处理 response.output_text.delta 事件
func (a *CodexAdapter) handleOutputTextDelta(state *CodexStreamState, event map[string]interface{}, onContent func(string)) {
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Content.WriteString(delta)
	if onContent != nil {
		onContent(delta)
	}
}

// handleFunctionCallArgumentsDelta 处理 response.function_call_arguments.delta 事件
func (a *CodexAdapter) handleFunctionCallArgumentsDelta(state *CodexStreamState, event map[string]interface{}) {
	index := getIntIndex(event)
	if index == -1 {
		return
	}

	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	tc, exists := state.ToolCalls[index]
	if !exists {
		tc = &CodexToolCall{}
		state.ToolCalls[index] = tc
	}
	tc.Arguments.WriteString(delta)
}

// handleReasoningSummaryPartAdded 处理 response.reasoning_summary_part.added 事件
func (a *CodexAdapter) handleReasoningSummaryPartAdded(state *CodexStreamState, event map[string]interface{}) {
	// 注意：使用 summary_index 而非 index
	summaryIndex := getSummaryIndex(event)
	state.SummaryIndex = summaryIndex
	state.SummaryStarted = true
}

// handleReasoningSummaryTextDelta 处理 response.reasoning_summary_text.delta 事件
func (a *CodexAdapter) handleReasoningSummaryTextDelta(state *CodexStreamState, event map[string]interface{}) {
	// 注意：使用 summary_index 而非 index
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Reasoning.WriteString(delta)
	state.SummaryContent.WriteString(delta)
}

// handleReasoningTextDelta 处理 response.reasoning_text.delta 事件
func (a *CodexAdapter) handleReasoningTextDelta(state *CodexStreamState, event map[string]interface{}) {
	// 注意：使用 content_index 而非 index
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Reasoning.WriteString(delta)
}

// handleOutputItemDone 处理 response.output_item.done 事件
func (a *CodexAdapter) handleOutputItemDone(state *CodexStreamState, event map[string]interface{}) {
	index := getIntIndex(event)
	if index == -1 {
		return
	}

	item, _ := event["item"].(map[string]interface{})
	if itemType, _ := item["type"].(string); itemType == "function_call" {
		tc, exists := state.ToolCalls[index]
		if !exists {
			tc = &CodexToolCall{}
			state.ToolCalls[index] = tc
		}
		if callID, ok := item["call_id"].(string); ok && tc.CallID == "" {
			tc.CallID = callID
		}
		if name, ok := item["name"].(string); ok && tc.Name == "" {
			tc.Name = name
		}
		if args, ok := item["arguments"].(string); ok && tc.Arguments.Len() == 0 {
			tc.Arguments.WriteString(args)
		}
	}

	// 可以在这里验证完整的 output item
	state.CurrentItemStarted = false
}

// handleResponseCompleted 处理 response.completed 事件
func (a *CodexAdapter) handleResponseCompleted(state *CodexStreamState, event map[string]interface{}) {
	if resp, ok := event["response"].(map[string]interface{}); ok {
		if stopReason, ok := resp["stop_reason"].(string); ok {
			state.FinishReason = stopReason
		}
		if usage, ok := resp["usage"].(map[string]interface{}); ok {
			if inputTokens, ok := usage["input_tokens"].(float64); ok {
				state.Usage["input_tokens"] = int64(inputTokens)
			}
			if outputTokens, ok := usage["output_tokens"].(float64); ok {
				state.Usage["output_tokens"] = int64(outputTokens)
			}
			if totalTokens, ok := usage["total_tokens"].(float64); ok {
				state.Usage["total_tokens"] = int64(totalTokens)
			}
		}
	}
}

// handleResponseFailed 处理 response.failed 事件
func (a *CodexAdapter) handleResponseFailed(state *CodexStreamState, event map[string]interface{}) {
	state.FinishReason = "failed"
}

// handleResponseIncomplete 处理 response.incomplete 事件
func (a *CodexAdapter) handleResponseIncomplete(state *CodexStreamState, event map[string]interface{}) {
	state.FinishReason = "incomplete"
}

// handleCodexNonStreamResponse 处理非流式响应
func (a *CodexAdapter) handleCodexNonStreamResponse(respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	var resp map[string]interface{}
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}

	result := make(map[string]interface{})

	// 提取基本信息
	if id, ok := resp["id"].(string); ok {
		result["id"] = id
	}
	if model, ok := resp["model"].(string); ok {
		result["model"] = model
	}
	if stopReason, ok := resp["stop_reason"].(string); ok {
		result["finish_reason"] = stopReason
	}

	// 提取 usage
	if usage, ok := resp["usage"].(map[string]interface{}); ok {
		result["usage"] = usage
	}

	// 处理 output 数组
	output, ok := resp["output"].([]interface{})
	if !ok {
		return result, nil
	}

	var content strings.Builder
	var reasoning strings.Builder
	toolCalls := make([]map[string]interface{}, 0)

	for _, item := range output {
		itemMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		itemType, _ := itemMap["type"].(string)

		switch itemType {
		case "message":
			// 提取 message 内容
			if contentArr, ok := itemMap["content"].([]interface{}); ok {
				for _, c := range contentArr {
					if cMap, ok := c.(map[string]interface{}); ok {
						if cType, ok := cMap["type"].(string); ok {
							if cType == "output_text" {
								if text, ok := cMap["text"].(string); ok {
									content.WriteString(text)
									if onContent != nil {
										onContent(text)
									}
								}
							}
						}
					}
				}
			}

		case "function_call":
			// 提取工具调用
			tc := make(map[string]interface{})
			if callID, ok := itemMap["call_id"].(string); ok {
				tc["id"] = callID
			}
			if name, ok := itemMap["name"].(string); ok {
				tc["name"] = name
			}
			if args, ok := itemMap["arguments"].(string); ok {
				tc["arguments"] = args
			}
			toolCalls = append(toolCalls, tc)

		case "reasoning":
			// 提取推理内容
			if summary, ok := itemMap["summary"].([]interface{}); ok {
				for _, s := range summary {
					if sMap, ok := s.(map[string]interface{}); ok {
						if sType, ok := sMap["type"].(string); ok && sType == "summary_text" {
							if text, ok := sMap["text"].(string); ok {
								reasoning.WriteString(text)
							}
						}
					}
				}
			}
		}
	}

	result["content"] = content.String()
	if reasoning.Len() > 0 {
		result["reasoning"] = reasoning.String()
	}
	if len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}

	return result, nil
}

// ToMap 将状态转换为 map
func (s *CodexStreamState) ToMap() map[string]interface{} {
	result := make(map[string]interface{})

	if s.ResponseID != "" {
		result["id"] = s.ResponseID
	}
	if s.Model != "" {
		result["model"] = s.Model
	}

	result["content"] = s.Content.String()

	if s.Reasoning.Len() > 0 {
		result["reasoning"] = s.Reasoning.String()
	}

	if s.FinishReason != "" {
		result["finish_reason"] = s.FinishReason
	}

	if len(s.Usage) > 0 {
		result["usage"] = s.Usage
	}

	// 转换 ToolCalls
	if len(s.ToolCalls) > 0 {
		toolCalls := make([]map[string]interface{}, 0, len(s.ToolCalls))
		for i := 0; i < len(s.ToolCalls)+1; i++ {
			if tc, exists := s.ToolCalls[i]; exists {
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":        tc.CallID,
					"name":      tc.Name,
					"arguments": tc.Arguments.String(),
				})
			}
		}
		if len(toolCalls) > 0 {
			result["tool_calls"] = toolCalls
		}
	}

	return result
}

// getIntIndex 从事件中获取 index 字段
func getIntIndex(event map[string]interface{}) int {
	if index, ok := event["index"].(float64); ok {
		return int(index)
	}
	if outputIndex, ok := event["output_index"].(float64); ok {
		return int(outputIndex)
	}
	return -1
}

func mergeCodexTools(groups ...interface{}) []map[string]interface{} {
	merged := make([]map[string]interface{}, 0)
	seen := make(map[string]struct{})

	for _, group := range groups {
		for _, tool := range normalizeCodexTools(group) {
			if tool == nil {
				continue
			}
			name, _ := tool["name"].(string)
			if name != "" {
				if _, exists := seen[name]; exists {
					continue
				}
				seen[name] = struct{}{}
			}
			merged = append(merged, tool)
		}
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

// getSummaryIndex 从事件中获取 summary_index 字段
func getSummaryIndex(event map[string]interface{}) int {
	if summaryIndex, ok := event["summary_index"].(float64); ok {
		return int(summaryIndex)
	}
	return 0
}

func normalizeCodexTools(raw interface{}) []map[string]interface{} {
	tools := decodeToolsToMaps(raw)
	if len(tools) == 0 {
		return nil
	}

	normalized := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		if tool == nil {
			continue
		}

		// OpenAI 风格: {"type":"function","function":{...}}
		if fnRaw, ok := tool["function"]; ok {
			fn := decodeMap(fnRaw)
			out := map[string]interface{}{
				"type": "function",
			}
			if name, ok := fn["name"]; ok {
				out["name"] = name
			}
			if desc, ok := fn["description"]; ok {
				out["description"] = desc
			}
			if params, ok := fn["parameters"]; ok {
				out["parameters"] = params
			}
			if _, ok := out["parameters"]; !ok {
				out["parameters"] = map[string]interface{}{
					"type": "object",
				}
			}
			if strict, ok := tool["strict"]; ok {
				out["strict"] = strict
			} else {
				out["strict"] = true
			}
			out = sanitizeCodexTool(out)
			normalized = append(normalized, out)
			continue
		}

		// 已是 Codex 扁平格式
		flat := make(map[string]interface{}, len(tool)+2)
		for key, value := range tool {
			flat[key] = value
		}
		if _, ok := flat["type"]; !ok {
			flat["type"] = "function"
		}
		if _, ok := flat["parameters"]; !ok {
			flat["parameters"] = map[string]interface{}{
				"type": "object",
			}
		}
		if _, ok := flat["strict"]; !ok {
			flat["strict"] = true
		}
		normalized = append(normalized, sanitizeCodexTool(flat))
	}

	return normalized
}

// NormalizeCodexToolsForRequest normalizes tool schemas to the Codex/Responses strict subset.
func NormalizeCodexToolsForRequest(raw interface{}) []map[string]interface{} {
	return normalizeCodexTools(raw)
}

func sanitizeCodexTool(tool map[string]interface{}) map[string]interface{} {
	if tool == nil {
		return nil
	}

	sanitized := make(map[string]interface{}, len(tool))
	for key, value := range tool {
		sanitized[key] = value
	}

	if params := decodeMap(sanitized["parameters"]); params != nil {
		sanitized["parameters"] = sanitizeCodexSchemaMap(params, false)
	}

	return sanitized
}

func sanitizeCodexSchemaMap(schema map[string]interface{}, optional bool) map[string]interface{} {
	if schema == nil {
		return nil
	}

	sanitized := make(map[string]interface{}, len(schema)+2)
	for key, value := range schema {
		if key == "default" {
			continue
		}
		sanitized[key] = value
	}

	for _, key := range []string{"properties", "items", "anyOf", "oneOf", "allOf"} {
		switch key {
		case "properties":
			if props := decodeMapOfMaps(sanitized[key]); len(props) > 0 {
				requiredSet := buildRequiredSet(sanitized["required"])
				names := make([]string, 0, len(props))
				sanitizedProps := make(map[string]interface{}, len(props))
				for name, prop := range props {
					names = append(names, name)
					sanitizedProp := sanitizeCodexSchemaMap(prop, !requiredSet[name])
					sanitizedProps[name] = sanitizedProp
					requiredSet[name] = true
				}
				sort.Strings(names)
				sanitized["properties"] = sanitizedProps
				sanitized["required"] = names
			}
		case "items":
			if items := decodeMap(sanitized[key]); items != nil {
				sanitized["items"] = sanitizeCodexSchemaMap(items, false)
			}
		default:
			if variants := decodeSliceOfMaps(sanitized[key]); len(variants) > 0 {
				out := make([]map[string]interface{}, 0, len(variants))
				for _, variant := range variants {
					out = append(out, sanitizeCodexSchemaMap(variant, false))
				}
				sanitized[key] = out
			}
		}
	}

	if schemaType, ok := sanitized["type"]; ok {
		if typeContains(schemaType, "object") {
			sanitized["additionalProperties"] = false
		}
	}

	if optional {
		makeCodexSchemaNullable(sanitized)
	}

	return sanitized
}

func buildRequiredSet(raw interface{}) map[string]bool {
	result := make(map[string]bool)
	switch typed := raw.(type) {
	case []string:
		for _, name := range typed {
			result[name] = true
		}
	case []interface{}:
		for _, item := range typed {
			if name, ok := item.(string); ok {
				result[name] = true
			}
		}
	}
	return result
}

func makeCodexSchemaNullable(schema map[string]interface{}) {
	if schema == nil {
		return
	}

	if enumValues, ok := schema["enum"].([]interface{}); ok {
		hasNull := false
		for _, value := range enumValues {
			if value == nil {
				hasNull = true
				break
			}
		}
		if !hasNull {
			schema["enum"] = append(enumValues, nil)
		}
	}

	if schemaType, ok := schema["type"]; ok {
		schema["type"] = addNullToSchemaType(schemaType)
		return
	}

	if anyOf := decodeSliceOfMaps(schema["anyOf"]); len(anyOf) > 0 {
		for _, variant := range anyOf {
			if typeContains(variant["type"], "null") {
				return
			}
		}
		schema["anyOf"] = append(anyOf, map[string]interface{}{"type": "null"})
	}
}

func addNullToSchemaType(raw interface{}) interface{} {
	switch typed := raw.(type) {
	case string:
		if typed == "null" {
			return typed
		}
		return []interface{}{typed, "null"}
	case []string:
		out := make([]interface{}, 0, len(typed)+1)
		hasNull := false
		for _, item := range typed {
			out = append(out, item)
			if item == "null" {
				hasNull = true
			}
		}
		if !hasNull {
			out = append(out, "null")
		}
		return out
	case []interface{}:
		hasNull := false
		out := make([]interface{}, 0, len(typed)+1)
		for _, item := range typed {
			out = append(out, item)
			if s, ok := item.(string); ok && s == "null" {
				hasNull = true
			}
		}
		if !hasNull {
			out = append(out, "null")
		}
		return out
	default:
		return raw
	}
}

func typeContains(raw interface{}, target string) bool {
	switch typed := raw.(type) {
	case string:
		return typed == target
	case []string:
		for _, item := range typed {
			if item == target {
				return true
			}
		}
	case []interface{}:
		for _, item := range typed {
			if s, ok := item.(string); ok && s == target {
				return true
			}
		}
	}
	return false
}

func decodeMapOfMaps(raw interface{}) map[string]map[string]interface{} {
	if raw == nil {
		return nil
	}
	if typed, ok := raw.(map[string]interface{}); ok {
		out := make(map[string]map[string]interface{}, len(typed))
		for key, value := range typed {
			if decoded := decodeMap(value); decoded != nil {
				out[key] = decoded
			}
		}
		return out
	}

	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out map[string]map[string]interface{}
	if err := json.Unmarshal(bytes, &out); err != nil {
		return nil
	}
	return out
}

func decodeToolsToMaps(raw interface{}) []map[string]interface{} {
	switch typed := raw.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
				continue
			}
			if decoded := decodeMap(item); decoded != nil {
				out = append(out, decoded)
			}
		}
		return out
	default:
		decoded := decodeSliceOfMaps(raw)
		if len(decoded) > 0 {
			return decoded
		}
		if single := decodeMap(raw); single != nil {
			return []map[string]interface{}{single}
		}
	}
	return nil
}

func decodeSliceOfMaps(raw interface{}) []map[string]interface{} {
	if raw == nil {
		return nil
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out []map[string]interface{}
	if err := json.Unmarshal(bytes, &out); err != nil {
		return nil
	}
	return out
}

func decodeMap(raw interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	if m, ok := raw.(map[string]interface{}); ok {
		return m
	}
	bytes, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	if err := json.Unmarshal(bytes, &out); err != nil {
		return nil
	}
	return out
}

// ExtractResponse 从响应中提取内容
func (a *CodexAdapter) ExtractResponse(result map[string]interface{}) string {
	if content, ok := result["content"].(string); ok {
		return content
	}
	return ""
}

// ExtractReasoning 从响应中提取推理内容
func (a *CodexAdapter) ExtractReasoning(result map[string]interface{}) string {
	if reasoning, ok := result["reasoning"].(string); ok {
		return reasoning
	}
	return ""
}

// ProcessResponse 统一处理响应
func (a *CodexAdapter) ProcessResponse(result map[string]interface{}) ProcessResult {
	pr := ProcessResult{
		Content:   a.ExtractResponse(result),
		Reasoning: a.ExtractReasoning(result),
	}

	if tc, ok := result["tool_calls"].([]map[string]interface{}); ok {
		pr.ToolCalls = tc
		pr.HasToolCalls = len(tc) > 0
	}

	return pr
}

// ExtractStreamContent 从流式响应中提取内容
func (a *CodexAdapter) ExtractStreamContent(result map[string]interface{}) string {
	return a.ExtractResponse(result)
}

// ExtractStreamReasoning 从流式响应中提取推理内容
func (a *CodexAdapter) ExtractStreamReasoning(result map[string]interface{}) string {
	return a.ExtractReasoning(result)
}

// ExtractStreamToolCalls 从流式累积数据中提取 ToolCalls
func (a *CodexAdapter) ExtractStreamToolCalls(streamData map[string]interface{}) []map[string]interface{} {
	if tc, ok := streamData["tool_calls"].([]map[string]interface{}); ok {
		return tc
	}
	return nil
}

// BuildAssistantMessage 构建 assistant 消息
func (a *CodexAdapter) BuildAssistantMessage(content string, toolCalls []map[string]interface{}, reasoning string) map[string]interface{} {
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
	if outputItems := buildCodexResponseOutputItems(content, toolCalls, reasoning); len(outputItems) > 0 {
		msg[codexResponseOutputItemsKey] = outputItems
	}

	return msg
}

func extractCodexReasoningText(msg map[string]interface{}) string {
	if reasoning, ok := msg["reasoning_content"].(string); ok && reasoning != "" {
		return reasoning
	}
	if reasoning, ok := msg["reasoning"].(string); ok && reasoning != "" {
		return reasoning
	}
	return ""
}

func extractCodexMessageText(msg map[string]interface{}) string {
	if msg == nil {
		return ""
	}
	switch typed := msg["content"].(type) {
	case string:
		return strings.TrimSpace(typed)
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, rawPart := range typed {
			part := decodeMap(rawPart)
			if part == nil {
				continue
			}
			text, _ := part["text"].(string)
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			parts = append(parts, text)
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func buildCodexResponseOutputItems(content string, toolCalls []map[string]interface{}, reasoning string) []map[string]interface{} {
	items := make([]map[string]interface{}, 0, len(toolCalls)+2)
	if reasoning != "" {
		items = append(items, buildCodexReasoningItem(reasoning))
	}
	if content != "" {
		items = append(items, buildCodexAssistantMessageItem(content))
	}
	for _, tc := range toolCalls {
		if item := buildCodexFunctionCallItem(tc); item != nil {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil
	}
	return items
}

func buildCodexReasoningItem(reasoning string) map[string]interface{} {
	return map[string]interface{}{
		"type": "reasoning",
		"summary": []map[string]interface{}{
			{
				"type": "summary_text",
				"text": reasoning,
			},
		},
	}
}

func buildCodexAssistantMessageItem(content string) map[string]interface{} {
	return map[string]interface{}{
		"type": "message",
		"role": "assistant",
		"content": []map[string]interface{}{
			{
				"type": "output_text",
				"text": content,
			},
		},
	}
}

func buildCodexFunctionCallItem(raw map[string]interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}

	callID, _ := raw["call_id"].(string)
	if callID == "" {
		callID, _ = raw["id"].(string)
	}

	name, _ := raw["name"].(string)
	arguments, _ := raw["arguments"].(string)

	if fnObj, ok := raw["function"].(map[string]interface{}); ok {
		if name == "" {
			name, _ = fnObj["name"].(string)
		}
		if arguments == "" {
			arguments, _ = fnObj["arguments"].(string)
		}
	}

	if callID == "" || name == "" {
		return nil
	}

	return map[string]interface{}{
		"type":      "function_call",
		"call_id":   callID,
		"name":      name,
		"arguments": arguments,
	}
}

// ExtractToolCallsFromRawCalls 从已解析的 tool_calls 数组直接构造 ToolCall 列表
func (a *CodexAdapter) ExtractToolCallsFromRawCalls(rawCalls []map[string]interface{}) []ToolCall {
	if len(rawCalls) == 0 {
		return nil
	}

	toolCalls := make([]ToolCall, 0, len(rawCalls))
	for _, raw := range rawCalls {
		tc := ToolCall{
			Type: "function",
		}

		// ID 字段 (call_id 或 id)
		if callID, ok := raw["call_id"].(string); ok {
			tc.ID = callID
		} else if id, ok := raw["id"].(string); ok {
			tc.ID = id
		}

		// Function 和 Args 字段
		var name string
		var argsJSON string

		if nameVal, ok := raw["name"].(string); ok {
			name = nameVal
		}

		if argsStr, ok := raw["arguments"].(string); ok {
			// 直接使用 arguments 字符串（已经是 JSON）
			argsJSON = argsStr
		} else if argsMap, ok := raw["arguments"].(map[string]interface{}); ok {
			// 如果是 map，序列化为 JSON 字符串
			if jsonBytes, err := json.Marshal(argsMap); err == nil {
				argsJSON = string(jsonBytes)
			}
		}

		tc.Function = ToolCallFunction{
			Name:      name,
			Arguments: argsJSON,
		}

		toolCalls = append(toolCalls, tc)
	}

	return toolCalls
}

// AccumulateStreamData 累积流式数据块
func (a *CodexAdapter) AccumulateStreamData(streamData map[string]interface{}, chunkData map[string]interface{}) bool {
	// Codex 使用事件驱动，不使用传统的累积方式
	// 这里保留接口兼容性
	return false
}

// IsCodexModel 判断是否为 Codex 模型
func IsCodexModel(model string) bool {
	model = strings.ToLower(model)
	return strings.Contains(model, "code-") ||
		strings.Contains(model, "codex") ||
		strings.Contains(model, "gpt-4.1") ||
		strings.Contains(model, "o1") ||
		strings.Contains(model, "o3")
}

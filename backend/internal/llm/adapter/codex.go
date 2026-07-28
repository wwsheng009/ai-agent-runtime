package adapter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"

	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// CodexAdapter Codex (OpenAI Responses API) 协议适配器
// Codex 使用 Responses API 格式，事件驱动的 SSE 流式响应
// 关键区别：使用 input/output 替代 messages，事件类型为 response.xxx
type CodexAdapter struct{}

const codexResponseOutputItemsKey = "response_output_items"
const (
	codexPromptCacheKeyMetadataKey = "prompt_cache_key"
	codexSessionIDMetadataKey      = "session_id"
	codexConversationIDMetadataKey = "conversation_id"
	codexServiceTierMetadataKey    = "service_tier"
	// codexServiceTierPriority is the request body value for Fast mode.
	codexServiceTierPriority                = "priority"
	codexServiceTierDefault                 = "default"
	codexServiceTierFast                    = "fast"
	codexImageGenerationToolType            = "image_generation"
	codexImageGenerationCallType            = "image_generation_call"
	codexSupportsMaxOutputTokensMetadataKey = "supports_max_output_tokens"
	codexToolSchemaCompactBytes             = 5000
	codexToolSchemaCompactDepth             = 3
)

// Name 返回适配器名称
func (a *CodexAdapter) Name() string {
	return "codex"
}

// CodexStreamState Codex 流式状态管理
// 事件驱动，需要跟踪 output_item 和 reasoning_summary
type CodexStreamState struct {
	ResponseID    string
	Model         string
	Content       strings.Builder
	Reasoning     strings.Builder
	Refusal       strings.Builder
	ToolCalls     map[int]*CodexToolCall // index -> tool call
	ToolItemKeys  map[string]int
	OutputItems   map[int]map[string]interface{}
	FinishReason  string
	Usage         map[string]int64
	ErrorCode     string
	ErrorMessage  string
	Annotations   []map[string]interface{}
	UnknownEvents map[string]int
	ImagePhases   map[string]map[string]struct{}

	// 追踪当前 output item
	CurrentItemIndex   int
	CurrentItemType    string // "message", "function_call", "reasoning"
	CurrentItemStarted bool
	NextSyntheticIndex int

	// 追踪 reasoning summary
	SummaryIndex   int
	SummaryStarted bool
	SummaryContent strings.Builder
}

// CodexToolCall 工具调用状态
type CodexToolCall struct {
	CallID    string
	Name      string
	Kind      string
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
		ToolCalls:          make(map[int]*CodexToolCall),
		ToolItemKeys:       make(map[string]int),
		OutputItems:        make(map[int]map[string]interface{}),
		Usage:              make(map[string]int64),
		UnknownEvents:      make(map[string]int),
		ImagePhases:        make(map[string]map[string]struct{}),
		CurrentItemIndex:   -1,
		NextSyntheticIndex: 1000000,
	}
}

// BuildRequest 构建请求体
// Codex Responses API 使用 input 字段替代 messages
// 需要将 OpenAI 格式的 messages 转换为 Codex 格式的 input 数组
func (a *CodexAdapter) BuildRequest(config RequestConfig) map[string]interface{} {
	// Responses API prefers system/developer guidance in top-level instructions.
	instructions, input := a.buildCodexInstructionsAndInput(config.Messages)
	promptCacheKey := a.resolvePromptCacheKey(config)

	request := map[string]interface{}{
		"model":  config.Model,
		"input":  input, // Codex 使用 input 数组格式
		"stream": config.Stream,
		// This adapter replays full conversation state via input/prompt_cache_key,
		// so requests should stay stateless across Codex-compatible upstreams.
		"store": false,
	}
	if instructions != "" {
		request["instructions"] = instructions
	}
	if promptCacheKey != "" {
		request["prompt_cache_key"] = promptCacheKey
	}
	if serviceTier := resolveCodexServiceTier(config.Metadata); serviceTier != "" {
		request["service_tier"] = serviceTier
	}

	// 设置 max_output_tokens（仅对支持该字段的 Codex 兼容上游发送）
	if metadataAllowsCodexMaxOutputTokens(config.Metadata) {
		if config.MaxTokens > 0 {
			request["max_output_tokens"] = config.MaxTokens
		} else {
			request["max_output_tokens"] = 4096
		}
	}

	effort := NormalizeCodexReasoningEffort(config.ReasoningEffort)
	if IsValidCodexReasoningEffort(effort) {
		request["reasoning"] = map[string]interface{}{
			"effort":  effort,
			"summary": "auto",
		}
		request["include"] = []string{"reasoning.encrypted_content"}
	}

	// 添加 Function Call
	if config.Functions != nil {
		// Functions already contains the session-frozen, protocol-converted tool
		// surface. Do not inject, filter, or reorder tools again in the adapter.
		tools := mergeCodexTools(config.Functions)
		if len(tools) > 0 {
			request["tools"] = tools
			if config.ToolChoice != nil {
				request["tool_choice"] = config.ToolChoice
			} else {
				request["tool_choice"] = "auto"
			}
			if parallel, ok := requestMetadataBool(config.Metadata, "parallel_tool_calls"); ok {
				request["parallel_tool_calls"] = parallel
			}
			if codexToolsContainType(tools, codexImageGenerationToolType) {
				request["parallel_tool_calls"] = false
			}
		}
	}

	return request
}

func (a *CodexAdapter) resolvePromptCacheKey(config RequestConfig) string {
	if key := strings.TrimSpace(stringFromMetadata(config.Metadata, codexPromptCacheKeyMetadataKey)); key != "" {
		return key
	}
	if key := strings.TrimSpace(stringFromMetadata(config.Metadata, codexSessionIDMetadataKey)); key != "" {
		return key
	}
	if key := strings.TrimSpace(stringFromMetadata(config.Metadata, codexConversationIDMetadataKey)); key != "" {
		return key
	}
	return ""
}

// resolveCodexServiceTier maps metadata service_tier to the Responses API value.
// Accepted inputs: "priority" / "fast" → "priority" (Fast mode); "default"/empty → omit.
func resolveCodexServiceTier(metadata map[string]interface{}) string {
	raw := strings.ToLower(strings.TrimSpace(stringFromMetadata(metadata, codexServiceTierMetadataKey)))
	switch raw {
	case "", codexServiceTierDefault:
		return ""
	case codexServiceTierPriority, codexServiceTierFast:
		return codexServiceTierPriority
	default:
		// Pass through unknown tiers (e.g. "flex") for forward compatibility.
		return raw
	}
}

func stringFromMetadata(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	case json.RawMessage:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func metadataAllowsCodexMaxOutputTokens(metadata map[string]interface{}) bool {
	if len(metadata) == 0 {
		return true
	}
	value, ok := metadata[codexSupportsMaxOutputTokensMetadataKey]
	if !ok || value == nil {
		return true
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		switch strings.ToLower(strings.TrimSpace(typed)) {
		case "false", "0", "no", "off":
			return false
		case "true", "1", "yes", "on":
			return true
		}
	}
	return true
}

func (a *CodexAdapter) buildCodexInstructionsAndInput(messages []map[string]interface{}) (string, []map[string]interface{}) {
	if len(messages) == 0 {
		return "", nil
	}

	// Only the leading system/developer prefix becomes top-level instructions.
	// Later developer/system turn-context messages must stay in input so goal /
	// recall / todo snapshots can grow append-only without rewriting the
	// provider instructions prefix used for prompt caching.
	instructionParts := make([]string, 0, 2)
	inputMessages := make([]map[string]interface{}, 0, len(messages))
	inLeadingInstructions := true

	for _, msg := range messages {
		if outputItems := decodeSliceOfMaps(msg[codexResponseOutputItemsKey]); len(outputItems) > 0 {
			inLeadingInstructions = false
			inputMessages = append(inputMessages, msg)
			continue
		}

		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))
		switch role {
		case "system", "developer":
			if inLeadingInstructions {
				if content := extractCodexMessageText(msg); content != "" {
					instructionParts = append(instructionParts, content)
				}
				continue
			}
			inputMessages = append(inputMessages, msg)
		default:
			inLeadingInstructions = false
			inputMessages = append(inputMessages, msg)
		}
	}

	return strings.Join(instructionParts, "\n\n"), a.convertMessagesToCodexInput(inputMessages)
}

// NormalizeCodexReasoningEffort 仅去除 Codex reasoning effort 的首尾空白。
func NormalizeCodexReasoningEffort(effort string) string {
	return strings.TrimSpace(effort)
}

// IsValidCodexReasoningEffort 判断是否为支持的 Codex reasoning effort。
func IsValidCodexReasoningEffort(effort string) bool {
	_, ok := validCodexReasoningEfforts[NormalizeCodexReasoningEffort(effort)]
	return ok
}

// convertMessagesToCodexInput 将 OpenAI 格式的 messages 转换为 Codex 格式的 input 数组。
// Leading system/developer messages are handled as top-level instructions in
// BuildRequest. Later system/developer turn-context messages stay in input.
// OpenAI 格式: {"role": "user/assistant/tool/developer", "content": "...", "tool_calls": [...]}
// Codex 格式: {"type": "message", "role": "user/assistant/developer", "content": [...]}
//
//	或 {"type": "function_call", "call_id": "...", "name": "...", "arguments": "..."}
//	或 {"type": "function_call_output", "call_id": "...", "output": "..."}
func (a *CodexAdapter) convertMessagesToCodexInput(messages []map[string]interface{}) []map[string]interface{} {
	input := make([]map[string]interface{}, 0, len(messages))
	toolCallKinds := make(map[string]string)

	for _, msg := range messages {
		if outputItems := decodeSliceOfMaps(msg[codexResponseOutputItemsKey]); len(outputItems) > 0 {
			registerCodexToolKindsFromOutputItems(toolCallKinds, outputItems)
			input = append(input, outputItems...)
			continue
		}

		role, _ := msg["role"].(string)
		role = strings.ToLower(strings.TrimSpace(role))

		switch role {
		case "user", "assistant", "developer", "system":
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
				// Codex input accepts developer messages. Map residual system
				// turn-context items to developer so they stay in the append-only
				// conversation stream instead of top-level instructions.
				inputRole := role
				if role == "system" {
					inputRole = "developer"
				}
				inputItem := map[string]interface{}{
					"type": "message",
					"role": inputRole,
				}
				if role == "user" {
					if parts := extractCodexUserInputParts(msg); len(parts) > 0 {
						inputItem["content"] = parts
					} else if content != "" {
						inputItem["content"] = []map[string]interface{}{{
							"type": "input_text",
							"text": content,
						}}
					} else {
						inputItem["content"] = []map[string]interface{}{}
					}
				} else if content != "" {
					inputItem["content"] = []map[string]interface{}{{
						"type": "input_text",
						"text": content,
					}}
				} else {
					// Skip empty residual instruction messages.
					continue
				}
				input = append(input, inputItem)
			}

			// 处理 assistant 消息中的 tool_calls，展开为独立的 function_call input item
			if role == "assistant" {
				if toolCallsRaw := decodeSliceOfMaps(msg["tool_calls"]); len(toolCallsRaw) > 0 {
					for _, tc := range toolCallsRaw {
						if item := buildCodexFunctionCallItem(tc); item != nil {
							registerCodexToolKind(toolCallKinds, item)
							input = append(input, item)
						}
					}
				}
			}

		case "tool":
			// 处理工具调用结果消息
			toolCallID, _ := msg["tool_call_id"].(string)
			content, _ := msg["content"].(string)
			itemType := "function_call_output"
			if kind := strings.TrimSpace(toolCallKinds[strings.TrimSpace(toolCallID)]); kind == "custom_tool_call" {
				itemType = "custom_tool_call_output"
			}
			item := map[string]interface{}{
				"type":    itemType,
				"call_id": toolCallID,
				"output":  content,
			}
			if itemType == "custom_tool_call_output" {
				if name := codexToolNameForCallID(toolCallKinds, toolCallID); name != "" {
					item["name"] = name
				}
			}
			input = append(input, item)
		}
	}

	return input
}

func registerCodexToolKindsFromOutputItems(kinds map[string]string, items []map[string]interface{}) {
	for _, item := range items {
		registerCodexToolKind(kinds, item)
	}
}

func registerCodexToolKind(kinds map[string]string, item map[string]interface{}) {
	if kinds == nil || len(item) == 0 {
		return
	}
	itemType, _ := item["type"].(string)
	normalizedType := strings.TrimSpace(itemType)
	if normalizedType != "function_call" && normalizedType != "custom_tool_call" {
		return
	}
	callID, _ := item["call_id"].(string)
	if strings.TrimSpace(callID) == "" {
		callID, _ = item["id"].(string)
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	kinds[callID] = normalizedType
	if name, _ := item["name"].(string); strings.TrimSpace(name) != "" {
		kinds[callID+"#name"] = strings.TrimSpace(name)
	}
}

func codexToolNameForCallID(kinds map[string]string, callID string) string {
	if len(kinds) == 0 {
		return ""
	}
	return strings.TrimSpace(kinds[strings.TrimSpace(callID)+"#name"])
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
	return "gpt-5.4"
}

// IsReasoningModel 判断是否为推理模型
func (a *CodexAdapter) IsReasoningModel(model string) bool {
	return looksLikeOpenAIReasoningModel(model) ||
		isReasoningModelPrefix(model) ||
		strings.Contains(strings.ToLower(strings.TrimSpace(model)), "gpt-4.1")
}

// HandleResponse 处理完整响应（流式或非流式）
func (a *CodexAdapter) HandleResponse(isStream bool, respBody io.Reader, callbacks StreamCallbacks) (map[string]interface{}, error) {
	if isStream {
		result, err := a.handleCodexStreamResponse(respBody, callbacks)
		if err != nil {
			return nil, err
		}
		if err := validateCodexToolCalls(result); err != nil {
			return nil, err
		}
		procResult := a.ProcessResponse(result)
		assistantMsg := attachReasoningBlock(a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), procResult.ReasoningBlock)
		attachCodexResultMetadata(assistantMsg, result)
		return assistantMsg, nil
	}

	rawBody, err := io.ReadAll(respBody)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %w", err)
	}

	// 某些 Codex 网关在 stream=false 时仍会返回 SSE；这里按响应体内容自适应解析。
	if looksLikeCodexSSEResponse(rawBody) {
		result, err := a.handleCodexStreamResponse(bytes.NewReader(rawBody), callbacks)
		if err != nil {
			return nil, err
		}
		if err := validateCodexToolCalls(result); err != nil {
			return nil, err
		}
		procResult := a.ProcessResponse(result)
		assistantMsg := attachReasoningBlock(a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), procResult.ReasoningBlock)
		attachCodexResultMetadata(assistantMsg, result)
		return assistantMsg, nil
	}
	result, err := a.handleCodexNonStreamResponse(bytes.NewReader(rawBody), callbacks)
	if err != nil {
		return nil, err
	}
	if err := validateCodexToolCalls(result); err != nil {
		return nil, err
	}
	procResult := a.ProcessResponse(result)
	assistantMsg := attachReasoningBlock(a.BuildAssistantMessage(procResult.Content, procResult.ToolCalls, procResult.Reasoning), procResult.ReasoningBlock)
	attachCodexResultMetadata(assistantMsg, result)
	return assistantMsg, nil
}

func validateCodexToolCalls(result map[string]interface{}) error {
	for index, call := range decodeSliceOfMaps(result["tool_calls"]) {
		kind := strings.ToLower(strings.TrimSpace(asCodexString(call["type"])))
		callID := strings.TrimSpace(asCodexString(call["id"]))
		name := strings.TrimSpace(asCodexString(call["name"]))
		if callID == "" {
			return &codexResponseError{kind: "codex response invalid", code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is missing a call id", index)}
		}
		if name == "" {
			return &codexResponseError{kind: "codex response invalid", code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is missing a name", index)}
		}
		if kind == "custom_tool_call" {
			continue
		}
		arguments := strings.TrimSpace(asCodexString(call["arguments"]))
		if arguments == "" {
			continue
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &decoded); err != nil || decoded == nil {
			return &codexResponseError{
				kind:    "codex response invalid",
				code:    "invalid_tool_arguments",
				message: fmt.Sprintf("tool call %d (%s) has incomplete or non-object JSON arguments", index, name),
			}
		}
	}
	return nil
}

func attachCodexResultMetadata(assistantMsg map[string]interface{}, result map[string]interface{}) {
	if finishReason := strings.TrimSpace(asCodexString(result["finish_reason"])); finishReason != "" {
		assistantMsg["finish_reason"] = finishReason
	}
	metadata := decodeMap(assistantMsg["metadata"])
	if annotations := decodeSliceOfMaps(result["annotations"]); len(annotations) > 0 {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["annotations"] = annotations
	}
	if unknownEvents := decodeMap(result["sse_unknown_events"]); len(unknownEvents) > 0 {
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["sse_unknown_events"] = unknownEvents
	}
	refusal := strings.TrimSpace(asCodexString(result["refusal"]))
	if refusal != "" {
		assistantMsg["refusal"] = refusal
		if metadata == nil {
			metadata = map[string]interface{}{}
		}
		metadata["refusal"] = refusal
		metadata["refused"] = true
	}
	if len(metadata) > 0 {
		assistantMsg["metadata"] = metadata
	}
}

func looksLikeCodexSSEResponse(body []byte) bool {
	trimmed := strings.TrimSpace(string(body))
	trimmed = strings.TrimPrefix(trimmed, "\uFEFF")
	return strings.HasPrefix(trimmed, "event:") || strings.HasPrefix(trimmed, "data:")
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
func (a *CodexAdapter) handleCodexStreamResponse(respBody io.Reader, callbacks StreamCallbacks) (map[string]interface{}, error) {
	state := NewCodexStreamState()
	if err := scanSSEFrames(respBody, func(frame SSEFrame) (bool, error) {
		data := strings.TrimSpace(frame.Data)
		frameEvent := strings.ToLower(strings.TrimSpace(frame.Event))
		if data == "" {
			if isCodexFailureEventType(frameEvent) {
				return false, &codexResponseError{
					kind:    "codex stream error",
					code:    "empty_" + strings.ReplaceAll(frameEvent, ".", "_") + "_event",
					message: "upstream returned an empty " + frameEvent + " event",
				}
			}
			if isCodexDoneEventType(frameEvent) && state.FinishReason == "" {
				state.FinishReason = "stop"
			}
			return true, nil
		}
		if data == "[DONE]" {
			if isCodexFailureEventType(frameEvent) {
				return false, &codexResponseError{kind: "codex stream error", message: data}
			}
			if state.FinishReason == "" {
				state.FinishReason = "stop"
			}
			return false, nil
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			if strings.EqualFold(strings.TrimSpace(frame.Event), "error") {
				return false, &codexResponseError{kind: "codex stream error", message: data}
			}
			return false, fmt.Errorf("malformed_stream_event: event=%s: %w", frame.Event, err)
		}

		eventType := resolveCodexEventType(frame.Event, event)
		a.processCodexEvent(state, eventType, event, callbacks)
		return true, nil
	}); err != nil {
		return state.ToMap(), err
	}

	if err := state.StreamError(); err != nil {
		return state.ToMap(), err
	}

	// 转换为最终结果
	return state.ToMap(), nil
}

// processCodexEvent 处理单个 Codex 事件
func (a *CodexAdapter) processCodexEvent(state *CodexStreamState, eventType string, event map[string]interface{}, callbacks StreamCallbacks) {
	// 获取事件类型
	if eventType == "" {
		if t, ok := event["type"].(string); ok {
			eventType = t
		}
	}

	switch eventType {
	case "response.created", "response.queued", "response.in_progress":
		a.handleResponseCreated(state, event)

	case "response.output_item.added":
		a.handleOutputItemAdded(state, event, callbacks)
	case "response.content_part.added":
		a.handleContentPartAdded(state, event)

	case "response.image_generation_call.partial_image":
		a.handleImageGenerationCallPartialImage(state, event, callbacks)
	case "response.image_generation_call.in_progress":
		a.emitCodexImageProgress(callbacks, state, nil, event, "started")
	case "response.image_generation_call.generating":
		a.emitCodexImageProgress(callbacks, state, nil, event, "generating")
	case "response.image_generation_call.completed":
		a.emitCodexImageProgress(callbacks, state, nil, event, "completed")
	case "response.image_generation_call.failed":
		a.emitCodexImageProgress(callbacks, state, nil, event, "failed")

	case "response.output_text.delta":
		a.handleOutputTextDelta(state, event, callbacks)
	case "response.output_text.done":
		a.handleOutputTextDone(state, event, callbacks)
	case "response.output_text.annotation.added":
		a.handleOutputTextAnnotationAdded(state, event)
	case "response.content_part.done":
		a.handleContentPartDone(state, event, callbacks)
	case "response.refusal.delta":
		a.handleRefusalDelta(state, event, callbacks)
	case "response.refusal.done":
		a.handleRefusalDone(state, event, callbacks)

	case "response.function_call_arguments.delta":
		a.handleFunctionCallArgumentsDelta(state, event)
	case "response.function_call_arguments.done":
		a.handleFunctionCallArgumentsDone(state, event)

	case "response.custom_tool_call_input.delta":
		a.handleCustomToolCallInputDelta(state, event)
	case "response.custom_tool_call_input.done":
		a.handleCustomToolCallInputDone(state, event)

	case "response.reasoning_summary_part.added":
		a.handleReasoningSummaryPartAdded(state, event)

	case "response.reasoning_summary_text.delta":
		a.handleReasoningSummaryTextDelta(state, event, callbacks)
	case "response.reasoning_summary_text.done":
		a.handleReasoningTextDone(state, event, callbacks)

	case "response.reasoning_summary_part.done":
		// 推理块结束，无需特殊处理

	case "response.reasoning_text.delta":
		a.handleReasoningTextDelta(state, event, callbacks)
	case "response.reasoning_text.done":
		a.handleReasoningTextDone(state, event, callbacks)

	case "response.output_item.done":
		a.handleOutputItemDone(state, event, callbacks)

	case "response.completed":
		a.handleResponseCompleted(state, event, callbacks)

	case "response.failed":
		a.recoverCodexResponseSnapshot(state, event, callbacks)
		a.handleResponseFailed(state, event, callbacks)
	case "response.cancelled", "response.canceled":
		a.recoverCodexResponseSnapshot(state, event, callbacks)
		a.handleResponseCancelled(state, event, callbacks)

	case "response.incomplete":
		a.recoverCodexResponseSnapshot(state, event, callbacks)
		a.handleResponseIncomplete(state, event, callbacks)

	case "error":
		a.handleErrorEvent(state, event, callbacks)

	case "done", "response.done":
		a.handleResponseCompleted(state, event, callbacks)

	default:
		state.RecordUnknownEvent(eventType)
	}
}

func resolveCodexEventType(frameEvent string, event map[string]interface{}) string {
	frameType := strings.ToLower(strings.TrimSpace(frameEvent))
	payloadType := strings.ToLower(strings.TrimSpace(asCodexString(event["type"])))
	if isCodexFailureEventType(payloadType) {
		return payloadType
	}
	if frameType == "error" || isCodexFailureEventType(frameType) {
		return frameType
	}
	if payloadType != "" {
		return payloadType
	}
	if codexEventHasErrorPayload(event) {
		return "error"
	}
	if frameType == "message" {
		return ""
	}
	return frameType
}

func isCodexFailureEventType(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "error", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func isCodexDoneEventType(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "done", "response.completed", "response.done":
		return true
	default:
		return false
	}
}

func codexEventHasErrorPayload(event map[string]interface{}) bool {
	raw, exists := event["error"]
	if !exists || raw == nil {
		return false
	}
	switch typed := raw.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return true
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
func (a *CodexAdapter) handleOutputItemAdded(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	item, ok := event["item"].(map[string]interface{})
	if !ok {
		return
	}

	itemType, _ := item["type"].(string)
	index := getIntIndex(event)
	if index == -1 {
		if itemType == "custom_tool_call" {
			key := codexToolItemKeyFromItem(item)
			if key == "" {
				key = codexToolItemKeyFromEvent(event)
			}
			if existing, ok := state.ToolItemKeys[key]; ok {
				index = existing
			} else {
				index = state.NextSyntheticIndex
				state.NextSyntheticIndex++
				if key != "" {
					state.ToolItemKeys[key] = index
				}
			}
		} else if itemType == codexImageGenerationCallType {
			index = state.CurrentItemIndex
			if index == -1 {
				index = state.NextSyntheticIndex
				state.NextSyntheticIndex++
			}
		} else {
			return
		}
	}

	state.CurrentItemIndex = index
	state.CurrentItemType = itemType
	state.CurrentItemStarted = true
	state.OutputItems[index] = cloneInterfaceMap(item)

	// 如果是 function_call，初始化 ToolCall
	if itemType == "function_call" || itemType == "custom_tool_call" {
		tc, exists := state.ToolCalls[index]
		if !exists {
			tc = &CodexToolCall{Kind: itemType}
			state.ToolCalls[index] = tc
		}
		if tc.Kind == "" {
			tc.Kind = itemType
		}
		if callID := strings.TrimSpace(asCodexString(item["call_id"])); tc.CallID == "" {
			tc.CallID = callID
		}
		if name := strings.TrimSpace(asCodexString(item["name"])); tc.Name == "" {
			tc.Name = name
		}
		if itemType == "custom_tool_call" {
			appendMissingCodexText(&tc.Arguments, asCodexString(item["input"]), nil)
		} else {
			appendMissingCodexText(&tc.Arguments, asCodexString(item["arguments"]), nil)
		}
		for _, key := range codexToolItemKeys(item) {
			state.ToolItemKeys[key] = index
		}
		return
	}

	if itemType == codexImageGenerationCallType {
		a.emitCodexImageProgress(callbacks, state, item, event, "started")
	}
}

func (a *CodexAdapter) handleContentPartAdded(state *CodexStreamState, event map[string]interface{}) {
	part := decodeMap(event["part"])
	if part == nil {
		return
	}
	state.AddAnnotations(part["annotations"])
}

// handleOutputTextDelta 处理 response.output_text.delta 事件
func (a *CodexAdapter) handleOutputTextDelta(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Content.WriteString(delta)
	callbacks.EmitText(delta)
}

func (a *CodexAdapter) handleOutputTextDone(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	text := strings.TrimSpace(asCodexString(event["text"]))
	appendMissingCodexText(&state.Content, text, callbacks.EmitText)
}

func (a *CodexAdapter) handleContentPartDone(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	part := decodeMap(event["part"])
	if part == nil {
		return
	}
	state.AddAnnotations(part["annotations"])
	switch strings.ToLower(strings.TrimSpace(asCodexString(part["type"]))) {
	case "output_text":
		appendMissingCodexText(&state.Content, asCodexString(part["text"]), callbacks.EmitText)
	case "refusal":
		refusal := asCodexString(part["refusal"])
		if refusal == "" {
			refusal = asCodexString(part["text"])
		}
		appendMissingCodexText(&state.Refusal, refusal, nil)
		appendMissingCodexText(&state.Content, refusal, callbacks.EmitText)
	}
}

func (a *CodexAdapter) handleOutputTextAnnotationAdded(state *CodexStreamState, event map[string]interface{}) {
	if annotation := decodeMap(event["annotation"]); annotation != nil {
		state.AddAnnotation(annotation)
	}
}

func (a *CodexAdapter) handleRefusalDelta(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	delta := asCodexString(event["delta"])
	if delta == "" {
		return
	}
	state.Refusal.WriteString(delta)
	state.Content.WriteString(delta)
	callbacks.EmitText(delta)
}

func (a *CodexAdapter) handleRefusalDone(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	refusal := asCodexString(event["refusal"])
	if refusal == "" {
		refusal = asCodexString(event["text"])
	}
	appendMissingCodexText(&state.Refusal, refusal, nil)
	appendMissingCodexText(&state.Content, refusal, callbacks.EmitText)
}

// handleFunctionCallArgumentsDelta 处理 response.function_call_arguments.delta 事件
func (a *CodexAdapter) handleFunctionCallArgumentsDelta(state *CodexStreamState, event map[string]interface{}) {
	index := resolveCodexToolIndex(state, event)
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

func (a *CodexAdapter) handleFunctionCallArgumentsDone(state *CodexStreamState, event map[string]interface{}) {
	index := resolveCodexToolIndex(state, event)
	if index == -1 {
		return
	}
	tc, exists := state.ToolCalls[index]
	if !exists {
		tc = &CodexToolCall{Kind: "function_call"}
		state.ToolCalls[index] = tc
	}
	if tc.Kind == "" {
		tc.Kind = "function_call"
	}
	if name := strings.TrimSpace(asCodexString(event["name"])); tc.Name == "" {
		tc.Name = name
	}
	if callID := strings.TrimSpace(asCodexString(event["call_id"])); tc.CallID == "" {
		tc.CallID = callID
	}
	appendMissingCodexText(&tc.Arguments, asCodexString(event["arguments"]), nil)
}

func (a *CodexAdapter) handleCustomToolCallInputDelta(state *CodexStreamState, event map[string]interface{}) {
	key := codexToolItemKeyFromEvent(event)
	if key == "" {
		return
	}
	index, ok := state.ToolItemKeys[key]
	if !ok {
		index = state.NextSyntheticIndex
		state.NextSyntheticIndex++
		state.ToolItemKeys[key] = index
	}

	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	tc, exists := state.ToolCalls[index]
	if !exists {
		tc = &CodexToolCall{Kind: "custom_tool_call"}
		if callID, _ := event["call_id"].(string); strings.TrimSpace(callID) != "" {
			tc.CallID = strings.TrimSpace(callID)
		}
		state.ToolCalls[index] = tc
	}
	if tc.Kind == "" {
		tc.Kind = "custom_tool_call"
	}
	tc.Arguments.WriteString(delta)
}

func (a *CodexAdapter) handleCustomToolCallInputDone(state *CodexStreamState, event map[string]interface{}) {
	key := codexToolItemKeyFromEvent(event)
	if key == "" {
		return
	}
	index, ok := state.ToolItemKeys[key]
	if !ok {
		index = state.NextSyntheticIndex
		state.NextSyntheticIndex++
		state.ToolItemKeys[key] = index
	}
	tc, exists := state.ToolCalls[index]
	if !exists {
		tc = &CodexToolCall{Kind: "custom_tool_call"}
		state.ToolCalls[index] = tc
	}
	if tc.Kind == "" {
		tc.Kind = "custom_tool_call"
	}
	if callID := strings.TrimSpace(asCodexString(event["call_id"])); tc.CallID == "" {
		tc.CallID = callID
	}
	if name := strings.TrimSpace(asCodexString(event["name"])); tc.Name == "" {
		tc.Name = name
	}
	appendMissingCodexText(&tc.Arguments, asCodexString(event["input"]), nil)
}

// handleReasoningSummaryPartAdded 处理 response.reasoning_summary_part.added 事件
func (a *CodexAdapter) handleReasoningSummaryPartAdded(state *CodexStreamState, event map[string]interface{}) {
	// 注意：使用 summary_index 而非 index
	summaryIndex := getSummaryIndex(event)
	state.SummaryIndex = summaryIndex
	state.SummaryStarted = true
}

// handleReasoningSummaryTextDelta 处理 response.reasoning_summary_text.delta 事件
func (a *CodexAdapter) handleReasoningSummaryTextDelta(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	// 注意：使用 summary_index 而非 index
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Reasoning.WriteString(delta)
	state.SummaryContent.WriteString(delta)
	callbacks.EmitReasoning(delta)
}

// handleReasoningTextDelta 处理 response.reasoning_text.delta 事件
func (a *CodexAdapter) handleReasoningTextDelta(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	// 注意：使用 content_index 而非 index
	delta, _ := event["delta"].(string)
	if delta == "" {
		return
	}

	state.Reasoning.WriteString(delta)
	callbacks.EmitReasoning(delta)
}

func (a *CodexAdapter) handleReasoningTextDone(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	text := strings.TrimSpace(asCodexString(event["text"]))
	appendMissingCodexText(&state.Reasoning, text, callbacks.EmitReasoning)
}

func appendMissingCodexText(builder *strings.Builder, authoritative string, emit func(string)) {
	if builder == nil || authoritative == "" {
		return
	}
	current := builder.String()
	if current == authoritative || strings.HasSuffix(current, authoritative) {
		return
	}
	missing := authoritative
	if strings.HasPrefix(authoritative, current) {
		missing = strings.TrimPrefix(authoritative, current)
	}
	if missing == "" {
		return
	}
	builder.WriteString(missing)
	if emit != nil {
		emit(missing)
	}
}

// handleOutputItemDone 处理 response.output_item.done 事件
func (a *CodexAdapter) handleOutputItemDone(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	index := getIntIndex(event)
	item, _ := event["item"].(map[string]interface{})
	itemType, _ := item["type"].(string)
	if index == -1 && (itemType == "function_call" || itemType == "custom_tool_call") {
		key := codexToolItemKeyFromItem(item)
		if key == "" {
			key = codexToolItemKeyFromEvent(event)
		}
		if existing, ok := state.ToolItemKeys[key]; ok {
			index = existing
		} else {
			index = state.NextSyntheticIndex
			state.NextSyntheticIndex++
			if key != "" {
				state.ToolItemKeys[key] = index
			}
		}
	}
	if index == -1 {
		if itemType == codexImageGenerationCallType {
			index = state.CurrentItemIndex
			if index == -1 {
				index = state.NextSyntheticIndex
				state.NextSyntheticIndex++
			}
		}
	}
	if index == -1 {
		return
	}

	if itemType == "function_call" || itemType == "custom_tool_call" {
		tc, exists := state.ToolCalls[index]
		if !exists {
			tc = &CodexToolCall{Kind: itemType}
			state.ToolCalls[index] = tc
		}
		if tc.Kind == "" {
			tc.Kind = itemType
		}
		if callID, ok := item["call_id"].(string); ok && tc.CallID == "" {
			tc.CallID = callID
		}
		if name, ok := item["name"].(string); ok && tc.Name == "" {
			tc.Name = name
		}
		if itemType == "function_call" {
			appendMissingCodexText(&tc.Arguments, asCodexString(item["arguments"]), nil)
		} else {
			appendMissingCodexText(&tc.Arguments, asCodexString(item["input"]), nil)
			for _, key := range codexToolItemKeys(item) {
				state.ToolItemKeys[key] = index
			}
		}
	} else if itemType == "message" {
		a.recoverCodexMessageItem(state, item, callbacks)
	} else if itemType == "reasoning" {
		a.recoverCodexReasoningItem(state, item, callbacks)
	} else if itemType == codexImageGenerationCallType {
		phase := strings.ToLower(strings.TrimSpace(asCodexString(item["status"])))
		switch phase {
		case "failed", "generating":
		case "in_progress", "queued":
			phase = "started"
		default:
			phase = "completed"
		}
		a.emitCodexImageProgress(callbacks, state, item, event, phase)
	}
	state.OutputItems[index] = cloneInterfaceMap(item)

	// 可以在这里验证完整的 output item
	state.CurrentItemStarted = false
}

func (a *CodexAdapter) recoverCodexReasoningItem(state *CodexStreamState, item map[string]interface{}, callbacks StreamCallbacks) {
	for _, part := range decodeSliceOfMaps(item["summary"]) {
		if strings.EqualFold(strings.TrimSpace(asCodexString(part["type"])), "summary_text") {
			appendMissingCodexText(&state.Reasoning, asCodexString(part["text"]), callbacks.EmitReasoning)
		}
	}
	for _, part := range decodeSliceOfMaps(item["content"]) {
		if strings.EqualFold(strings.TrimSpace(asCodexString(part["type"])), "reasoning_text") {
			appendMissingCodexText(&state.Reasoning, asCodexString(part["text"]), callbacks.EmitReasoning)
		}
	}
}

func (a *CodexAdapter) recoverCodexMessageItem(state *CodexStreamState, item map[string]interface{}, callbacks StreamCallbacks) {
	content, _ := item["content"].([]interface{})
	for _, rawPart := range content {
		part := decodeMap(rawPart)
		if part == nil {
			continue
		}
		state.AddAnnotations(part["annotations"])
		switch strings.ToLower(strings.TrimSpace(asCodexString(part["type"]))) {
		case "output_text":
			appendMissingCodexText(&state.Content, asCodexString(part["text"]), callbacks.EmitText)
		case "refusal":
			refusal := asCodexString(part["refusal"])
			if refusal == "" {
				refusal = asCodexString(part["text"])
			}
			appendMissingCodexText(&state.Refusal, refusal, nil)
			appendMissingCodexText(&state.Content, refusal, callbacks.EmitText)
		}
	}
}

// handleResponseCompleted consumes the authoritative response snapshot carried
// by response.completed/response.done. Some compatible gateways omit deltas,
// so final output items must be recoverable from this event alone.
func (a *CodexAdapter) handleResponseCompleted(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	resp := a.recoverCodexResponseSnapshot(state, event, callbacks)

	status := strings.ToLower(strings.TrimSpace(asCodexString(resp["status"])))
	switch status {
	case "failed":
		a.handleResponseFailed(state, resp, callbacks)
		return
	case "cancelled", "canceled":
		a.handleResponseCancelled(state, resp, callbacks)
		return
	case "incomplete":
		a.handleResponseIncomplete(state, resp, callbacks)
		return
	}
	if stopReason := strings.TrimSpace(asCodexString(resp["stop_reason"])); stopReason != "" {
		state.FinishReason = stopReason
	}
	if state.FinishReason == "" {
		state.FinishReason = "stop"
	}
}

func (a *CodexAdapter) recoverCodexResponseSnapshot(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) map[string]interface{} {
	resp := decodeMap(event["response"])
	if resp == nil {
		resp = event
	}
	if id := strings.TrimSpace(asCodexString(resp["id"])); id != "" {
		state.ResponseID = id
	}
	if model := strings.TrimSpace(asCodexString(resp["model"])); model != "" {
		state.Model = model
	}
	if usage := decodeMap(resp["usage"]); usage != nil {
		mergeCodexUsage(state, usage)
	}
	a.recoverCodexFinalOutput(state, decodeSliceOfMaps(resp["output"]), callbacks)
	return resp
}

func (a *CodexAdapter) recoverCodexFinalOutput(state *CodexStreamState, output []map[string]interface{}, callbacks StreamCallbacks) {
	var content strings.Builder
	var refusal strings.Builder
	var reasoning strings.Builder
	for index, item := range output {
		event := map[string]interface{}{"output_index": float64(index), "item": item}
		switch strings.ToLower(strings.TrimSpace(asCodexString(item["type"]))) {
		case "message":
			state.OutputItems[index] = cloneInterfaceMap(item)
			for _, part := range decodeSliceOfMaps(item["content"]) {
				state.AddAnnotations(part["annotations"])
				switch strings.ToLower(strings.TrimSpace(asCodexString(part["type"]))) {
				case "output_text":
					content.WriteString(asCodexString(part["text"]))
				case "refusal":
					text := asCodexString(part["refusal"])
					if text == "" {
						text = asCodexString(part["text"])
					}
					refusal.WriteString(text)
					content.WriteString(text)
				}
			}
		case "reasoning":
			state.OutputItems[index] = cloneInterfaceMap(item)
			for _, part := range decodeSliceOfMaps(item["summary"]) {
				if strings.EqualFold(strings.TrimSpace(asCodexString(part["type"])), "summary_text") {
					reasoning.WriteString(asCodexString(part["text"]))
				}
			}
			for _, part := range decodeSliceOfMaps(item["content"]) {
				if strings.EqualFold(strings.TrimSpace(asCodexString(part["type"])), "reasoning_text") {
					reasoning.WriteString(asCodexString(part["text"]))
				}
			}
		default:
			a.handleOutputItemDone(state, event, callbacks)
		}
	}
	appendMissingCodexText(&state.Content, content.String(), callbacks.EmitText)
	appendMissingCodexText(&state.Refusal, refusal.String(), nil)
	appendMissingCodexText(&state.Reasoning, reasoning.String(), callbacks.EmitReasoning)
	state.CurrentItemStarted = false
}

func mergeCodexUsage(state *CodexStreamState, usage map[string]interface{}) {
	if state == nil {
		return
	}
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens"} {
		if value, ok := numericValue(usage[key]); ok {
			state.Usage[key] = int64(value)
		}
	}
}

// handleResponseFailed 处理 response.failed 事件
func (a *CodexAdapter) handleResponseFailed(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	if a.shouldEmitFailedImageProgress(state) {
		a.emitCodexImageProgress(callbacks, state, nil, event, "failed")
	}
	state.FinishReason = "failed"
	code, message := codexErrorFromEvent(event)
	state.SetError(code, message)
}

func (a *CodexAdapter) handleResponseCancelled(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	if a.shouldEmitFailedImageProgress(state) {
		a.emitCodexImageProgress(callbacks, state, nil, event, "failed")
	}
	state.FinishReason = "failed"
	code, message := codexErrorFromEvent(event)
	if code == "" {
		code = "response_cancelled"
	}
	if message == "" {
		message = "codex response was cancelled before completion"
	}
	state.SetError(code, message)
}

// handleResponseIncomplete 处理 response.incomplete 事件
func (a *CodexAdapter) handleResponseIncomplete(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	if a.shouldEmitFailedImageProgress(state) {
		a.emitCodexImageProgress(callbacks, state, nil, event, "failed")
	}
	if reason := codexIncompleteReason(event); isCodexMaxOutputStop(reason) {
		state.FinishReason = reason
		return
	}
	state.FinishReason = "incomplete"
	code, message := codexIncompleteMessage(event)
	state.SetError(code, message)
}

func (a *CodexAdapter) handleErrorEvent(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	if a.shouldEmitFailedImageProgress(state) {
		a.emitCodexImageProgress(callbacks, state, nil, event, "failed")
	}
	state.FinishReason = "failed"
	code, message := codexErrorFromEvent(event)
	state.SetError(code, message)
}

func (a *CodexAdapter) handleImageGenerationCallPartialImage(state *CodexStreamState, event map[string]interface{}, callbacks StreamCallbacks) {
	if !a.isCurrentImageGenerationCall(state) {
		return
	}
	item := decodeMap(event["item"])
	if item == nil && state != nil {
		item = cloneInterfaceMap(state.OutputItems[state.CurrentItemIndex])
	}
	a.emitCodexImageProgress(callbacks, state, item, event, "partial")
}

func (a *CodexAdapter) emitCodexImageProgress(callbacks StreamCallbacks, state *CodexStreamState, item map[string]interface{}, event map[string]interface{}, phase string) {
	if callbacks.OnImage == nil {
		return
	}

	metadata := map[string]interface{}{
		"phase": strings.TrimSpace(phase),
	}
	if state != nil {
		if state.ResponseID != "" {
			metadata["response_id"] = state.ResponseID
		}
		if index := state.CurrentItemIndex; index >= 0 {
			metadata["output_index"] = index
		}
	}
	if index, ok := numericValue(firstPresentValue(event, "output_index", "item_index")); ok {
		metadata["output_index"] = int(index)
	}

	source := item
	if source == nil {
		source = decodeMap(event)
	}
	if source != nil {
		if value, ok := source["id"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				metadata["image_id"] = value
				metadata["sanitized_id"] = sanitizeGeneratedImageID(value)
			}
		} else if value, ok := source["call_id"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				metadata["image_id"] = value
				metadata["sanitized_id"] = sanitizeGeneratedImageID(value)
			}
		}
		if value, ok := source["item_id"].(string); ok {
			value = strings.TrimSpace(value)
			if value != "" {
				metadata["item_id"] = value
				if _, exists := metadata["image_id"]; !exists {
					metadata["image_id"] = value
					metadata["sanitized_id"] = sanitizeGeneratedImageID(value)
				}
			}
		}
		if prompt := strings.TrimSpace(asCodexString(source["revised_prompt"])); prompt != "" {
			metadata["revised_prompt"] = prompt
		}
		if prompt := strings.TrimSpace(asCodexString(source["prompt"])); prompt != "" && metadata["revised_prompt"] == nil {
			metadata["revised_prompt"] = prompt
		}
	}

	if progress, ok := codexImageProgressValue(event); ok {
		metadata["progress"] = progress
	}
	if errorCode, message := codexErrorFromEvent(event); strings.TrimSpace(message) != "" {
		metadata["error"] = strings.TrimSpace(message)
		if strings.TrimSpace(errorCode) != "" {
			metadata["error_code"] = strings.TrimSpace(errorCode)
		}
	}
	if state != nil && !state.ShouldEmitImagePhase(codexImagePhaseKey(metadata), phase) {
		return
	}

	callbacks.EmitImage(metadata)
}

func codexImagePhaseKey(metadata map[string]interface{}) string {
	if index, ok := numericValue(metadata["output_index"]); ok {
		return fmt.Sprintf("output:%d", int(index))
	}
	for _, key := range []string{"image_id", "item_id", "sanitized_id"} {
		if value := strings.TrimSpace(asCodexString(metadata[key])); value != "" {
			return key + ":" + value
		}
	}
	return "current"
}

func (s *CodexStreamState) ShouldEmitImagePhase(key, phase string) bool {
	if s == nil {
		return true
	}
	phase = strings.ToLower(strings.TrimSpace(phase))
	if phase == "" || phase == "partial" {
		return true
	}
	if s.ImagePhases[key] == nil {
		s.ImagePhases[key] = make(map[string]struct{})
	}
	if _, exists := s.ImagePhases[key][phase]; exists {
		return false
	}
	s.ImagePhases[key][phase] = struct{}{}
	return true
}

func (a *CodexAdapter) isCurrentImageGenerationCall(state *CodexStreamState) bool {
	if state == nil {
		return false
	}
	return state.CurrentItemStarted && strings.EqualFold(strings.TrimSpace(state.CurrentItemType), codexImageGenerationCallType)
}

func sanitizeGeneratedImageID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "generated_image"
	}

	var builder strings.Builder
	for _, ch := range value {
		switch {
		case ch >= 'a' && ch <= 'z':
			builder.WriteRune(ch)
		case ch >= 'A' && ch <= 'Z':
			builder.WriteRune(ch)
		case ch >= '0' && ch <= '9':
			builder.WriteRune(ch)
		case ch == '-' || ch == '_':
			builder.WriteRune(ch)
		default:
			builder.WriteByte('_')
		}
	}

	result := builder.String()
	if result == "" {
		return "generated_image"
	}
	return result
}

func (a *CodexAdapter) shouldEmitFailedImageProgress(state *CodexStreamState) bool {
	return a.isCurrentImageGenerationCall(state)
}

func codexImageProgressValue(event map[string]interface{}) (float64, bool) {
	if len(event) == 0 {
		return 0, false
	}
	if value, ok := numericValue(event["progress"]); ok {
		return clampFloat64(value, 0, 1), true
	}
	if value, ok := numericValue(event["progress_ratio"]); ok {
		return clampFloat64(value, 0, 1), true
	}
	index, indexOK := numericValue(firstPresentValue(event, "index", "output_index", "item_index"))
	total, totalOK := numericValue(firstPresentValue(event, "count", "total", "total_count", "image_count"))
	if indexOK && totalOK && total > 0 {
		return clampFloat64(index/total, 0, 1), true
	}
	if value, ok := numericValue(event["partial_image"]); ok {
		return clampFloat64(value, 0, 1), true
	}
	return 0, false
}

func firstPresentValue(values map[string]interface{}, keys ...string) interface{} {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			return value
		}
	}
	return nil
}

func numericValue(value interface{}) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float32:
		return float64(typed), true
	case float64:
		return typed, true
	case json.Number:
		if parsed, err := typed.Float64(); err == nil {
			return parsed, true
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64); err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func clampFloat64(value, min, max float64) float64 {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}

// handleCodexNonStreamResponse 处理非流式响应
func (a *CodexAdapter) handleCodexNonStreamResponse(respBody io.Reader, callbacks StreamCallbacks) (map[string]interface{}, error) {
	var resp map[string]interface{}
	if err := json.NewDecoder(respBody).Decode(&resp); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	status := strings.ToLower(strings.TrimSpace(asCodexString(resp["status"])))
	incompleteFinishReason := ""
	switch status {
	case "failed":
		code, message := codexErrorFromEvent(resp)
		if message == "" {
			message = "unknown codex response failure"
		}
		return nil, &codexResponseError{kind: "codex response failed", code: code, message: message}
	case "incomplete":
		if reason := codexIncompleteReason(resp); isCodexMaxOutputStop(reason) {
			incompleteFinishReason = reason
			break
		}
		code, message := codexIncompleteMessage(resp)
		if message == "" {
			message = "unknown codex incomplete response"
		}
		return nil, &codexResponseError{kind: "codex response incomplete", code: code, message: message}
	}

	result := make(map[string]interface{})
	if incompleteFinishReason != "" {
		result["finish_reason"] = incompleteFinishReason
	} else if status == "completed" {
		result["finish_reason"] = "stop"
	}

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
	if len(output) > 0 {
		result[codexResponseOutputItemsKey] = output
	}

	var content strings.Builder
	var reasoning strings.Builder
	toolCalls := make([]map[string]interface{}, 0)
	annotations := make([]map[string]interface{}, 0)

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
						annotations = append(annotations, decodeSliceOfMaps(cMap["annotations"])...)
						if cType, ok := cMap["type"].(string); ok {
							if cType == "output_text" {
								if text, ok := cMap["text"].(string); ok {
									content.WriteString(text)
									callbacks.EmitText(text)
								}
							} else if cType == "refusal" {
								refusal := strings.TrimSpace(asCodexString(cMap["refusal"]))
								if refusal == "" {
									refusal = strings.TrimSpace(asCodexString(cMap["text"]))
								}
								if refusal != "" {
									result["refusal"] = refusal
									content.WriteString(refusal)
									callbacks.EmitText(refusal)
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

		case "custom_tool_call":
			tc := map[string]interface{}{
				"type": itemType,
			}
			if callID, ok := itemMap["call_id"].(string); ok {
				tc["id"] = callID
			}
			if name, ok := itemMap["name"].(string); ok {
				tc["name"] = name
			}
			if input, ok := itemMap["input"].(string); ok {
				tc["input"] = input
				tc["arguments"] = input
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
	if len(annotations) > 0 {
		result["annotations"] = annotations
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
	if s.Refusal.Len() > 0 {
		result["refusal"] = s.Refusal.String()
	}

	if s.Reasoning.Len() > 0 {
		result["reasoning"] = s.Reasoning.String()
	}

	if s.FinishReason != "" {
		result["finish_reason"] = s.FinishReason
	}
	if s.ErrorCode != "" {
		result["error_code"] = s.ErrorCode
	}
	if s.ErrorMessage != "" {
		result["error"] = s.ErrorMessage
	}

	if len(s.Usage) > 0 {
		result["usage"] = s.Usage
	}
	if len(s.Annotations) > 0 {
		result["annotations"] = s.Annotations
	}
	if len(s.UnknownEvents) > 0 {
		unknownEvents := make(map[string]interface{}, len(s.UnknownEvents))
		for eventType, count := range s.UnknownEvents {
			unknownEvents[eventType] = count
		}
		result["sse_unknown_events"] = unknownEvents
	}

	// 转换 ToolCalls
	if len(s.ToolCalls) > 0 {
		keys := make([]int, 0, len(s.ToolCalls))
		for index := range s.ToolCalls {
			keys = append(keys, index)
		}
		sort.Ints(keys)

		toolCalls := make([]map[string]interface{}, 0, len(keys))
		for _, index := range keys {
			tc := s.ToolCalls[index]
			call := map[string]interface{}{
				"id":        tc.CallID,
				"name":      tc.Name,
				"arguments": tc.Arguments.String(),
			}
			if strings.TrimSpace(tc.Kind) != "" {
				call["type"] = tc.Kind
			}
			if tc.Kind == "custom_tool_call" {
				call["input"] = tc.Arguments.String()
			}
			toolCalls = append(toolCalls, call)
		}
		if len(toolCalls) > 0 {
			result["tool_calls"] = toolCalls
		}
	}

	if len(s.OutputItems) > 0 {
		keys := make([]int, 0, len(s.OutputItems))
		for index := range s.OutputItems {
			keys = append(keys, index)
		}
		sort.Ints(keys)

		outputItems := make([]map[string]interface{}, 0, len(keys))
		for _, index := range keys {
			if item := s.OutputItems[index]; item != nil {
				outputItems = append(outputItems, cloneInterfaceMap(item))
			}
		}
		if len(outputItems) > 0 {
			result[codexResponseOutputItemsKey] = outputItems
		}
	}

	return result
}

func (s *CodexStreamState) AddAnnotations(raw interface{}) {
	for _, annotation := range decodeSliceOfMaps(raw) {
		s.AddAnnotation(annotation)
	}
}

func (s *CodexStreamState) AddAnnotation(annotation map[string]interface{}) {
	if s == nil || len(annotation) == 0 {
		return
	}
	encoded, _ := json.Marshal(annotation)
	for _, existing := range s.Annotations {
		existingJSON, _ := json.Marshal(existing)
		if bytes.Equal(existingJSON, encoded) {
			return
		}
	}
	s.Annotations = append(s.Annotations, cloneInterfaceMap(annotation))
}

func (s *CodexStreamState) RecordUnknownEvent(eventType string) {
	if s == nil {
		return
	}
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = "(missing)"
	}
	s.UnknownEvents[eventType]++
}

func (s *CodexStreamState) SetError(code, message string) {
	if s == nil {
		return
	}
	code = strings.TrimSpace(code)
	message = strings.TrimSpace(message)
	if s.ErrorCode == "" && code != "" {
		s.ErrorCode = code
	}
	if message == "" {
		return
	}
	if s.ErrorMessage == "" {
		s.ErrorMessage = message
		return
	}
	if !strings.Contains(s.ErrorMessage, message) {
		s.ErrorMessage = s.ErrorMessage + "; " + message
	}
}

func (s *CodexStreamState) StreamError() error {
	if s == nil {
		return nil
	}
	switch strings.TrimSpace(s.FinishReason) {
	case "failed":
		message := strings.TrimSpace(s.ErrorMessage)
		if message == "" {
			message = "unknown codex stream failure"
		}
		return &codexResponseError{
			kind:    "codex response failed",
			code:    strings.TrimSpace(s.ErrorCode),
			message: message,
		}
	case "incomplete":
		message := strings.TrimSpace(s.ErrorMessage)
		if message == "" {
			message = "unknown codex stream incomplete response"
		}
		return &codexResponseError{
			kind:    "codex response incomplete",
			code:    strings.TrimSpace(s.ErrorCode),
			message: message,
		}
	default:
		return nil
	}
}

type codexResponseError struct {
	kind    string
	code    string
	message string
}

func (e *codexResponseError) Error() string {
	if e == nil {
		return ""
	}
	kind := strings.TrimSpace(e.kind)
	if code := strings.TrimSpace(e.code); code != "" {
		kind = fmt.Sprintf("%s (code=%s)", kind, code)
	}
	message := strings.TrimSpace(e.message)
	switch {
	case kind == "":
		return message
	case message == "":
		return kind
	default:
		return fmt.Sprintf("%s: %s", kind, message)
	}
}

func (e *codexResponseError) RetryErrorCode() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.code)
}

func codexErrorFromEvent(event map[string]interface{}) (string, string) {
	if len(event) == 0 {
		return "", ""
	}
	code := strings.TrimSpace(asCodexString(event["code"]))
	if nested, ok := event["error"].(map[string]interface{}); ok {
		if code == "" {
			code = strings.TrimSpace(asCodexString(nested["code"]))
		}
		if code == "" {
			code = strings.TrimSpace(asCodexString(nested["type"]))
		}
		if message := strings.TrimSpace(asCodexString(nested["message"])); message != "" {
			return code, message
		}
	}
	if response, ok := event["response"].(map[string]interface{}); ok {
		if nested, ok := response["error"].(map[string]interface{}); ok {
			if code == "" {
				code = strings.TrimSpace(asCodexString(nested["code"]))
			}
			if code == "" {
				code = strings.TrimSpace(asCodexString(nested["type"]))
			}
			if message := strings.TrimSpace(asCodexString(nested["message"])); message != "" {
				return code, message
			}
		}
	}
	return code, strings.TrimSpace(asCodexString(event["message"]))
}

func codexIncompleteMessage(event map[string]interface{}) (string, string) {
	if len(event) == 0 {
		return "incomplete", ""
	}
	if details, ok := event["incomplete_details"]; ok {
		if encoded := stringifyCodexEventField(details); encoded != "" {
			return "incomplete", encoded
		}
	}
	if response, ok := event["response"].(map[string]interface{}); ok {
		if details, ok := response["incomplete_details"]; ok {
			if encoded := stringifyCodexEventField(details); encoded != "" {
				return "incomplete", encoded
			}
		}
	}
	return "incomplete", strings.TrimSpace(asCodexString(event["message"]))
}

func codexIncompleteReason(event map[string]interface{}) string {
	if len(event) == 0 {
		return ""
	}
	if details := decodeMap(event["incomplete_details"]); details != nil {
		if reason := strings.TrimSpace(asCodexString(details["reason"])); reason != "" {
			return reason
		}
	}
	if response := decodeMap(event["response"]); response != nil {
		return codexIncompleteReason(response)
	}
	return ""
}

func isCodexMaxOutputStop(reason string) bool {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "max_output_tokens", "max_tokens", "length", "max_output_tokens_exceeded":
		return true
	default:
		return false
	}
}

func stringifyCodexEventField(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(typed))
		}
		return strings.TrimSpace(string(encoded))
	}
}

func asCodexString(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		return fmt.Sprint(typed)
	}
}

func cloneInterfaceMap(raw map[string]interface{}) map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(raw))
	for key, value := range raw {
		cloned[key] = value
	}
	return cloned
}

// getIntIndex 从事件中获取 index 字段
func getIntIndex(event map[string]interface{}) int {
	if index, ok := numericValue(event["index"]); ok {
		return int(index)
	}
	if outputIndex, ok := numericValue(event["output_index"]); ok {
		return int(outputIndex)
	}
	return -1
}

func resolveCodexToolIndex(state *CodexStreamState, event map[string]interface{}) int {
	if index := getIntIndex(event); index >= 0 {
		if key := codexToolItemKeyFromEvent(event); key != "" {
			state.ToolItemKeys[key] = index
		}
		return index
	}
	key := codexToolItemKeyFromEvent(event)
	if key == "" {
		return -1
	}
	if index, exists := state.ToolItemKeys[key]; exists {
		return index
	}
	index := state.NextSyntheticIndex
	state.NextSyntheticIndex++
	state.ToolItemKeys[key] = index
	return index
}

func codexToolItemKeyFromEvent(event map[string]interface{}) string {
	if key, _ := event["item_id"].(string); strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if key, _ := event["call_id"].(string); strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if item := decodeMap(event["item"]); item != nil {
		return codexToolItemKeyFromItem(item)
	}
	return ""
}

func codexToolItemKeyFromItem(item map[string]interface{}) string {
	if item == nil {
		return ""
	}
	if key, _ := item["id"].(string); strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	if key, _ := item["call_id"].(string); strings.TrimSpace(key) != "" {
		return strings.TrimSpace(key)
	}
	return ""
}

func codexToolItemKeys(item map[string]interface{}) []string {
	if item == nil {
		return nil
	}
	keys := make([]string, 0, 2)
	if key, _ := item["id"].(string); strings.TrimSpace(key) != "" {
		keys = append(keys, strings.TrimSpace(key))
	}
	if key, _ := item["call_id"].(string); strings.TrimSpace(key) != "" {
		trimmed := strings.TrimSpace(key)
		if len(keys) == 0 || keys[0] != trimmed {
			keys = append(keys, trimmed)
		}
	}
	return keys
}

func mergeCodexTools(groups ...interface{}) []map[string]interface{} {
	merged := make([]map[string]interface{}, 0)

	for _, group := range groups {
		for _, tool := range normalizeCodexTools(group) {
			if tool == nil {
				continue
			}
			merged = append(merged, tool)
		}
	}

	if len(merged) == 0 {
		return nil
	}
	return merged
}

func codexToolName(tool map[string]interface{}) string {
	if tool == nil {
		return ""
	}
	name, _ := tool["name"].(string)
	return strings.TrimSpace(name)
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
			if params, ok := fn["parameters"]; ok && params != nil {
				out["parameters"] = params
			}
			if _, ok := out["parameters"]; !ok {
				out["parameters"] = emptyCodexObjectParameters()
			}
			if strict, ok := tool["strict"].(bool); ok {
				out["strict"] = strict
			} else if strict, ok := fn["strict"].(bool); ok {
				out["strict"] = strict
			} else {
				out["strict"] = false
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
		if toolType, _ := flat["type"].(string); strings.TrimSpace(toolType) != "" && !strings.EqualFold(strings.TrimSpace(toolType), "function") {
			normalized = append(normalized, flat)
			continue
		}
		if _, ok := flat["type"]; !ok {
			flat["type"] = "function"
		}
		if _, ok := flat["parameters"]; !ok {
			flat["parameters"] = emptyCodexObjectParameters()
		} else if flat["parameters"] == nil {
			flat["parameters"] = emptyCodexObjectParameters()
		}
		if strict, ok := flat["strict"].(bool); ok {
			flat["strict"] = strict
		} else {
			flat["strict"] = false
		}
		normalized = append(normalized, sanitizeCodexTool(flat))
	}

	return normalized
}

func codexToolsContainType(tools []map[string]interface{}, target string) bool {
	target = strings.TrimSpace(target)
	if target == "" {
		return false
	}
	for _, tool := range tools {
		if tool == nil {
			continue
		}
		toolType, _ := tool["type"].(string)
		if strings.EqualFold(strings.TrimSpace(toolType), target) {
			return true
		}
	}
	return false
}

// NormalizeCodexToolsForRequest normalizes tools for the Codex/Responses API.
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

	strict, _ := sanitized["strict"].(bool)
	if params := decodeMap(sanitized["parameters"]); params != nil {
		parameters := sanitizeCodexSchemaMap(params, false, strict)
		compactLargeCodexToolSchema(parameters, strict)
		sanitized["parameters"] = parameters
	} else if strings.EqualFold(strings.TrimSpace(asCodexString(sanitized["type"])), "function") || sanitized["type"] == nil {
		sanitized["parameters"] = emptyCodexObjectParameters()
	}

	return sanitized
}

func emptyCodexObjectParameters() map[string]interface{} {
	return map[string]interface{}{
		"type":                 "object",
		"properties":           map[string]interface{}{},
		"additionalProperties": false,
	}
}

func sanitizeCodexSchemaMap(schema map[string]interface{}, optional bool, strict bool) map[string]interface{} {
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
	if constValue, ok := sanitized["const"]; ok {
		delete(sanitized, "const")
		if _, hasEnum := sanitized["enum"]; !hasEnum {
			sanitized["enum"] = []interface{}{constValue}
		}
	}
	normalizeCodexSchemaRequired(sanitized)
	normalizeCodexSchemaEnum(sanitized)
	normalizeCodexSchemaType(sanitized)
	inferCodexSchemaType(sanitized)
	pruneUnsupportedCodexSchemaKeywords(sanitized)
	if !codexSchemaHasShape(sanitized) {
		return map[string]interface{}{}
	}

	for _, key := range []string{"properties", "items", "anyOf", "oneOf", "allOf"} {
		switch key {
		case "properties":
			if rawProps, exists := sanitized[key]; exists {
				props := decodeMap(rawProps)
				requiredSet := buildRequiredSet(sanitized["required"])
				names := make([]string, 0, len(props))
				sanitizedProps := make(map[string]interface{}, len(props))
				for name, prop := range props {
					names = append(names, name)
					sanitizedProp := sanitizeCodexSchemaValue(prop, strict && !requiredSet[name], strict)
					sanitizedProps[name] = sanitizedProp
				}
				sanitized["properties"] = sanitizedProps
				if strict {
					sort.Strings(names)
					sanitized["required"] = names
				}
			}
		case "items":
			if items, exists := sanitized[key]; exists {
				sanitized["items"] = sanitizeCodexSchemaValue(items, false, strict)
			}
		default:
			if _, exists := sanitized[key]; exists {
				if variants := sanitizeCodexSchemaVariants(sanitized[key], strict); len(variants) > 0 {
					sanitized[key] = variants
				} else {
					delete(sanitized, key)
				}
			}
		}
	}
	sanitizeCodexSchemaDefinitions(sanitized, strict)
	pruneUnreachableCodexSchemaDefinitions(sanitized)
	if additional, exists := sanitized["additionalProperties"]; exists {
		if _, ok := additional.(bool); !ok {
			sanitized["additionalProperties"] = sanitizeCodexSchemaValue(additional, false, strict)
		}
	}

	if schemaType, ok := sanitized["type"]; ok {
		if typeContains(schemaType, "object") {
			if strict {
				sanitized["additionalProperties"] = false
			}
			if _, exists := sanitized["properties"]; !exists {
				sanitized["properties"] = map[string]interface{}{}
			}
			if strict {
				if _, exists := sanitized["required"]; !exists {
					sanitized["required"] = []string{}
				}
			}
		}
		if typeContains(schemaType, "array") {
			if _, exists := sanitized["items"]; !exists {
				sanitized["items"] = map[string]interface{}{"type": "string"}
			}
		}
	}

	if optional {
		makeCodexSchemaNullable(sanitized)
	}

	return sanitized
}

func sanitizeCodexSchemaValue(raw interface{}, optional bool, strict bool) interface{} {
	if _, ok := raw.(bool); ok {
		return sanitizeCodexSchemaMap(map[string]interface{}{"type": "string"}, optional, strict)
	}
	if schema := decodeMap(raw); schema != nil {
		return sanitizeCodexSchemaMap(schema, optional, strict)
	}
	return map[string]interface{}{}
}

func sanitizeCodexSchemaVariants(raw interface{}, strict bool) []interface{} {
	var values []interface{}
	switch typed := raw.(type) {
	case []interface{}:
		values = typed
	case []map[string]interface{}:
		values = make([]interface{}, len(typed))
		for index, value := range typed {
			values[index] = value
		}
	default:
		encoded, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(encoded, &values) != nil {
			return nil
		}
	}
	if len(values) == 0 {
		return nil
	}
	out := make([]interface{}, 0, len(values))
	for _, value := range values {
		out = append(out, sanitizeCodexSchemaValue(value, false, strict))
	}
	return out
}

func sanitizeCodexSchemaDefinitions(schema map[string]interface{}, strict bool) {
	for _, key := range []string{"$defs", "definitions"} {
		raw, exists := schema[key]
		if !exists {
			continue
		}
		definitions := decodeMap(raw)
		if definitions == nil {
			delete(schema, key)
			continue
		}
		sanitized := make(map[string]interface{}, len(definitions))
		for name, definition := range definitions {
			sanitized[name] = sanitizeCodexSchemaValue(definition, false, strict)
		}
		schema[key] = sanitized
	}
}

func normalizeCodexSchemaRequired(schema map[string]interface{}) {
	raw, exists := schema["required"]
	if !exists {
		return
	}
	var required []string
	switch typed := raw.(type) {
	case []string:
		required = append([]string(nil), typed...)
	case []interface{}:
		for _, value := range typed {
			if name, ok := value.(string); ok {
				required = append(required, name)
			}
		}
	default:
		delete(schema, "required")
		return
	}
	if len(required) == 0 {
		// JSON Schema requires `required` to be an array when present. In Go,
		// normalizing an empty slice through a nil accumulator would otherwise
		// serialize it as `null`, which several Responses-compatible providers
		// reject before inference starts. Omission is equivalent for non-strict
		// schemas; strict normalization reconstructs the complete array later.
		delete(schema, "required")
		return
	}
	schema["required"] = required
}

func normalizeCodexSchemaEnum(schema map[string]interface{}) {
	raw, exists := schema["enum"]
	if !exists {
		return
	}
	if _, ok := raw.([]interface{}); ok {
		return
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		delete(schema, "enum")
		return
	}
	var values []interface{}
	if err := json.Unmarshal(encoded, &values); err != nil {
		delete(schema, "enum")
		return
	}
	schema["enum"] = values
}

func normalizeCodexSchemaType(schema map[string]interface{}) {
	raw, exists := schema["type"]
	if !exists {
		return
	}
	allowed := func(value string) bool {
		switch value {
		case "object", "string", "number", "integer", "boolean", "array", "null":
			return true
		default:
			return false
		}
	}
	switch typed := raw.(type) {
	case string:
		if !allowed(typed) {
			delete(schema, "type")
		}
	case []string:
		values := make([]interface{}, 0, len(typed))
		for _, value := range typed {
			if allowed(value) {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			delete(schema, "type")
		} else {
			schema["type"] = values
		}
	case []interface{}:
		values := make([]interface{}, 0, len(typed))
		for _, rawValue := range typed {
			if value, ok := rawValue.(string); ok && allowed(value) {
				values = append(values, value)
			}
		}
		if len(values) == 0 {
			delete(schema, "type")
		} else {
			schema["type"] = values
		}
	default:
		delete(schema, "type")
	}
}

type codexDefinitionRef struct {
	table string
	name  string
}

func pruneUnreachableCodexSchemaDefinitions(schema map[string]interface{}) {
	definitions := map[string]map[string]interface{}{}
	for _, table := range []string{"$defs", "definitions"} {
		if values := decodeMap(schema[table]); values != nil {
			definitions[table] = values
		}
	}
	if len(definitions) == 0 {
		return
	}

	reachable := make(map[codexDefinitionRef]bool)
	queue := collectCodexDefinitionRefs(schema, false)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if reachable[ref] {
			continue
		}
		reachable[ref] = true
		definition := definitions[ref.table][ref.name]
		if definition != nil {
			queue = append(queue, collectCodexDefinitionRefs(definition, true)...)
		}
	}

	for table, values := range definitions {
		for name := range values {
			if !reachable[codexDefinitionRef{table: table, name: name}] {
				delete(values, name)
			}
		}
		if len(values) == 0 {
			delete(schema, table)
		}
	}
}

func collectCodexDefinitionRefs(value interface{}, includeDefinitionTables bool) []codexDefinitionRef {
	refs := make([]codexDefinitionRef, 0)
	var visit func(interface{})
	visit = func(current interface{}) {
		switch typed := current.(type) {
		case map[string]interface{}:
			if rawRef, _ := typed["$ref"].(string); rawRef != "" {
				if ref, ok := parseCodexDefinitionRef(rawRef); ok {
					refs = append(refs, ref)
				}
			}
			if properties := decodeMap(typed["properties"]); properties != nil {
				for _, property := range properties {
					visit(property)
				}
			}
			for _, key := range []string{"items", "anyOf", "oneOf", "allOf"} {
				visit(typed[key])
			}
			if _, isBool := typed["additionalProperties"].(bool); !isBool {
				visit(typed["additionalProperties"])
			}
			if includeDefinitionTables {
				for _, table := range []string{"$defs", "definitions"} {
					if definitions := decodeMap(typed[table]); definitions != nil {
						for _, definition := range definitions {
							visit(definition)
						}
					}
				}
			}
		case []interface{}:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	return refs
}

func parseCodexDefinitionRef(raw string) (codexDefinitionRef, bool) {
	fragment := strings.TrimPrefix(raw, "#")
	if fragment == raw {
		return codexDefinitionRef{}, false
	}
	if decoded, err := url.PathUnescape(fragment); err == nil {
		fragment = decoded
	}
	parts := strings.Split(strings.TrimPrefix(fragment, "/"), "/")
	if len(parts) < 2 {
		return codexDefinitionRef{}, false
	}
	table := strings.ReplaceAll(strings.ReplaceAll(parts[0], "~1", "/"), "~0", "~")
	name := strings.ReplaceAll(strings.ReplaceAll(parts[1], "~1", "/"), "~0", "~")
	if (table == "$defs" || table == "definitions") && name != "" {
		return codexDefinitionRef{table: table, name: name}, true
	}
	return codexDefinitionRef{}, false
}

func inferCodexSchemaType(schema map[string]interface{}) {
	if schema == nil || schema["type"] != nil || schema["$ref"] != nil {
		return
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf"} {
		if schema[key] != nil {
			return
		}
	}
	for _, key := range []string{"properties", "required", "additionalProperties"} {
		if _, exists := schema[key]; exists {
			schema["type"] = "object"
			return
		}
	}
	for _, key := range []string{"items", "prefixItems"} {
		if _, exists := schema[key]; exists {
			schema["type"] = "array"
			return
		}
	}
	for _, key := range []string{"enum", "format"} {
		if _, exists := schema[key]; exists {
			schema["type"] = "string"
			return
		}
	}
	for _, key := range []string{"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"} {
		if _, exists := schema[key]; exists {
			schema["type"] = "number"
			return
		}
	}
}

func pruneUnsupportedCodexSchemaKeywords(schema map[string]interface{}) {
	for key := range schema {
		switch key {
		case "$ref", "type", "description", "encrypted", "enum",
			"items", "properties", "required", "additionalProperties",
			"anyOf", "oneOf", "allOf", "$defs", "definitions":
		default:
			delete(schema, key)
		}
	}
}

func codexSchemaHasShape(schema map[string]interface{}) bool {
	for _, key := range []string{"$ref", "type", "enum", "items", "properties", "required", "additionalProperties", "anyOf", "oneOf", "allOf"} {
		if _, exists := schema[key]; exists {
			return true
		}
	}
	return false
}

func compactLargeCodexToolSchema(schema map[string]interface{}, strict bool) {
	if codexSchemaFitsCompactBudget(schema) {
		return
	}
	stripCodexSchemaDescriptions(schema)
	if strict || codexSchemaFitsCompactBudget(schema) {
		return
	}
	rewriteCodexDefinitionRefs(schema)
	delete(schema, "$defs")
	delete(schema, "definitions")
	if codexSchemaFitsCompactBudget(schema) {
		return
	}
	collapseDeepCodexSchemaObjects(schema, 0)
	if !codexSchemaFitsCompactBudget(schema) {
		pruneCodexSchemaCompositions(schema)
	}
}

func codexSchemaFitsCompactBudget(schema map[string]interface{}) bool {
	encoded, err := json.Marshal(schema)
	return err == nil && len(encoded) <= codexToolSchemaCompactBytes
}

func stripCodexSchemaDescriptions(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		delete(typed, "description")
		if properties := decodeMap(typed["properties"]); properties != nil {
			for _, property := range properties {
				stripCodexSchemaDescriptions(property)
			}
		}
		for _, key := range []string{"items", "anyOf", "oneOf", "allOf"} {
			stripCodexSchemaDescriptions(typed[key])
		}
		if _, isBool := typed["additionalProperties"].(bool); !isBool {
			stripCodexSchemaDescriptions(typed["additionalProperties"])
		}
		for _, key := range []string{"$defs", "definitions"} {
			if definitions := decodeMap(typed[key]); definitions != nil {
				for _, definition := range definitions {
					stripCodexSchemaDescriptions(definition)
				}
			}
		}
	case []interface{}:
		for _, child := range typed {
			stripCodexSchemaDescriptions(child)
		}
	}
}

func rewriteCodexDefinitionRefs(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if ref, _ := typed["$ref"].(string); strings.HasPrefix(ref, "#/$defs/") || strings.HasPrefix(ref, "#/definitions/") {
			for key := range typed {
				delete(typed, key)
			}
			return
		}
		if properties := decodeMap(typed["properties"]); properties != nil {
			for _, property := range properties {
				rewriteCodexDefinitionRefs(property)
			}
		}
		for _, key := range []string{"items", "anyOf", "oneOf", "allOf"} {
			rewriteCodexDefinitionRefs(typed[key])
		}
		if _, isBool := typed["additionalProperties"].(bool); !isBool {
			rewriteCodexDefinitionRefs(typed["additionalProperties"])
		}
	case []interface{}:
		for _, child := range typed {
			rewriteCodexDefinitionRefs(child)
		}
	}
}

func collapseDeepCodexSchemaObjects(value interface{}, depth int) {
	switch typed := value.(type) {
	case map[string]interface{}:
		if depth >= codexToolSchemaCompactDepth && codexSchemaIsComplex(typed) {
			for key := range typed {
				delete(typed, key)
			}
			return
		}
		if properties := decodeMap(typed["properties"]); properties != nil {
			for _, property := range properties {
				collapseDeepCodexSchemaObjects(property, depth+1)
			}
		}
		for _, key := range []string{"items", "anyOf", "oneOf", "allOf"} {
			collapseDeepCodexSchemaObjects(typed[key], depth+1)
		}
		if _, isBool := typed["additionalProperties"].(bool); !isBool {
			collapseDeepCodexSchemaObjects(typed["additionalProperties"], depth+1)
		}
	case []interface{}:
		for _, child := range typed {
			collapseDeepCodexSchemaObjects(child, depth)
		}
	}
}

func codexSchemaIsComplex(schema map[string]interface{}) bool {
	for _, key := range []string{"properties", "items", "anyOf", "oneOf", "allOf", "additionalProperties", "$ref"} {
		if _, exists := schema[key]; exists {
			return true
		}
	}
	return false
}

func pruneCodexSchemaCompositions(value interface{}) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, key := range []string{"anyOf", "oneOf", "allOf"} {
			if _, exists := typed[key]; exists {
				for existing := range typed {
					delete(typed, existing)
				}
				return
			}
		}
		if properties := decodeMap(typed["properties"]); properties != nil {
			for _, property := range properties {
				pruneCodexSchemaCompositions(property)
			}
		}
		pruneCodexSchemaCompositions(typed["items"])
		if _, isBool := typed["additionalProperties"].(bool); !isBool {
			pruneCodexSchemaCompositions(typed["additionalProperties"])
		}
	case []interface{}:
		for _, child := range typed {
			pruneCodexSchemaCompositions(child)
		}
	}
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
	if outputItems := decodeSliceOfMaps(result[codexResponseOutputItemsKey]); len(outputItems) > 0 || strings.TrimSpace(pr.Reasoning) != "" {
		visibility := runtimetypes.ReasoningVisibilitySummary
		if strings.TrimSpace(pr.Reasoning) == "" {
			visibility = runtimetypes.ReasoningVisibilityOpaque
		}
		pr.ReasoningBlock = &runtimetypes.ReasoningBlock{
			Format:     "openai_responses",
			Summary:    strings.TrimSpace(pr.Reasoning),
			Streamable: true,
			Visibility: visibility,
			Metadata:   map[string]interface{}{},
		}
		if len(outputItems) > 0 {
			pr.ReasoningBlock.Metadata["response_output_items"] = outputItems
		}
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
		if reasoningDetails := buildCodexReasoningDetails(reasoning, outputItems); len(reasoningDetails) > 0 {
			msg[assistantReasoningDetailsKey] = reasoningDetails
		}
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

func extractCodexUserInputParts(msg map[string]interface{}) []map[string]interface{} {
	if msg == nil {
		return nil
	}
	switch typed := msg["content"].(type) {
	case []map[string]interface{}:
		return normalizeCodexInputPartsFromMaps(typed)
	case []interface{}:
		return normalizeCodexInputParts(typed)
	default:
		return nil
	}
}

func normalizeCodexInputParts(raw []interface{}) []map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	parts := make([]map[string]interface{}, 0, len(raw))
	for _, item := range raw {
		part := decodeMap(item)
		if canonical := normalizeCodexInputPart(part); canonical != nil {
			parts = append(parts, canonical)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func normalizeCodexInputPartsFromMaps(raw []map[string]interface{}) []map[string]interface{} {
	if len(raw) == 0 {
		return nil
	}
	parts := make([]map[string]interface{}, 0, len(raw))
	for _, part := range raw {
		if canonical := normalizeCodexInputPart(part); canonical != nil {
			parts = append(parts, canonical)
		}
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func normalizeCodexInputPart(part map[string]interface{}) map[string]interface{} {
	if len(part) == 0 {
		return nil
	}
	partType, _ := part["type"].(string)
	switch strings.ToLower(strings.TrimSpace(partType)) {
	case "input_text", "text", "output_text", "summary_text":
		text, _ := part["text"].(string)
		if strings.TrimSpace(text) == "" {
			return nil
		}
		return map[string]interface{}{
			"type": "input_text",
			"text": text,
		}
	case "input_image", "image_url":
		imageURL, _ := part["image_url"].(string)
		if strings.TrimSpace(imageURL) == "" {
			return nil
		}
		return map[string]interface{}{
			"type":      "input_image",
			"image_url": strings.TrimSpace(imageURL),
		}
	default:
		return nil
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

func buildCodexReasoningDetails(reasoning string, outputItems []map[string]interface{}) map[string]interface{} {
	details := map[string]interface{}{
		"format":     "openai_responses",
		"streamable": true,
	}
	if strings.TrimSpace(reasoning) != "" {
		details["summary"] = strings.TrimSpace(reasoning)
		details["visibility"] = "summary"
	} else {
		details["visibility"] = "opaque"
	}
	if len(outputItems) > 0 {
		details["metadata"] = map[string]interface{}{
			"response_output_items": outputItems,
		}
	}
	if len(details) == 0 {
		return nil
	}
	return details
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

	toolType, _ := raw["type"].(string)
	toolType = strings.TrimSpace(toolType)
	if toolType == "" {
		toolType = "function_call"
	}
	callID, _ := raw["call_id"].(string)
	if callID == "" {
		callID, _ = raw["id"].(string)
	}

	name, _ := raw["name"].(string)
	arguments, _ := raw["arguments"].(string)
	input, _ := raw["input"].(string)

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

	if toolType == "custom_tool_call" {
		if input == "" {
			input = arguments
		}
		return map[string]interface{}{
			"type":    "custom_tool_call",
			"call_id": callID,
			"name":    name,
			"input":   input,
		}
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
		toolType, _ := raw["type"].(string)
		toolType = strings.TrimSpace(toolType)

		if argsStr, ok := raw["arguments"].(string); ok {
			// 直接使用 arguments 字符串（已经是 JSON）
			argsJSON = argsStr
		} else if argsMap, ok := raw["arguments"].(map[string]interface{}); ok {
			// 如果是 map，序列化为 JSON 字符串
			if jsonBytes, err := json.Marshal(argsMap); err == nil {
				argsJSON = string(jsonBytes)
			}
		}
		if toolType == "custom_tool_call" {
			if input, ok := raw["input"].(string); ok {
				argsJSON = input
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
		strings.HasPrefix(model, "gpt-5") ||
		strings.Contains(model, "gpt-4.1") ||
		strings.Contains(model, "o1") ||
		strings.Contains(model, "o3")
}

package adapter

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// OpenAIAdapter OpenAI/Gemini protocol adapter
type OpenAIAdapter struct{}

type openAIStreamError struct {
	eventType string
	code      string
	errorType string
	message   string
}

type openAIProtocolError struct {
	code    string
	message string
}

func (e *openAIProtocolError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("openai_stream_protocol_error: code=%s: %s", strings.TrimSpace(e.code), strings.TrimSpace(e.message))
}

func (e *openAIProtocolError) RetryErrorCode() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.code)
}

func (e *openAIStreamError) Error() string {
	if e == nil {
		return ""
	}
	metadata := make([]string, 0, 3)
	if e.eventType != "" {
		metadata = append(metadata, "event="+e.eventType)
	}
	if e.errorType != "" {
		metadata = append(metadata, "type="+e.errorType)
	}
	if e.code != "" && e.code != e.errorType {
		metadata = append(metadata, "code="+e.code)
	}
	detail := ""
	if len(metadata) > 0 {
		detail = " (" + strings.Join(metadata, ", ") + ")"
	}
	message := strings.TrimSpace(e.message)
	if message == "" {
		message = "upstream stream returned an error event"
	}
	return "stream_interrupted: openai stream error" + detail + ": " + message
}

func (e *openAIStreamError) RetryErrorCode() string {
	if e == nil {
		return ""
	}
	if code := strings.TrimSpace(e.code); code != "" {
		return code
	}
	if errorType := strings.TrimSpace(e.errorType); errorType != "" {
		return errorType
	}
	return "openai_stream_error"
}

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
				procResult.Refusal = openAIRefusalText(msg["refusal"])
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
					procResult.ToolCalls = append(procResult.ToolCalls, map[string]interface{}{
						"id":       "legacy_function_call_1",
						"type":     "function",
						"function": fn,
					})
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
	Refusal          strings.Builder
	ReasoningPresent bool
	ToolCalls        map[int]*StreamToolCall // key 是 tool_call 的 index
	MarkupTail       string
	MarkupCalls      []map[string]interface{}
	FinishReason     string
	ProtocolError    error
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
	return toolargs.DecodeJSON(tc.Args.String())
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
		if err := scanSSEFrames(respBody, func(frame SSEFrame) (bool, error) {
			data := strings.TrimSpace(frame.Data)
			eventType := strings.TrimSpace(frame.Event)
			if strings.EqualFold(eventType, "error") && data == "" {
				return false, &openAIStreamError{eventType: eventType, message: "upstream returned an empty error event"}
			}
			if data == "[DONE]" {
				if strings.EqualFold(eventType, "error") {
					return false, &openAIStreamError{eventType: eventType, message: data}
				}
				return false, nil
			}
			if data == "" {
				if isOpenAIStreamDoneEvent(eventType) {
					return false, nil
				}
				return true, nil
			}

			var chunk map[string]interface{}
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				if strings.EqualFold(eventType, "error") {
					return false, &openAIStreamError{eventType: eventType, message: data}
				}
				return false, fmt.Errorf("malformed_stream_event: event=%s: %w", eventType, err)
			}
			if streamErr := newOpenAIStreamError(eventType, chunk); streamErr != nil {
				return false, streamErr
			}

			parseChunk(state, chunk, callbacks)
			if state.ProtocolError != nil {
				return false, state.ProtocolError
			}
			return !isOpenAIStreamDoneEvent(eventType), nil
		}); err != nil {
			return nil, err
		}
		flushPendingMarkupContent(state, callbacks)
		if err := validateOpenAIStreamState(state); err != nil {
			return nil, err
		}

		// 转换为最终结果
		streamData := state.ToMap()
		toolCalls, _ := streamData["tool_calls"].([]map[string]interface{})
		content, _ := streamData["content"].(string)
		reasoning, _ := streamData["reasoning"].(string)
		_, reasoningPresent := streamData["reasoning"]
		assistantMsg := a.buildAssistantMessageWithReasoningPresence(content, toolCalls, reasoning, reasoningPresent)
		attachOpenAIRefusal(assistantMsg, state.Refusal.String())
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

	if streamErr := newOpenAIStreamError("", result); streamErr != nil {
		return nil, streamErr
	}
	procResult := a.ProcessResponse(result)
	if err := validateOpenAIRawToolCalls(procResult.ToolCalls); err != nil {
		return nil, err
	}
	assistantMsg := a.buildAssistantMessageWithReasoningPresence(procResult.Content, procResult.ToolCalls, procResult.Reasoning, procResult.ReasoningPresent)
	attachOpenAIRefusal(assistantMsg, procResult.Refusal)
	if choice := firstOpenAIChoice(result); choice != nil {
		if finishReason := strings.TrimSpace(firstOpenAIErrorString(choice["finish_reason"])); finishReason != "" {
			if err := validateOpenAIFinishReason(finishReason); err != nil {
				return nil, err
			}
			assistantMsg["finish_reason"] = finishReason
		}
	}
	return attachReasoningBlock(assistantMsg, procResult.ReasoningBlock), nil
}

func newOpenAIStreamError(eventType string, chunk map[string]interface{}) error {
	eventType = strings.TrimSpace(eventType)
	rawError, hasError := chunk["error"]
	hasError = hasError && openAIErrorPayloadPresent(rawError)
	chunkType := firstOpenAIErrorString(chunk["type"], chunk["object"])
	isErrorType := strings.EqualFold(chunkType, "error") || strings.HasSuffix(strings.ToLower(chunkType), "_error")
	isFailedResponse := strings.EqualFold(chunkType, "response.failed") || strings.EqualFold(eventType, "response.failed")
	isCancelledResponse := isOpenAIResponseCancelled(chunkType) || isOpenAIResponseCancelled(eventType)
	if !hasError && !strings.EqualFold(eventType, "error") && !isErrorType && !isFailedResponse && !isCancelledResponse {
		return nil
	}

	details, _ := rawError.(map[string]interface{})
	message := ""
	errorType := ""
	code := ""
	if rawMessage, ok := rawError.(string); ok {
		message = strings.TrimSpace(rawMessage)
	}
	if details != nil {
		message = firstOpenAIErrorString(details["message"], details["detail"], message)
	}
	if response, ok := chunk["response"].(map[string]interface{}); ok {
		if nested, ok := response["error"].(map[string]interface{}); ok {
			message = firstOpenAIErrorString(nested["message"], nested["detail"], message)
			if errorType == "" {
				errorType = firstOpenAIErrorString(nested["type"])
			}
			if code == "" {
				code = firstOpenAIErrorString(nested["code"])
			}
		}
	}
	message = firstOpenAIErrorString(message, chunk["message"], chunk["detail"])
	if isCancelledResponse {
		if code == "" {
			code = "response_cancelled"
		}
		if message == "" {
			message = "upstream response was cancelled before completion"
		}
	}

	if details != nil {
		errorType = firstOpenAIErrorString(errorType, details["type"])
		code = firstOpenAIErrorString(code, details["code"])
	}
	errorType = firstOpenAIErrorString(errorType, chunk["type"], chunk["object"])
	code = firstOpenAIErrorString(code, chunk["code"])

	return &openAIStreamError{
		eventType: eventType,
		code:      code,
		errorType: errorType,
		message:   message,
	}
}

func isOpenAIResponseCancelled(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func isOpenAIStreamDoneEvent(eventType string) bool {
	switch strings.ToLower(strings.TrimSpace(eventType)) {
	case "done", "response.completed", "response.done":
		return true
	default:
		return false
	}
}

func validateOpenAIStreamState(state *StreamState) error {
	if state == nil {
		return &openAIProtocolError{code: "invalid_stream_state", message: "stream state is nil"}
	}
	finishReason := strings.ToLower(strings.TrimSpace(state.FinishReason))
	if err := validateOpenAIFinishReason(finishReason); err != nil {
		return err
	}
	switch finishReason {
	case "tool_calls", "function_call":
		if len(state.ToolCalls)+len(state.MarkupCalls) == 0 {
			return &openAIProtocolError{
				code:    "missing_tool_call",
				message: fmt.Sprintf("finish_reason=%s was returned without a tool call", finishReason),
			}
		}
	}

	for index, call := range state.ToolCalls {
		if call == nil {
			return &openAIProtocolError{code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is nil", index)}
		}
		if strings.TrimSpace(call.Name) == "" {
			return &openAIProtocolError{code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is missing a function name", index)}
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("stream_tool_call_%d", index+1)
		}
		if call.Type == "" {
			call.Type = "function"
		}
		arguments := strings.TrimSpace(call.Args.String())
		if arguments == "" {
			arguments = "{}"
			call.Args.WriteString(arguments)
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &decoded); err != nil || decoded == nil {
			return &openAIProtocolError{
				code:    "invalid_tool_arguments",
				message: fmt.Sprintf("tool call %d (%s) has incomplete or non-object JSON arguments", index, call.Name),
			}
		}
	}
	return nil
}

func validateOpenAIFinishReason(finishReason string) error {
	switch strings.ToLower(strings.TrimSpace(finishReason)) {
	case "content_filter":
		return &openAIProtocolError{code: "content_filter", message: "provider blocked the response by content policy"}
	case "insufficient_system_resource":
		return &openAIProtocolError{code: "insufficient_system_resource", message: "provider ended the response because upstream resources were unavailable"}
	default:
		return nil
	}
}

func validateOpenAIRawToolCalls(toolCalls []map[string]interface{}) error {
	for index, call := range toolCalls {
		function, _ := call["function"].(map[string]interface{})
		if function == nil {
			return &openAIProtocolError{code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is missing its function object", index)}
		}
		name := strings.TrimSpace(firstOpenAIErrorString(function["name"]))
		if name == "" {
			return &openAIProtocolError{code: "invalid_tool_call", message: fmt.Sprintf("tool call %d is missing a function name", index)}
		}
		rawArguments := function["arguments"]
		if _, ok := rawArguments.(map[string]interface{}); ok {
			continue
		}
		arguments := strings.TrimSpace(firstOpenAIErrorString(rawArguments))
		if arguments == "" {
			continue
		}
		var decoded map[string]interface{}
		if err := json.Unmarshal([]byte(arguments), &decoded); err != nil || decoded == nil {
			return &openAIProtocolError{
				code:    "invalid_tool_arguments",
				message: fmt.Sprintf("tool call %d (%s) has incomplete or non-object JSON arguments", index, name),
			}
		}
	}
	return nil
}

func firstOpenAIChoice(result map[string]interface{}) map[string]interface{} {
	choices, _ := result["choices"].([]interface{})
	if len(choices) == 0 {
		return nil
	}
	choice, _ := choices[0].(map[string]interface{})
	return choice
}

func openAIRefusalText(value interface{}) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case []interface{}:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if text := openAIRefusalText(item); text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]interface{}:
		return firstOpenAIErrorString(typed["refusal"], typed["text"], typed["content"], typed["message"])
	default:
		return ""
	}
}

func attachOpenAIRefusal(assistantMsg map[string]interface{}, refusal string) {
	refusal = strings.TrimSpace(refusal)
	if len(assistantMsg) == 0 || refusal == "" {
		return
	}
	assistantMsg["refusal"] = refusal
	if content, _ := assistantMsg["content"].(string); strings.TrimSpace(content) == "" {
		assistantMsg["content"] = refusal
	}
	metadata, _ := assistantMsg["metadata"].(map[string]interface{})
	if metadata == nil {
		metadata = map[string]interface{}{}
	}
	metadata["refusal"] = refusal
	metadata["refused"] = true
	assistantMsg["metadata"] = metadata
}

func openAIErrorPayloadPresent(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return true
	}
}

func firstOpenAIErrorString(values ...interface{}) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		var text string
		switch typed := value.(type) {
		case string:
			text = typed
		case json.Number:
			text = typed.String()
		case float64, float32, int, int32, int64, uint, uint32, uint64:
			text = fmt.Sprint(typed)
		}
		if text = strings.TrimSpace(text); text != "" {
			return text
		}
	}
	return ""
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
	if refusal, ok := delta["refusal"].(string); ok && refusal != "" {
		state.Refusal.WriteString(refusal)
	} else if refusal := openAIRefusalText(delta["refusal"]); refusal != "" {
		state.Refusal.WriteString(refusal)
	}
	parseContent(state, delta, callbacks)
	parseToolCalls(state, delta)
	parseLegacyFunctionCall(state, delta)
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

		index, err := resolveOpenAIToolCallIndex(state, tcMap)
		if err != nil {
			state.ProtocolError = err
			return
		}

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

func resolveOpenAIToolCallIndex(state *StreamState, tcMap map[string]interface{}) (int, error) {
	if rawIndex, ok := tcMap["index"].(float64); ok && rawIndex >= 0 && rawIndex == float64(int(rawIndex)) {
		return int(rawIndex), nil
	}
	id := strings.TrimSpace(firstOpenAIErrorString(tcMap["id"]))
	if id != "" {
		for index, call := range state.ToolCalls {
			if call != nil && call.ID == id {
				return index, nil
			}
		}
		index := 0
		for existing := range state.ToolCalls {
			if existing >= index {
				index = existing + 1
			}
		}
		return index, nil
	}
	if len(state.ToolCalls) == 0 {
		return 0, nil
	}
	if len(state.ToolCalls) == 1 {
		for index := range state.ToolCalls {
			return index, nil
		}
	}
	return -1, &openAIProtocolError{
		code:    "ambiguous_tool_call_delta",
		message: "tool call delta omitted both index and id while multiple calls were active",
	}
}

func parseLegacyFunctionCall(state *StreamState, delta map[string]interface{}) {
	functionCall, ok := delta["function_call"].(map[string]interface{})
	if !ok {
		return
	}
	tc := state.getToolCall(0)
	if tc.ID == "" {
		tc.ID = "legacy_function_call_1"
	}
	if tc.Type == "" {
		tc.Type = "function"
	}
	if name, ok := functionCall["name"].(string); ok && name != "" {
		tc.Name = name
	}
	if arguments, ok := functionCall["arguments"].(string); ok {
		tc.Args.WriteString(arguments)
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
				args = toolargs.DecodeJSON(argsStr)
			} else if argsMap, ok := fn["arguments"].(map[string]interface{}); ok {
				args = argsMap
			}
			args = toolargs.Normalize(args)

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
	return looksLikeOpenAIReasoningModel(model) || isReasoningModelPrefix(model)
}

func looksLikeOpenAIReasoningModel(modelID string) bool {
	modelID = strings.ToLower(strings.TrimSpace(modelID))
	modelID = strings.TrimPrefix(modelID, "models/")
	if strings.Contains(modelID, "codex") {
		return true
	}
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4", "o5"} {
		if strings.HasPrefix(modelID, prefix) {
			return true
		}
	}
	return false
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

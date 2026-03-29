package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// Provider ??? Runtime Provider ??
type Provider interface {
	Name() string
	Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error)
	Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error)
	CountTokens(text string) int
	GetCapabilities() *ModelCapabilities
	CheckHealth(ctx context.Context) error
}

// ModelCatalogProvider ?????????
type ModelCatalogProvider interface {
	SupportedModels() []string
}

// LegacyChatProvider ???? Chat API??????????
type LegacyChatProvider interface {
	Provider
	ModelCatalogProvider
	Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error)
	ChatStream(ctx context.Context, request ChatRequest, onResponse func(ChatChunk)) error
}

// ChatRequest 聊天请求
type ChatRequest struct {
	Model           string                 `json:"model"`
	Messages        []Message              `json:"messages"`
	MaxTokens       int                    `json:"max_tokens,omitempty"`
	Temperature     float64                `json:"temperature,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty"`
	Thinking        *ThinkingConfig        `json:"thinking,omitempty"`
	Stream          bool                   `json:"stream,omitempty"`
	Tools           []Tool                 `json:"tools,omitempty"`
	ToolChoice      string                 `json:"tool_choice,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// Message 消息
type Message struct {
	Role       string                 `json:"role"`
	Content    string                 `json:"content,omitempty"`
	ToolCalls  []ToolCall             `json:"tool_calls,omitempty"`
	ToolCallID string                 `json:"tool_call_id,omitempty"`
	Reasoning  string                 `json:"reasoning,omitempty"`
	Metadata   map[string]interface{} `json:"metadata,omitempty"`
}

// Tool 工具
type Tool struct {
	Type     string       `json:"type"`
	Function ToolFunction `json:"function"`
}

// ToolFunction 工具函数
type ToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// ToolCall 工具调用
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function ToolCallFunc `json:"function"`
}

// ToolCallFunc 工具调用函数
type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ChatResponse 聊天响应
type ChatResponse struct {
	ID           string                 `json:"id"`
	Object       string                 `json:"object"`
	Created      int64                  `json:"created"`
	Model        string                 `json:"model"`
	Choices      []Choice               `json:"choices"`
	Usage        Usage                  `json:"usage"`
	FinishReason string                 `json:"finish_reason,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// Choice 选择
type Choice struct {
	Index        int           `json:"index"`
	Message      Message       `json:"message"`
	FinishReason string        `json:"finish_reason"`
	Delta        *MessageDelta `json:"delta,omitempty"`
}

// MessageDelta 消息增量（流式响应）
type MessageDelta struct {
	Role      string     `json:"role,omitempty"`
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Usage 使用情况
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatChunk 聊天流式响应块
type ChatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChoiceChunk `json:"choices"`
}

// ChoiceChunk 选择块
type ChoiceChunk struct {
	Index        int          `json:"index"`
	Delta        MessageDelta `json:"delta"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

// ProviderConfig 提供者配置
type ProviderConfig struct {
	Type               string                     `json:"type"` // openai, anthropic, gemini, codex
	APIKey             string                     `json:"apiKey"`
	BaseURL            string                     `json:"baseUrl"`
	Timeout            time.Duration              `json:"timeout"`
	MaxRetries         int                        `json:"maxRetries"`
	DefaultModel       string                     `json:"defaultModel,omitempty"`
	SupportedModels    []string                   `json:"supportedModels,omitempty"`
	ModelMappings      map[string]string          `json:"modelMappings,omitempty"`
	Headers            map[string]string          `json:"headers,omitempty"`
	HeaderMappings     map[string]string          `json:"headerMappings,omitempty"`
	HeaderMappingRules []HeaderMappingRule `json:"headerMappingRules,omitempty"`
}

// ProviderWrapper Provider 包装器，使用 ProtocolAdapter
type ProviderWrapper struct {
	config    *ProviderConfig
	adapter   adapter.ProtocolAdapter
	tokenizer *Tokenizer
}

// NewProvider 创建新的 Provider
func NewProvider(config *ProviderConfig) (LegacyChatProvider, error) {
	if config == nil {
		return nil, fmt.Errorf("provider config is required")
	}

	// 创建 ProtocolAdapter
	a, err := adapter.NewAdapter(config.Type)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter: %w", err)
	}

	return &ProviderWrapper{
		config:    config,
		adapter:   a,
		tokenizer: NewTokenizer(providerTokenizerStrategy(config.Type)),
	}, nil
}

// Chat 执行聊天请求
func (p *ProviderWrapper) Chat(ctx context.Context, request ChatRequest) (*ChatResponse, error) {
	// 转换为 Adapter 请求
	adapterRequest := p.convertRequest(request)

	// 构建请求体
	requestBody := p.adapter.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	url := p.buildURL(p.adapter.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "provider_wrapper",
		Phase:            "request",
		Protocol:         p.config.Type,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	adapterConfig := adapter.AdapterConfig{
		Type:        p.config.Type,
		APIKey:      p.config.APIKey,
		Timeout:     p.config.Timeout,
		Model:       adapterRequest.Model,
		RequestBody: requestBody,
		Headers:     p.config.Headers,
	}
	headers := p.adapter.BuildHeaders(adapterConfig)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 发送请求
	client := &http.Client{
		Timeout: p.config.Timeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "provider_wrapper",
			Phase:    "response",
			Protocol: p.config.Type,
			Model:    adapterRequest.Model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "provider_wrapper",
			Phase:               "response",
			Protocol:            p.config.Type,
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  resp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "provider_wrapper",
		Phase:               "response",
		Protocol:            p.config.Type,
		Model:               adapterRequest.Model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  resp.StatusCode,
		ResponseBodyBytes:   len(body),
		ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
	})

	// 使用 adapter 处理响应
	onContent := func(content string) {
		if !request.Stream || content == "" {
			return
		}
		reportStreamChunk(ctx, StreamChunk{
			Type:    EventTypeText,
			Content: content,
			Metadata: map[string]interface{}{
				"provider": p.config.Type,
				"model":    adapterRequest.Model,
			},
		})
	}
	assistantMsg, err := p.adapter.HandleResponse(request.Stream, bytes.NewReader(body), onContent)
	if err != nil {
		return nil, fmt.Errorf("failed to handle response: %w", err)
	}

	// 构建响应
	response := &ChatResponse{
		ID:      fmt.Sprintf("chat_%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   request.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role:    "assistant",
					Content: assistantMsg["content"].(string),
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens:     len(request.Messages) * 10, // 简单估算
			CompletionTokens: 100,                        // 简单估算
			TotalTokens:      len(request.Messages)*10 + 100,
		},
	}

	// 提取 tool_calls
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		switch tcSlice := toolCalls.(type) {
		case []interface{}:
			if len(tcSlice) > 0 {
				response.Choices[0].Message.ToolCalls = p.convertToolCalls(tcSlice)
				response.Choices[0].FinishReason = "tool_calls"
			}
		case []map[string]interface{}:
			if len(tcSlice) > 0 {
				normalized := make([]interface{}, 0, len(tcSlice))
				for _, tc := range tcSlice {
					normalized = append(normalized, tc)
				}
				response.Choices[0].Message.ToolCalls = p.convertToolCalls(normalized)
				response.Choices[0].FinishReason = "tool_calls"
			}
		}
	}

	// 提取 reasoning_content
	if reasoning, ok := assistantMsg["reasoning_content"].(string); ok && reasoning != "" {
		response.Choices[0].Message.Reasoning = reasoning
	}

	return response, nil
}

// ChatStream 执行流式聊天请求
func (p *ProviderWrapper) ChatStream(ctx context.Context, request ChatRequest, onResponse func(ChatChunk)) error {
	// 转换为 Adapter 请求
	adapterRequest := p.convertRequest(request)
	adapterRequest.Stream = true

	// 构建请求体
	requestBody := p.adapter.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	url := p.buildURL(p.adapter.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "provider_wrapper",
		Phase:            "request",
		Protocol:         p.config.Type,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	// 创建 HTTP 请求
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	adapterConfig := adapter.AdapterConfig{
		Type:        p.config.Type,
		APIKey:      p.config.APIKey,
		Timeout:     p.config.Timeout,
		Model:       adapterRequest.Model,
		RequestBody: requestBody,
		Headers:     p.config.Headers,
	}
	headers := p.adapter.BuildHeaders(adapterConfig)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 发送请求
	client := &http.Client{
		Timeout: 0, // 流式请求不设置超时，由 Context 控制
	}
	resp, err := client.Do(req)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "provider_wrapper",
			Phase:    "response",
			Protocol: p.config.Type,
			Model:    adapterRequest.Model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	// 检查状态码
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "provider_wrapper",
			Phase:               "response",
			Protocol:            p.config.Type,
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  resp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// 使用 adapter 处理流式响应
	onContent := func(content string) {
		if onResponse != nil && content != "" {
			onResponse(ChatChunk{
				ID:      fmt.Sprintf("chat_%d", time.Now().Unix()),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   request.Model,
				Choices: []ChoiceChunk{
					{
						Index: 0,
						Delta: MessageDelta{
							Content: content,
						},
					},
				},
			})
		}
	}

	assistantMsg, err := p.adapter.HandleResponse(true, resp.Body, onContent)
	if err != nil {
		return fmt.Errorf("failed to handle stream response: %w", err)
	}

	// 发送结束块（如果完成原因不是 stop）
	if finishReason, ok := assistantMsg["finish_reason"].(string); ok && finishReason != "" {
		onResponse(ChatChunk{
			ID:      fmt.Sprintf("chat_%d", time.Now().Unix()),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   request.Model,
			Choices: []ChoiceChunk{
				{
					Index:        0,
					Delta:        MessageDelta{},
					FinishReason: finishReason,
				},
			},
		})
	}

	return nil
}

// Name 返回提供者名称
func (p *ProviderWrapper) Name() string {
	return p.adapter.Name()
}

// SupportedModels 返回支持的模型列表
func (p *ProviderWrapper) SupportedModels() []string {
	if len(p.config.SupportedModels) > 0 {
		return append([]string(nil), p.config.SupportedModels...)
	}

	// 简化实现，返回常见模型
	switch p.config.Type {
	case "openai":
		return []string{"gpt-4", "gpt-4-turbo", "gpt-3.5-turbo", "gpt-4o", "o1", "o1-mini"}
	case "anthropic":
		return []string{"claude-3-opus", "claude-3-sonnet", "claude-3-haiku", "claude-3.5-sonnet"}
	case "gemini":
		return []string{"gemini-pro", "gemini-1.5-pro"}
	case "codex":
		return []string{"gpt-4-turbo", "gpt-4o"}
	default:
		return []string{"gpt-4"}
	}
}

func (p *ProviderWrapper) resolveModel(model string) string {
	if model == "" {
		model = p.config.DefaultModel
	}
	if model == "" {
		return model
	}
	if mapped, ok := p.config.ModelMappings[model]; ok && mapped != "" {
		return mapped
	}
	if wildcard, ok := p.config.ModelMappings["*"]; ok && wildcard != "" {
		return wildcard
	}
	return model
}

// Call ???? Runtime ?????????
func (p *ProviderWrapper) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if req.Stream {
		return p.callStreamingAggregate(ctx, req)
	}

	chatResp, err := p.Chat(ctx, p.toChatRequest(req))
	if err != nil {
		return nil, err
	}

	return p.toLLMResponse(chatResp), nil
}

func (p *ProviderWrapper) callStreamingAggregate(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}

	request := p.toChatRequest(req)
	adapterRequest := p.convertRequest(request)
	adapterRequest.Stream = true

	requestBody := p.adapter.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := p.buildURL(p.adapter.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "provider_wrapper",
		Phase:            "request",
		Protocol:         p.config.Type,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	adapterConfig := adapter.AdapterConfig{
		Type:        p.config.Type,
		APIKey:      p.config.APIKey,
		Timeout:     p.config.Timeout,
		Model:       adapterRequest.Model,
		RequestBody: requestBody,
		Headers:     p.config.Headers,
	}
	headers := p.adapter.BuildHeaders(adapterConfig)
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	client := &http.Client{Timeout: 0}
	resp, err := client.Do(httpReq)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "provider_wrapper",
			Phase:    "response",
			Protocol: p.config.Type,
			Model:    adapterRequest.Model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "provider_wrapper",
			Phase:               "response",
			Protocol:            p.config.Type,
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  resp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	onContent := func(content string) {
		if content == "" {
			return
		}
		reportStreamChunk(ctx, StreamChunk{
			Type:    EventTypeText,
			Content: content,
			Metadata: map[string]interface{}{
				"provider": p.config.Type,
				"model":    adapterRequest.Model,
			},
		})
	}
	assistantMsg, err := p.adapter.HandleResponse(true, resp.Body, onContent)
	if err != nil {
		return nil, fmt.Errorf("failed to handle stream response: %w", err)
	}

	response := &ChatResponse{
		ID:      fmt.Sprintf("chat_%d", time.Now().Unix()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   request.Model,
		Choices: []Choice{
			{
				Index: 0,
				Message: Message{
					Role: "assistant",
				},
				FinishReason: "stop",
			},
		},
		Usage: Usage{
			PromptTokens: len(request.Messages) * 10,
		},
	}
	if content, ok := assistantMsg["content"].(string); ok {
		response.Choices[0].Message.Content = content
	}
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		switch tcSlice := toolCalls.(type) {
		case []interface{}:
			if len(tcSlice) > 0 {
				response.Choices[0].Message.ToolCalls = p.convertToolCalls(tcSlice)
				response.Choices[0].FinishReason = "tool_calls"
			}
		case []map[string]interface{}:
			if len(tcSlice) > 0 {
				normalized := make([]interface{}, 0, len(tcSlice))
				for _, tc := range tcSlice {
					normalized = append(normalized, tc)
				}
				response.Choices[0].Message.ToolCalls = p.convertToolCalls(normalized)
				response.Choices[0].FinishReason = "tool_calls"
			}
		}
	}
	if reasoning, ok := assistantMsg["reasoning_content"].(string); ok && reasoning != "" {
		response.Choices[0].Message.Reasoning = reasoning
	}
	return p.toLLMResponse(response), nil
}

// Stream ???? Runtime ????????
func (p *ProviderWrapper) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}

	ch := make(chan StreamChunk, 32)
	go func() {
		defer close(ch)

		sawDone := false
		err := p.ChatStream(ctx, p.toChatRequest(req), func(chunk ChatChunk) {
			for _, choice := range chunk.Choices {
				if choice.Delta.Content != "" {
					ch <- StreamChunk{Type: EventTypeText, Content: choice.Delta.Content}
				}
				if choice.FinishReason != "" {
					sawDone = true
					ch <- StreamChunk{Type: EventTypeDone, Done: true, Metadata: map[string]interface{}{"finish_reason": choice.FinishReason}}
				}
			}
		})
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamChunk{Type: EventTypeError, Error: err.Error(), Done: true}:
			}
			return
		}
		if !sawDone {
			select {
			case <-ctx.Done():
				return
			case ch <- StreamChunk{Type: EventTypeDone, Done: true}:
			}
		}
	}()

	return ch, nil
}

// CountTokens ???? Token ?
func (p *ProviderWrapper) CountTokens(text string) int {
	if p.tokenizer == nil {
		p.tokenizer = NewTokenizer(providerTokenizerStrategy(p.config.Type))
	}
	return p.tokenizer.Count(text)
}

// GetCapabilities ??????
func (p *ProviderWrapper) GetCapabilities() *ModelCapabilities {
	caps := &ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}

	switch p.config.Type {
	case "anthropic":
		caps.MaxOutputTokens = 8192
	case "gemini":
		caps.SupportsVision = true
		caps.MaxOutputTokens = 8192
	case "codex", "openai", "":
		caps.SupportsVision = true
	}

	return caps
}

// CheckHealth ?? Provider ????????
func (p *ProviderWrapper) CheckHealth(ctx context.Context) error {
	if p == nil || p.config == nil {
		return fmt.Errorf("provider config is required")
	}
	if p.adapter == nil {
		return fmt.Errorf("provider adapter is required")
	}
	return nil
}

func (p *ProviderWrapper) toChatRequest(req *LLMRequest) ChatRequest {
	messages := make([]Message, 0, len(req.Messages))
	for _, msg := range req.Messages {
		toolCalls := make([]ToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			argsBytes, _ := json.Marshal(call.Args)
			toolCalls = append(toolCalls, ToolCall{
				ID:   call.ID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      call.Name,
					Arguments: string(argsBytes),
				},
			})
		}

		messages = append(messages, Message{
			Role:       msg.Role,
			Content:    msg.Content,
			ToolCalls:  toolCalls,
			ToolCallID: msg.ToolCallID,
			Metadata:   mapFromMetadata(msg.Metadata),
		})
	}

	tools := make([]Tool, 0, len(req.Tools))
	for _, tool := range req.Tools {
		tools = append(tools, Tool{
			Type: "function",
			Function: ToolFunction{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  tool.Parameters,
			},
		})
	}

	return ChatRequest{
		Model:           req.Model,
		Messages:        messages,
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		ReasoningEffort: resolveReasoningEffort(req.ReasoningEffort, req.Metadata),
		Thinking:        resolveThinkingConfig(req.Thinking, req.Metadata),
		Stream:          req.Stream,
		Tools:           tools,
		Metadata:        req.Metadata,
	}
}

func (p *ProviderWrapper) toLLMResponse(resp *ChatResponse) *LLMResponse {
	if resp == nil {
		return nil
	}

	result := &LLMResponse{
		Model:    resp.Model,
		Metadata: resp.Metadata,
		Usage: &types.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}
	if len(resp.Choices) == 0 {
		return result
	}

	choice := resp.Choices[0]
	result.Content = choice.Message.Content
	result.Reasoning = choice.Message.Reasoning
	if result.Model == "" {
		result.Model = p.Name()
	}
	if len(choice.Message.ToolCalls) > 0 {
		result.ToolCalls = make([]types.ToolCall, 0, len(choice.Message.ToolCalls))
		for _, call := range choice.Message.ToolCalls {
			result.ToolCalls = append(result.ToolCalls, types.ToolCall{
				ID:   call.ID,
				Name: call.Function.Name,
				Args: parseToolArguments(call.Function.Arguments),
			})
		}
	}

	return result
}

func providerTokenizerStrategy(providerType string) string {
	switch providerType {
	case "openai", "codex", "gemini":
		return "openai"
	case "anthropic":
		return "anthropic"
	default:
		return "simple"
	}
}

func mapFromMetadata(metadata types.Metadata) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	result := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		result[key] = value
	}
	return result
}

// convertRequest 转换请求格式
func (p *ProviderWrapper) convertRequest(request ChatRequest) adapter.RequestConfig {
	// 转换 Messages
	messages := make([]map[string]interface{}, len(request.Messages))
	for i, msg := range request.Messages {
		messages[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if len(msg.ToolCalls) > 0 {
			messages[i]["tool_calls"] = msg.ToolCalls
		}
		if msg.ToolCallID != "" {
			messages[i]["tool_call_id"] = msg.ToolCallID
		}
		if msg.Reasoning != "" {
			messages[i]["reasoning_content"] = msg.Reasoning
		}
	}

	// 转换 Tools（从 OpenAI 嵌套格式转换为协议特定格式）
	var tools interface{}
	if len(request.Tools) > 0 {
		tools = p.convertTools(request.Tools, p.config.Type)
	}

	return adapter.RequestConfig{
		Model:           p.resolveModel(request.Model),
		Messages:        messages,
		Stream:          request.Stream,
		MaxTokens:       request.MaxTokens,
		ReasoningEffort: resolveReasoningEffort(request.ReasoningEffort, request.Metadata),
		Thinking:        resolveThinkingConfig(request.Thinking, request.Metadata),
		Temperature:     request.Temperature,
		Functions:       tools,
		Timeout:         p.config.Timeout,
	}
}

// convertTools 转换工具定义（从 OpenAI 嵌套格式转换为协议特定格式）
func (p *ProviderWrapper) convertTools(tools []Tool, protocol string) interface{} {
	if len(tools) == 0 {
		return nil
	}
	normalized := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, map[string]interface{}{
			"name":        tool.Function.Name,
			"description": tool.Function.Description,
			"parameters":  tool.Function.Parameters,
		})
	}
	return buildToolDefinitionsForProtocol(normalized, protocol, true)
}

// HandleResponse 处理 HTTP 响应
func (p *ProviderWrapper) HandleResponse(isStream bool, respBody io.Reader, onContent func(string)) (map[string]interface{}, error) {
	return p.adapter.HandleResponse(isStream, respBody, onContent)
}

// buildURL 构建完整请求 URL
func (p *ProviderWrapper) buildURL(apiPath string) string {
	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	return baseURL + "/" + strings.TrimPrefix(apiPath, "/")
}

// convertToolCalls 转换 tool_calls 格式
func (p *ProviderWrapper) convertToolCalls(tcSlice []interface{}) []ToolCall {
	result := make([]ToolCall, 0, len(tcSlice))
	for _, tc := range tcSlice {
		if tcMap, ok := tc.(map[string]interface{}); ok {
			toolCall := ToolCall{
				ID:   "", // 从 tcMap 提取
				Type: "function",
			}
			if id, ok := tcMap["id"].(string); ok {
				toolCall.ID = id
			}
			if name, ok := tcMap["name"].(string); ok {
				toolCall.Function.Name = name
			}
			if args, ok := tcMap["arguments"].(string); ok {
				toolCall.Function.Arguments = args
			}
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				toolCall.Function = ToolCallFunc{
					Name: "",
				}
				if name, ok := fn["name"].(string); ok {
					toolCall.Function.Name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					toolCall.Function.Arguments = args
				}
			}
			result = append(result, toolCall)
		}
	}
	return result
}

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/providercompat"
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
	Model                  string                 `json:"model"`
	Messages               []Message              `json:"messages"`
	MaxTokens              int                    `json:"max_tokens,omitempty"`
	Temperature            float64                `json:"temperature,omitempty"`
	ReasoningEffort        string                 `json:"reasoning_effort,omitempty"`
	ReasoningEffortBudgets map[string]int         `json:"reasoning_effort_budgets,omitempty"`
	ReasoningModel         bool                   `json:"reasoning_model,omitempty"`
	Thinking               *ThinkingConfig        `json:"thinking,omitempty"`
	Stream                 bool                   `json:"stream,omitempty"`
	Tools                  []Tool                 `json:"tools,omitempty"`
	ToolChoice             string                 `json:"tool_choice,omitempty"`
	Metadata               map[string]interface{} `json:"metadata,omitempty"`
}

// Message 消息
type Message struct {
	Role         string                 `json:"role"`
	Content      string                 `json:"content,omitempty"`
	ContentParts []types.ContentPart    `json:"content_parts,omitempty"`
	ToolCalls    []ToolCall             `json:"tool_calls,omitempty"`
	ToolCallID   string                 `json:"tool_call_id,omitempty"`
	Reasoning    string                 `json:"reasoning,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
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
	Reasoning string     `json:"reasoning,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// Usage 使用情况
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
	ReasoningTokens  int `json:"reasoning_tokens,omitempty"`
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
	Type                    string                                     `json:"type"` // openai, anthropic, gemini, codex
	APIKey                  string                                     `json:"apiKey"`
	BaseURL                 string                                     `json:"baseUrl"`
	APIPath                 string                                     `json:"apiPath,omitempty"`
	Timeout                 time.Duration                              `json:"timeout"`
	MaxRetries              int                                        `json:"maxRetries"`
	RetryTuning             RetryTuning                                `json:"retryTuning,omitempty"`
	RetryRules              []RetryRule                                `json:"retryRules,omitempty"`
	DefaultModel            string                                     `json:"defaultModel,omitempty"`
	SupportedModels         []string                                   `json:"supportedModels,omitempty"`
	ModelMappings           map[string]string                          `json:"modelMappings,omitempty"`
	ModelCapabilities       map[string]agentconfig.ModelCapabilitySpec `json:"modelCapabilities,omitempty"`
	Headers                 map[string]string                          `json:"headers,omitempty"`
	HeaderMappings          map[string]string                          `json:"headerMappings,omitempty"`
	HeaderMappingRules      []HeaderMappingRule                        `json:"headerMappingRules,omitempty"`
	SupportsMaxOutputTokens *bool                                      `json:"supportsMaxOutputTokens,omitempty"`
	Proxy                   *agentconfig.ProxyConfig                   `json:"proxy,omitempty"`
	RequestsPerMinute       int                                        `json:"requestsPerMinute,omitempty"`
}

// tokenBucketLimiter implements a simple sliding-window rate limiter.
// It tracks request timestamps and blocks when the per-minute cap is reached.
type tokenBucketLimiter struct {
	limit int
	mu    struct {
		sync.Mutex
		timestamps []time.Time
	}
}

func newTokenBucketLimiter(rpm int) *tokenBucketLimiter {
	if rpm <= 0 {
		return nil
	}
	return &tokenBucketLimiter{limit: rpm}
}

// Wait blocks until a request slot is available or ctx is cancelled.
func (l *tokenBucketLimiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		if l.tryAcquire() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (l *tokenBucketLimiter) tryAcquire() bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	windowStart := now.Add(-time.Minute)

	// evict expired entries
	valid := l.mu.timestamps[:0]
	for _, ts := range l.mu.timestamps {
		if ts.After(windowStart) {
			valid = append(valid, ts)
		}
	}
	l.mu.timestamps = valid

	if len(l.mu.timestamps) >= l.limit {
		return false
	}
	l.mu.timestamps = append(l.mu.timestamps, now)
	return true
}

// ProviderWrapper Provider 包装器，使用 ProtocolAdapter
type ProviderWrapper struct {
	config           *ProviderConfig
	adapter          adapter.ProtocolAdapter
	tokenizer        *Tokenizer
	limiter          *tokenBucketLimiter // nil when RequestsPerMinute <= 0
	httpClientMu     sync.Mutex
	httpClient       *http.Client
	streamHTTPClient *http.Client
}

// ListModelCapabilities returns a detached copy of the configured model capability map.
func (p *ProviderWrapper) ListModelCapabilities() map[string]agentconfig.ModelCapabilitySpec {
	if p == nil || p.config == nil || len(p.config.ModelCapabilities) == 0 {
		return nil
	}
	return CloneModelCapabilityMap(p.config.ModelCapabilities)
}

func (p *ProviderWrapper) modelCapabilities() map[string]agentconfig.ModelCapabilitySpec {
	if p == nil || p.config == nil {
		return nil
	}
	return providerModelCapabilitiesWithFallback(p.config.ModelCapabilities, "", p.config.Type, p.config.BaseURL)
}

// ResolveModelCapability exposes provider/model capability metadata for runtime
// features such as auto compaction.
func (p *ProviderWrapper) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	if p == nil || p.config == nil {
		return strings.TrimSpace(requestedModel), agentconfig.ModelCapabilitySpec{}, false
	}

	candidates := make([]string, 0, 3)
	appendCandidate := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		for _, existing := range candidates {
			if existing == value {
				return
			}
		}
		candidates = append(candidates, value)
	}

	requestedModel = strings.TrimSpace(requestedModel)
	appendCandidate(requestedModel)
	if requestedModel == "" {
		appendCandidate(p.config.DefaultModel)
	}
	if requestedModel != "" {
		if mapped, ok := p.config.ModelMappings[requestedModel]; ok {
			appendCandidate(mapped)
		}
	}

	modelCapabilities := p.modelCapabilities()
	for _, candidate := range candidates {
		if capability, ok := ResolveModelCapabilitySpec(candidate, modelCapabilities); ok {
			return candidate, capability, true
		}
	}

	return requestedModel, agentconfig.ModelCapabilitySpec{}, false
}

// RemoteCompact invokes a provider-native remote compaction endpoint when the
// configured protocol supports it.
func (p *ProviderWrapper) RemoteCompact(ctx context.Context, req RemoteCompactRequest) (*RemoteCompactResponse, error) {
	if p == nil || p.config == nil {
		return nil, fmt.Errorf("provider config is required")
	}
	if !strings.EqualFold(strings.TrimSpace(p.config.Type), "codex") {
		return nil, ErrRemoteCompactUnsupported
	}

	model := strings.TrimSpace(p.resolveModel(req.Model))
	requestBody := buildCodexRemoteCompactRequest(model, req.History)
	url := resolveCompactURL(p.config.BaseURL, p.config.APIPath, (&adapter.CodexAdapter{}).GetAPIPath()+"/compact")
	bodyBytes, marshalErr := json.Marshal(requestBody)
	if marshalErr != nil {
		return nil, fmt.Errorf("failed to marshal remote compact request body: %w", marshalErr)
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "provider_wrapper",
		Phase:            "request",
		Protocol:         p.config.Type,
		Model:            model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(nil, p.config.Type, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
	})

	headers := buildCodexRemoteCompactHeaders(p.config.APIKey, p.config.Timeout, requestBody, p.config.Headers)
	client := p.providerHTTPClient(false)
	responseBody, statusCode, err := sendRemoteCompactRequest(ctx, client, url, headers, requestBody)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "provider_wrapper",
			Phase:               "response",
			Protocol:            p.config.Type,
			Model:               model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  statusCode,
			ResponseBodyBytes:   len(responseBody),
			ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
			ResponseBodyRaw:     append([]byte(nil), responseBody...),
			Error:               err.Error(),
		})
		return nil, err
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "provider_wrapper",
		Phase:               "response",
		Protocol:            p.config.Type,
		Model:               model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  statusCode,
		ResponseBodyBytes:   len(responseBody),
		ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
		ResponseBodyRaw:     append([]byte(nil), responseBody...),
	})

	return decodeCodexRemoteCompactResponse(req.History, responseBody)
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

	httpClient, streamHTTPClient := newProviderWrapperHTTPClients(config)
	return &ProviderWrapper{
		config:           config,
		adapter:          a,
		tokenizer:        NewTokenizer(providerTokenizerStrategy(config.Type)),
		limiter:          newTokenBucketLimiter(config.RequestsPerMinute),
		httpClient:       httpClient,
		streamHTTPClient: streamHTTPClient,
	}, nil
}

func newProviderWrapperHTTPClients(config *ProviderConfig) (*http.Client, *http.Client) {
	var timeout time.Duration
	var proxy *agentconfig.ProxyConfig
	if config != nil {
		timeout = config.Timeout
		proxy = config.Proxy
	}

	client := newProviderHTTPClient(timeout, proxy, false)
	streamClient := &http.Client{
		Transport: client.Transport,
	}
	return client, streamClient
}

func (p *ProviderWrapper) providerHTTPClient(stream bool) *http.Client {
	if p == nil {
		return newProviderHTTPClient(0, nil, stream)
	}
	if stream {
		if p.streamHTTPClient != nil {
			return p.streamHTTPClient
		}
	} else if p.httpClient != nil {
		return p.httpClient
	}

	p.httpClientMu.Lock()
	defer p.httpClientMu.Unlock()
	if p.httpClient == nil || p.streamHTTPClient == nil {
		p.httpClient, p.streamHTTPClient = newProviderWrapperHTTPClients(p.config)
	}
	if stream {
		return p.streamHTTPClient
	}
	return p.httpClient
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
		RequestMetadata:  buildHTTPDebugRequestMetadata(request.Metadata, p.config.Type, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
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
	client := p.providerHTTPClient(false)
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
			ResponseBodyRaw:     append([]byte(nil), body...),
			Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return nil, newProviderHTTPError(resp.StatusCode, string(body), resp.Header)
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
		ResponseBodyRaw:     append([]byte(nil), body...),
	})
	if err := validateNonStreamingChatResponseBody(p.config.Type, body); err != nil {
		return nil, err
	}

	// 使用 adapter 处理响应
	callbacks := adapter.StreamCallbacks{
		OnText: func(content string) {
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
		},
		OnReasoning: func(reasoning string) {
			if !request.Stream || reasoning == "" {
				return
			}
			reportStreamChunk(ctx, StreamChunk{
				Type:    EventTypeReasoning,
				Content: reasoning,
				Metadata: map[string]interface{}{
					"provider": p.config.Type,
					"model":    adapterRequest.Model,
				},
			})
		},
		OnImage: func(metadata map[string]interface{}) {
			if !request.Stream || len(metadata) == 0 {
				return
			}
			chunkMetadata := map[string]interface{}{
				"provider": p.config.Type,
				"model":    adapterRequest.Model,
			}
			for key, value := range metadata {
				chunkMetadata[key] = value
			}
			reportStreamChunk(ctx, StreamChunk{
				Type:     EventTypeImage,
				Metadata: chunkMetadata,
			})
		},
	}
	assistantMsg, err := p.adapter.HandleResponse(request.Stream, bytes.NewReader(body), callbacks)
	if err != nil {
		return nil, fmt.Errorf("failed to handle response: %w", err)
	}
	if assistantMessageHasTruncatedToolCall(assistantMsg) {
		return nil, fmt.Errorf("truncated_tool_call: model output was truncated before completing a tool call; split long file writes into smaller chunks and retry")
	}
	if strings.EqualFold(strings.TrimSpace(p.config.Type), "codex") {
		if outputDir := strings.TrimSpace(stringValue(request.Metadata[MetadataKeyGeneratedImageOutputDir])); outputDir != "" {
			if _, imageErr := ProcessCodexAssistantImageGeneration(assistantMsg, outputDir); imageErr != nil {
				metadata := decodeMapAny(assistantMsg["metadata"])
				if metadata == nil {
					metadata = map[string]interface{}{}
				}
				metadata["generated_images_error"] = imageErr.Error()
				assistantMsg["metadata"] = metadata
			}
		}
	}

	content, _ := assistantMsg["content"].(string)
	usage, usageSource := resolveUnifiedChatTokenUsage(p.config.Type, body, assistantMsg, request.Messages, content, p.tokenizer)

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
					Content: content,
				},
				FinishReason: "stop",
			},
		},
		Usage: chatUsageFromTokenUsage(usage),
	}
	if usageSource != "" {
		response.Metadata = map[string]interface{}{
			"usage_source": usageSource,
		}
	}
	if metadata := decodeMapAny(assistantMsg["metadata"]); len(metadata) > 0 {
		response.Choices[0].Message.Metadata = cloneMapStringAny(metadata)
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

	if reasoningBlock := extractReasoningFromAssistantMessage(assistantMsg); reasoningBlock != nil {
		response.Choices[0].Message.Reasoning = reasoningBlock.DisplayText()
		if response.Metadata == nil {
			response.Metadata = make(map[string]interface{})
		}
		response.Metadata[assistantReasoningDetailsKey] = reasoningBlock.ToMap()
	} else if reasoning, ok := assistantMsg["reasoning_content"].(string); ok {
		if response.Metadata == nil {
			response.Metadata = make(map[string]interface{})
		}
		response.Metadata["reasoning_content"] = reasoning
		if reasoning != "" {
			response.Choices[0].Message.Reasoning = reasoning
		}
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
		RequestMetadata:  buildHTTPDebugRequestMetadata(request.Metadata, p.config.Type, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
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
	client := p.providerHTTPClient(true)
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
			ResponseBodyRaw:     append([]byte(nil), body...),
			Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
		})
		return newProviderHTTPError(resp.StatusCode, string(body), resp.Header)
	}

	// 使用 adapter 处理流式响应
	callbacks := adapter.StreamCallbacks{
		OnText: func(content string) {
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
		},
		OnReasoning: func(reasoning string) {
			if onResponse != nil && reasoning != "" {
				onResponse(ChatChunk{
					ID:      fmt.Sprintf("chat_%d", time.Now().Unix()),
					Object:  "chat.completion.chunk",
					Created: time.Now().Unix(),
					Model:   request.Model,
					Choices: []ChoiceChunk{
						{
							Index: 0,
							Delta: MessageDelta{
								Reasoning: reasoning,
							},
						},
					},
				})
			}
		},
	}

	var responseBuffer bytes.Buffer
	assistantMsg, err := p.adapter.HandleResponse(true, io.TeeReader(resp.Body, &responseBuffer), callbacks)
	responseBody := append([]byte(nil), responseBuffer.Bytes()...)
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "provider_wrapper",
		Phase:               "response",
		Protocol:            p.config.Type,
		Model:               adapterRequest.Model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  resp.StatusCode,
		ResponseBodyBytes:   len(responseBody),
		ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
		ResponseBodyRaw:     responseBody,
		Error:               errorString(err),
	})
	if err != nil {
		return fmt.Errorf("failed to handle stream response: %w", err)
	}
	if assistantMessageHasTruncatedToolCall(assistantMsg) {
		return fmt.Errorf("truncated_tool_call: model output was truncated before completing a tool call; split long file writes into smaller chunks and retry")
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
	if err := p.limiter.Wait(ctx); err != nil {
		return nil, fmt.Errorf("rate limit wait cancelled: %w", err)
	}
	if req.Stream {
		return p.callStreamingAggregate(ctx, req)
	}

	chatReq := p.toChatRequest(req)
	policy := newProviderRetryPolicy(p.config.MaxRetries, p.config.RetryTuning, p.config.RetryRules)
	var lastErr error
	startedAt := time.Now()
	resolvedModel := p.resolveModel(chatReq.Model)

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, policy.MaxAttempts)
		chatResp, err := p.Chat(attemptCtx, chatReq)
		if err == nil {
			if chatResp == nil {
				err = fmt.Errorf("empty provider response")
			} else if choicesErr := newEmptyProviderChoicesError(chatResp); choicesErr != nil {
				err = choicesErr
			} else if reasoningErr := newReasoningOnlyEmptyReplyError(chatResp); reasoningErr != nil {
				err = reasoningErr
			} else {
				return p.toLLMResponse(chatResp), nil
			}
		}

		lastErr = err
		retryResult, retryErr := prepareRetry(ctx, policy, startedAt, attempt, err, retryExecutionMeta{
			Source:   "provider_wrapper",
			Protocol: p.config.Type,
			Model:    resolvedModel,
		})
		if retryErr != nil {
			return nil, retryErr
		}
		if !retryResult.Decision.Retryable {
			return nil, err
		}
		if retryResult.Retry {
			continue
		}
	}

	return nil, markRetryExhausted("provider call failed after retries", policy.MaxAttempts, lastErr)
}

type emptyProviderChoicesError struct {
	model string
}

func (e *emptyProviderChoicesError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"empty_provider_choices"}
	if model := strings.TrimSpace(e.model); model != "" {
		parts = append(parts, fmt.Sprintf("model=%s", model))
	}
	return strings.Join(parts, ": ")
}

func (e *emptyProviderChoicesError) RetryErrorCode() string {
	return "empty_provider_choices"
}

func newEmptyProviderChoicesError(resp *ChatResponse) error {
	if resp == nil || len(resp.Choices) > 0 {
		return nil
	}
	return &emptyProviderChoicesError{model: resp.Model}
}

func validateNonStreamingChatResponseBody(protocol string, body []byte) error {
	if !strings.EqualFold(strings.TrimSpace(protocol), "openai") {
		return nil
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil
	}
	rawChoices, exists := payload["choices"]
	if !exists {
		return &emptyProviderChoicesError{}
	}
	if len(rawChoices) == 0 || strings.EqualFold(strings.TrimSpace(string(rawChoices)), "null") {
		return &emptyProviderChoicesError{model: stringFromRawJSON(payload["model"])}
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(rawChoices, &choices); err == nil && len(choices) == 0 {
		return &emptyProviderChoicesError{model: stringFromRawJSON(payload["model"])}
	}
	return nil
}

func stringFromRawJSON(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

type reasoningOnlyEmptyReplyError struct {
	finishReason string
}

func (e *reasoningOnlyEmptyReplyError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"reasoning_only_empty_reply"}
	if reason := strings.TrimSpace(e.finishReason); reason != "" {
		parts = append(parts, fmt.Sprintf("finish_reason=%s", reason))
	}
	parts = append(parts, "reasoning_present=true", "content_empty=true", "tool_calls=0")
	return strings.Join(parts, ": ")
}

func (e *reasoningOnlyEmptyReplyError) RetryErrorCode() string {
	return "reasoning_only_empty_reply"
}

func newReasoningOnlyEmptyReplyError(resp *ChatResponse) error {
	if resp == nil || len(resp.Choices) == 0 {
		return nil
	}

	choice := resp.Choices[0]
	message := choice.Message
	if strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0 {
		return nil
	}

	reasoning := strings.TrimSpace(message.Reasoning)
	if reasoning == "" && len(message.Metadata) > 0 {
		if metaReasoning, ok := message.Metadata["reasoning_content"].(string); ok {
			reasoning = strings.TrimSpace(metaReasoning)
		}
	}
	if reasoning == "" {
		return nil
	}

	finishReason := strings.TrimSpace(choice.FinishReason)
	if finishReason == "" {
		finishReason = strings.TrimSpace(resp.FinishReason)
	}
	return &reasoningOnlyEmptyReplyError{finishReason: finishReason}
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
	adapterConfig := adapter.AdapterConfig{
		Type:        p.config.Type,
		APIKey:      p.config.APIKey,
		Timeout:     p.config.Timeout,
		Model:       adapterRequest.Model,
		RequestBody: requestBody,
		Headers:     p.config.Headers,
	}
	headers := p.adapter.BuildHeaders(adapterConfig)

	client := p.providerHTTPClient(true)
	policy := newProviderRetryPolicy(p.config.MaxRetries, p.config.RetryTuning, p.config.RetryRules)
	var lastErr error
	startedAt := time.Now()

	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, policy.MaxAttempts)
		reportHTTPDebug(attemptCtx, HTTPDebugEvent{
			Source:           "provider_wrapper",
			Phase:            "request",
			Protocol:         p.config.Type,
			Model:            adapterRequest.Model,
			Method:           http.MethodPost,
			URL:              url,
			RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, p.config.Type, requestBody),
			RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
			RequestBodyBytes: len(bodyBytes),
			RequestBodyRaw:   append([]byte(nil), bodyBytes...),
		})

		httpReq, err := http.NewRequestWithContext(attemptCtx, "POST", url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		for key, value := range headers {
			httpReq.Header.Set(key, value)
		}

		resp, err := client.Do(httpReq)
		if err != nil {
			reportHTTPDebug(attemptCtx, HTTPDebugEvent{
				Source:   "provider_wrapper",
				Phase:    "response",
				Protocol: p.config.Type,
				Model:    adapterRequest.Model,
				Method:   http.MethodPost,
				URL:      url,
				Error:    err.Error(),
			})
			lastErr = fmt.Errorf("failed to send request: %w", err)
			retryResult, retryErr := prepareRetry(ctx, policy, startedAt, attempt, lastErr, retryExecutionMeta{
				Source:   "provider_wrapper",
				Protocol: p.config.Type,
				Model:    adapterRequest.Model,
			})
			if retryErr != nil {
				return nil, retryErr
			}
			if retryResult.Retry {
				continue
			}
			break
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			responseBody, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			reportHTTPDebug(attemptCtx, HTTPDebugEvent{
				Source:              "provider_wrapper",
				Phase:               "response",
				Protocol:            p.config.Type,
				Model:               adapterRequest.Model,
				Method:              http.MethodPost,
				URL:                 url,
				ResponseStatusCode:  resp.StatusCode,
				ResponseBodyBytes:   len(responseBody),
				ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
				ResponseBodyRaw:     append([]byte(nil), responseBody...),
				Error:               fmt.Sprintf("HTTP %d", resp.StatusCode),
			})
			lastErr = newProviderHTTPError(resp.StatusCode, string(responseBody), resp.Header)
			retryResult, retryErr := prepareRetry(ctx, policy, startedAt, attempt, lastErr, retryExecutionMeta{
				Source:   "provider_wrapper",
				Protocol: p.config.Type,
				Model:    adapterRequest.Model,
			})
			if retryErr != nil {
				return nil, retryErr
			}
			if retryResult.Retry {
				continue
			}
			break
		}

		emissionState := &streamEmissionState{}
		callbacks := adapter.StreamCallbacks{
			OnText: func(content string) {
				if content == "" {
					return
				}
				emissionState.markText(content)
				reportStreamChunk(attemptCtx, StreamChunk{
					Type:    EventTypeText,
					Content: content,
					Metadata: map[string]interface{}{
						"provider": p.config.Type,
						"model":    adapterRequest.Model,
					},
				})
			},
			OnReasoning: func(reasoning string) {
				if reasoning == "" {
					return
				}
				emissionState.markReasoning(reasoning)
				reportStreamChunk(attemptCtx, StreamChunk{
					Type:    EventTypeReasoning,
					Content: reasoning,
					Metadata: map[string]interface{}{
						"provider": p.config.Type,
						"model":    adapterRequest.Model,
					},
				})
			},
			OnImage: func(metadata map[string]interface{}) {
				if len(metadata) == 0 {
					return
				}
				emissionState.markImage(metadata)
				chunkMetadata := map[string]interface{}{
					"provider": p.config.Type,
					"model":    adapterRequest.Model,
				}
				for key, value := range metadata {
					chunkMetadata[key] = value
				}
				reportStreamChunk(attemptCtx, StreamChunk{
					Type:     EventTypeImage,
					Metadata: chunkMetadata,
				})
			},
		}

		var responseBuffer bytes.Buffer
		assistantMsg, handleErr := p.adapter.HandleResponse(true, io.TeeReader(resp.Body, &responseBuffer), callbacks)
		resp.Body.Close()
		responseBody := append([]byte(nil), responseBuffer.Bytes()...)

		if handleErr == nil {
			handleErr = validateStreamingAggregateResponse(p.config.Type, responseBody, assistantMsg)
		}
		reportHTTPDebug(attemptCtx, HTTPDebugEvent{
			Source:              "provider_wrapper",
			Phase:               "response",
			Protocol:            p.config.Type,
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  resp.StatusCode,
			ResponseBodyBytes:   len(responseBody),
			ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
			ResponseBodyRaw:     responseBody,
			Error:               errorString(handleErr),
		})

		if handleErr != nil {
			lastErr = fmt.Errorf("failed to handle stream response: %w", handleErr)
			if emissionState.emittedAnything() {
				return nil, suppressRetry(lastErr)
			}
			retryResult, retryErr := prepareRetry(ctx, policy, startedAt, attempt, lastErr, retryExecutionMeta{
				Source:   "provider_wrapper",
				Protocol: p.config.Type,
				Model:    adapterRequest.Model,
			})
			if retryErr != nil {
				return nil, retryErr
			}
			if retryResult.Retry {
				continue
			}
			break
		}

		if strings.EqualFold(strings.TrimSpace(p.config.Type), "codex") {
			if outputDir := strings.TrimSpace(stringValue(req.Metadata[MetadataKeyGeneratedImageOutputDir])); outputDir != "" {
				if _, imageErr := ProcessCodexAssistantImageGeneration(assistantMsg, outputDir); imageErr != nil {
					metadata := decodeMapAny(assistantMsg["metadata"])
					if metadata == nil {
						metadata = map[string]interface{}{}
					}
					metadata["generated_images_error"] = imageErr.Error()
					assistantMsg["metadata"] = metadata
				}
			}
		}

		content, _ := assistantMsg["content"].(string)
		usage, usageSource := resolveUnifiedChatTokenUsage(p.config.Type, responseBody, assistantMsg, request.Messages, content, p.tokenizer)

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
			Usage: chatUsageFromTokenUsage(usage),
		}
		response.Choices[0].Message.Content = content
		if usageSource != "" {
			response.Metadata = map[string]interface{}{
				"usage_source": usageSource,
			}
		}
		if metadata := decodeMapAny(assistantMsg["metadata"]); len(metadata) > 0 {
			response.Choices[0].Message.Metadata = cloneMapStringAny(metadata)
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
		if reasoningBlock := extractReasoningFromAssistantMessage(assistantMsg); reasoningBlock != nil {
			response.Choices[0].Message.Reasoning = reasoningBlock.DisplayText()
			if response.Metadata == nil {
				response.Metadata = make(map[string]interface{})
			}
			response.Metadata[assistantReasoningDetailsKey] = reasoningBlock.ToMap()
		} else if reasoning, ok := assistantMsg["reasoning_content"].(string); ok {
			if response.Metadata == nil {
				response.Metadata = make(map[string]interface{})
			}
			response.Metadata["reasoning_content"] = reasoning
			if reasoning != "" {
				response.Choices[0].Message.Reasoning = reasoning
			}
		}
		return p.toLLMResponse(response), nil
	}

	return nil, markRetryExhausted("streaming aggregate call failed after retries", policy.MaxAttempts, lastErr)
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
				if choice.Delta.Reasoning != "" {
					ch <- StreamChunk{Type: EventTypeReasoning, Content: choice.Delta.Reasoning}
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
	reasoningConfig := resolveRequestReasoningConfig(req.ReasoningEffort, req.Thinking, req.Metadata)
	resolvedModel := p.resolveModel(req.Model)
	capability, hasCapability := ResolveModelCapabilitySpec(resolvedModel, p.modelCapabilities())
	reasoningModel := ReasoningModelEnabled(capability, req.ReasoningModel)
	requestReasoningEffort := supportedProviderReasoningEffort(reasoningConfig.ReasoningEffort, capability, hasCapability)
	for _, msg := range req.Messages {
		toolCalls := make([]ToolCall, 0, len(msg.ToolCalls))
		for _, call := range msg.ToolCalls {
			argsJSON := "{}"
			if len(call.Args) > 0 {
				if argsBytes, err := json.Marshal(call.Args); err == nil && len(argsBytes) > 0 && string(argsBytes) != "null" {
					argsJSON = providercompat.NormalizeToolCallArguments(string(argsBytes))
				}
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:   call.ID,
				Type: "function",
				Function: ToolCallFunc{
					Name:      call.Name,
					Arguments: argsJSON,
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
		Model:                  req.Model,
		Messages:               messages,
		MaxTokens:              req.MaxTokens,
		Temperature:            req.Temperature,
		ReasoningEffort:        requestReasoningEffort,
		ReasoningEffortBudgets: capability.ReasoningEffortBudgets,
		ReasoningModel:         reasoningModel,
		Thinking:               reasoningConfig.Thinking,
		Stream:                 req.Stream,
		Tools:                  tools,
		Metadata:               req.Metadata,
	}
}

func (p *ProviderWrapper) toLLMResponse(resp *ChatResponse) *LLMResponse {
	if resp == nil {
		return nil
	}

	metadata := cloneMapStringAny(resp.Metadata)
	if len(metadata) == 0 {
		metadata = nil
	}
	result := &LLMResponse{
		Model:    resp.Model,
		Metadata: metadata,
		Usage: &types.TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
			CachedTokens:     resp.Usage.CachedTokens,
			ReasoningTokens:  resp.Usage.ReasoningTokens,
		},
	}
	if len(resp.Choices) == 0 {
		return result
	}

	choice := resp.Choices[0]
	if len(choice.Message.Metadata) > 0 {
		if result.Metadata == nil {
			result.Metadata = make(map[string]interface{}, len(choice.Message.Metadata))
		}
		for key, value := range choice.Message.Metadata {
			result.Metadata[key] = value
		}
	}
	result.Content = choice.Message.Content
	result.Reasoning = choice.Message.Reasoning
	if reasoningBlock := types.ReasoningBlockFromMap(result.Metadata[assistantReasoningDetailsKey]); reasoningBlock != nil {
		result.ReasoningBlock = reasoningBlock
		if result.Reasoning == "" {
			result.Reasoning = reasoningBlock.DisplayText()
		}
	}
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

func cloneMapStringAny(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return make(map[string]interface{})
	}
	output := make(map[string]interface{}, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// convertRequest 转换请求格式
func (p *ProviderWrapper) convertRequest(request ChatRequest) adapter.RequestConfig {
	metadata := cloneMapStringAny(request.Metadata)
	if strings.TrimSpace(request.ToolChoice) != "" {
		if _, exists := metadata["tool_choice"]; !exists {
			metadata["tool_choice"] = strings.TrimSpace(request.ToolChoice)
		}
	}
	resolvedModel := p.resolveModel(request.Model)
	capability, hasCapability := ResolveModelCapabilitySpec(resolvedModel, p.modelCapabilities())

	// 转换 Messages
	messages := make([]map[string]interface{}, len(request.Messages))
	providerHint := strings.TrimSpace(resolvedModel)
	for i, msg := range request.Messages {
		messages[i] = providerMessageToAdapterMessage(msg, p.config.Type, providerHint)
	}
	switch strings.ToLower(strings.TrimSpace(p.config.Type)) {
	case "codex":
		before := len(messages)
		messages = sanitizeCodexProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
		if !providerSupportsCodexMaxOutputTokens(p.config.BaseURL, p.config.SupportsMaxOutputTokens) {
			metadata[codexSupportsMaxOutputTokensMetadataKey] = false
		}
	case "openai":
		before := len(messages)
		messages = sanitizeOpenAICompatibleProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
		compat := providercompat.NewChain(providercompat.Context{
			Protocol: p.config.Type,
			BaseURL:  p.config.BaseURL,
			Model:    resolvedModel,
		})
		before = len(messages)
		messages = compat.NormalizeOpenAICompatibleMessages(messages)
		if merged := before - len(messages); merged > 0 {
			metadata["provider_compat_system_messages_merged"] = merged
		}
	case "anthropic":
		before := len(messages)
		messages = sanitizeAnthropicProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
	}

	// 转换 Tools（从 OpenAI 嵌套格式转换为协议特定格式）
	var tools interface{}
	if !metadataDisablesTools(metadata) {
		tools = p.convertTools(request.Tools, p.config.Type, resolvedModel, !metadataDisablesMetaTools(metadata))
	}

	reasoningConfig := resolveRequestReasoningConfig(request.ReasoningEffort, request.Thinking, request.Metadata)
	requestReasoningEffort := supportedProviderReasoningEffort(reasoningConfig.ReasoningEffort, capability, hasCapability)
	reasoningModel := ReasoningModelEnabled(capability, request.ReasoningModel)
	reasoningEffortBudgets := request.ReasoningEffortBudgets
	if len(reasoningEffortBudgets) == 0 && hasCapability {
		reasoningEffortBudgets = capability.ReasoningEffortBudgets
	}

	return adapter.RequestConfig{
		Model:                  resolvedModel,
		Messages:               messages,
		Stream:                 request.Stream,
		MaxTokens:              request.MaxTokens,
		ReasoningEffort:        requestReasoningEffort,
		ReasoningEffortBudgets: reasoningEffortBudgets,
		ReasoningModel:         reasoningModel,
		Thinking:               reasoningConfig.Thinking,
		Temperature:            request.Temperature,
		Functions:              tools,
		Timeout:                p.config.Timeout,
		Metadata:               metadata,
	}
}

// convertTools 转换工具定义（从 OpenAI 嵌套格式转换为协议特定格式）
func (p *ProviderWrapper) convertTools(tools []Tool, protocol string, model string, includeMeta bool) interface{} {
	normalized := make([]types.ToolDefinition, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, types.ToolDefinition{
			Name:        tool.Function.Name,
			Description: tool.Function.Description,
			Parameters:  cloneDeepMapStringAny(tool.Function.Parameters),
		})
	}
	return BuildToolDefinitionsForRequest(
		normalized,
		protocol,
		model,
		p.config.ModelCapabilities,
		includeMeta,
	)
}

// HandleResponse 处理 HTTP 响应
func (p *ProviderWrapper) HandleResponse(isStream bool, respBody io.Reader, callbacks adapter.StreamCallbacks) (map[string]interface{}, error) {
	return p.adapter.HandleResponse(isStream, respBody, callbacks)
}

// buildURL 构建完整请求 URL
func (p *ProviderWrapper) buildURL(apiPath string) string {
	baseURL := p.config.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	if configured := strings.TrimSpace(p.config.APIPath); configured != "" {
		apiPath = configured
	}
	if strings.TrimSpace(apiPath) == "" {
		return strings.TrimRight(baseURL, "/") + "/"
	}
	return agentconfig.JoinBaseURLAndPath(baseURL, apiPath)
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

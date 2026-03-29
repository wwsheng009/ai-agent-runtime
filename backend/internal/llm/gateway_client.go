package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// GatewayClient Gateway 客户端，集成 loadbalancer 选择 Provider 并调用 LLM
type GatewayClient struct {
	// loadbalancer 资源管理器
	resourceManager ResourceManager

	// 默认配置
	defaultModel   string
	defaultTimeout time.Duration
	maxRetries     int

	// HTTP 客户端
	httpClient *http.Client

	// Tokenizer 实现
	tokenizer *Tokenizer
}

// NewGatewayClient 创建新的 Gateway 客户端
// resourceManager: loadbalancer 资源管理器
// defaultModel: 默认模型
func NewGatewayClient(resourceManager ResourceManager, defaultModel string) *GatewayClient {
	return &GatewayClient{
		resourceManager: resourceManager,
		defaultModel:    defaultModel,
		defaultTimeout:  30 * time.Second,
		maxRetries:      3,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		tokenizer: NewTokenizer("openai"),
	}
}

// SetTimeout 设置默认超时
func (c *GatewayClient) SetTimeout(timeout time.Duration) {
	c.defaultTimeout = timeout
	if c.httpClient != nil {
		c.httpClient.Timeout = timeout
	}
}

// SetMaxRetries 设置最大重试次数
func (c *GatewayClient) SetMaxRetries(maxRetries int) {
	c.maxRetries = maxRetries
}

// SetHTTPClient 自定义 HTTP 客户端
func (c *GatewayClient) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// Name 返回提供者名称
func (c *GatewayClient) Name() string {
	return "gateway-client"
}

// Call 调用 LLM（非流式）
func (c *GatewayClient) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if c.resourceManager == nil {
		return nil, fmt.Errorf("resource manager not initialized")
	}

	// 设置默认模型
	model := req.Model
	if model == "" {
		model = c.defaultModel
	}

	// 选择 Provider
	retryInfo := RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      c.maxRetries,
		RequestedModel:   model,
		UseEnhancedRetry: true,
	}

	var lastError error
	for attempt := 1; attempt <= c.maxRetries; attempt++ {
		retryInfo.Attempt = attempt

		selected, err := c.resourceManager.SelectResource(retryInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to select provider: %w", err)
		}

		// 构建请求
		response, err := c.callProvider(ctx, selected, model, req)
		if err != nil {
			// 记录失败
			c.resourceManager.RecordResult(
				&SelectedResource{
					GroupName: selected.GroupName,
					Provider:  selected.Provider,
					Key:       selected.Key,
					KeyValue:  selected.KeyValue,
					KeyID:     selected.KeyID,
					Model:     selected.Model,
				},
				false, err, 0,
				0,
			)

			lastError = err

			// 更新重试信息
			if selected.GroupName != "" {
				retryInfo.TriedGroups = append(retryInfo.TriedGroups, selected.GroupName)
			}
			if selected.Provider != nil && selected.Provider.Name != "" {
				retryInfo.TriedProviders = append(retryInfo.TriedProviders, selected.Provider.Name)
			}
			if selected.KeyID != "" && selected.Provider != nil {
				if retryInfo.TriedAPIKeys == nil {
					retryInfo.TriedAPIKeys = make(map[string][]string)
				}
				retryInfo.TriedAPIKeys[selected.Provider.Name] = append(retryInfo.TriedAPIKeys[selected.Provider.Name], selected.KeyID)
			}

			// 等待后重试
			if attempt < c.maxRetries {
				backoff := time.Duration(attempt) * 100 * time.Millisecond
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(backoff):
				}
			}
			continue
		}

		// 记录成功
		c.resourceManager.RecordResult(
			&SelectedResource{
				GroupName: selected.GroupName,
				Provider:  selected.Provider,
				Key:       selected.Key,
				KeyValue:  selected.KeyValue,
				KeyID:     selected.KeyID,
				Model:     selected.Model,
			},
			true, nil, 200, 0,
		)

		return response, nil
	}

	return nil, fmt.Errorf("all retry attempts failed: %w", lastError)
}

// callProvider 调用指定的 Provider
func (c *GatewayClient) callProvider(ctx context.Context, selected *SelectedResource, model string, req *LLMRequest) (*LLMResponse, error) {
	if selected.Provider == nil {
		return nil, fmt.Errorf("selected provider is nil")
	}
	if req != nil && req.Stream {
		return c.callProviderStreamingAggregate(ctx, selected, model, req)
	}

	// 创建协议适配器
	protocol := selected.Provider.Type
	if protocol == "" {
		protocol = "openai" // 默认使用 openai 协议
	}

	adpt, err := adapter.NewAdapter(protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter: %w", err)
	}

	// 构建请求体
	adapterRequest := c.buildAdapterRequest(model, req, protocol)
	requestBody := adpt.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	baseURL := selected.Provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	apiPath := adpt.GetAPIPath()
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	// 创建 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	adaptConfig := adapter.AdapterConfig{
		Type:        protocol,
		APIKey:      selected.KeyValue,
		Timeout:     c.defaultTimeout,
		Model:       model,
		RequestBody: requestBody,
	}
	headers := adpt.BuildHeaders(adaptConfig)
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	// 发送请求
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: c.defaultTimeout}
	}

	startTime := time.Now()
	httpResp, err := client.Do(httpReq)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "gateway_client",
			Phase:    "response",
			Provider: selected.Provider.Name,
			Protocol: protocol,
			Model:    model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer httpResp.Body.Close()

	latency := time.Since(startTime)

	// 检查状态码
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "gateway_client",
			Phase:               "response",
			Provider:            selected.Provider.Name,
			Protocol:            protocol,
			Model:               model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "gateway_client",
		Phase:               "response",
		Provider:            selected.Provider.Name,
		Protocol:            protocol,
		Model:               model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  httpResp.StatusCode,
		ResponseBodyBytes:   len(body),
		ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
	})

	// 使用 adapter 处理响应
	onContent := func(content string) {
		if !req.Stream || content == "" {
			return
		}
		reportStreamChunk(ctx, StreamChunk{
			Type:    EventTypeText,
			Content: content,
			Metadata: map[string]interface{}{
				"provider": selected.Provider.Name,
				"protocol": protocol,
				"model":    model,
			},
		})
	}

	assistantMsg, err := adpt.HandleResponse(req.Stream, bytes.NewReader(body), onContent)
	if err != nil {
		return nil, fmt.Errorf("failed to handle response: %w", err)
	}

	// 构建响应
	response := &LLMResponse{
		Content: "",
		Usage: &types.TokenUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
		Model: model,
		Metadata: map[string]interface{}{
			"provider":   selected.Provider.Name,
			"latency_ms": latency.Milliseconds(),
			"protocol":   protocol,
		},
	}

	// 提取 content
	if content, ok := assistantMsg["content"].(string); ok {
		response.Content = content
	}

	// 提取 tool_calls
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		if tcSlice, ok := toolCalls.([]interface{}); ok && len(tcSlice) > 0 {
			response.ToolCalls = c.convertToolCalls(tcSlice)
		}
	}

	// 提取 reasoning_content
	if reasoning, ok := assistantMsg["reasoning_content"].(string); ok && reasoning != "" {
		response.Reasoning = reasoning
	}

	// 统计 Token
	response.Usage.PromptTokens = c.tokenizer.CountMessages(convertToInterfaceSlice(req.Messages))
	response.Usage.CompletionTokens = c.tokenizer.Count(response.Content)
	response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens

	return response, nil
}

func (c *GatewayClient) callProviderStreamingAggregate(ctx context.Context, selected *SelectedResource, model string, req *LLMRequest) (*LLMResponse, error) {
	if selected.Provider == nil {
		return nil, fmt.Errorf("selected provider is nil")
	}

	protocol := selected.Provider.Type
	if protocol == "" {
		protocol = "openai"
	}

	adpt, err := adapter.NewAdapter(protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter: %w", err)
	}

	adapterRequest := c.buildAdapterRequest(model, req, protocol)
	adapterRequest.Stream = true
	requestBody := adpt.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	baseURL := selected.Provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	apiPath := adpt.GetAPIPath()
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	adaptConfig := adapter.AdapterConfig{
		Type:        protocol,
		APIKey:      selected.KeyValue,
		Timeout:     0,
		Model:       model,
		RequestBody: requestBody,
	}
	headers := adpt.BuildHeaders(adaptConfig)
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: 0}
	}
	startTime := time.Now()
	httpResp, err := client.Do(httpReq)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "gateway_client",
			Phase:    "response",
			Provider: selected.Provider.Name,
			Protocol: protocol,
			Model:    model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}
	defer httpResp.Body.Close()

	latency := time.Since(startTime)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "gateway_client",
			Phase:               "response",
			Provider:            selected.Provider.Name,
			Protocol:            protocol,
			Model:               model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	onContent := func(content string) {
		if content == "" {
			return
		}
		reportStreamChunk(ctx, StreamChunk{
			Type:    EventTypeText,
			Content: content,
			Metadata: map[string]interface{}{
				"provider": selected.Provider.Name,
				"protocol": protocol,
				"model":    model,
			},
		})
	}
	assistantMsg, err := adpt.HandleResponse(true, httpResp.Body, onContent)
	if err != nil {
		return nil, fmt.Errorf("failed to handle stream response: %w", err)
	}

	response := &LLMResponse{
		Content: "",
		Usage: &types.TokenUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
		Model: model,
		Metadata: map[string]interface{}{
			"provider":   selected.Provider.Name,
			"latency_ms": latency.Milliseconds(),
			"protocol":   protocol,
		},
	}
	if content, ok := assistantMsg["content"].(string); ok {
		response.Content = content
	}
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		if tcSlice, ok := toolCalls.([]interface{}); ok && len(tcSlice) > 0 {
			response.ToolCalls = c.convertToolCalls(tcSlice)
		}
	}
	if reasoning, ok := assistantMsg["reasoning_content"].(string); ok && reasoning != "" {
		response.Reasoning = reasoning
	}
	response.Usage.PromptTokens = c.tokenizer.CountMessages(convertToInterfaceSlice(req.Messages))
	response.Usage.CompletionTokens = c.tokenizer.Count(response.Content)
	response.Usage.TotalTokens = response.Usage.PromptTokens + response.Usage.CompletionTokens
	return response, nil
}

// Stream 流式调用 LLM
func (c *GatewayClient) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	if c.resourceManager == nil {
		return nil, fmt.Errorf("resource manager not initialized")
	}

	// 设置默认模型
	model := req.Model
	if model == "" {
		model = c.defaultModel
	}

	// 选择 Provider
	retryInfo := RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      1, // 流式请求尽量不重试
		RequestedModel:   model,
		UseEnhancedRetry: true,
	}

	selected, err := c.resourceManager.SelectResource(retryInfo)
	if err != nil {
		return nil, fmt.Errorf("failed to select provider: %w", err)
	}

	return c.streamProvider(ctx, selected, model, req)
}

// streamProvider 流式调用指定的 Provider
func (c *GatewayClient) streamProvider(ctx context.Context, selected *SelectedResource, model string, req *LLMRequest) (<-chan StreamChunk, error) {
	if selected.Provider == nil {
		return nil, fmt.Errorf("selected provider is nil")
	}

	// 创建协议适配器
	protocol := selected.Provider.Type
	if protocol == "" {
		protocol = "openai" // 默认使用 openai 协议
	}

	adpt, err := adapter.NewAdapter(protocol)
	if err != nil {
		return nil, fmt.Errorf("failed to create adapter: %w", err)
	}

	// 构建请求体
	adapterRequest := c.buildAdapterRequest(model, req, protocol)
	adapterRequest.Stream = true
	requestBody := adpt.BuildRequest(adapterRequest)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	baseURL := selected.Provider.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	apiPath := adpt.GetAPIPath()
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            model,
		Method:           http.MethodPost,
		URL:              url,
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
	})

	// 创建 HTTP 请求
	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// 设置请求头
	adaptConfig := adapter.AdapterConfig{
		Type:        protocol,
		APIKey:      selected.KeyValue,
		Timeout:     0, // 流式请求不设超时，由 Context 控制
		Model:       model,
		RequestBody: requestBody,
	}
	headers := adpt.BuildHeaders(adaptConfig)
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	// 发送请求
	client := c.httpClient
	if client == nil {
		client = &http.Client{}
	}

	httpResp, err := client.Do(httpReq)
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:   "gateway_client",
			Phase:    "response",
			Provider: selected.Provider.Name,
			Protocol: protocol,
			Model:    model,
			Method:   http.MethodPost,
			URL:      url,
			Error:    err.Error(),
		})
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// 检查状态码
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "gateway_client",
			Phase:               "response",
			Provider:            selected.Provider.Name,
			Protocol:            protocol,
			Model:               model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(body))
	}

	// 创建流式响应通道
	ch := make(chan StreamChunk, 100)

	// 启动 goroutine 处理流式响应
	go func() {
		defer httpResp.Body.Close()
		defer close(ch)

		// 构建回调函数
		onContent := func(content string) {
			if content != "" {
				ch <- StreamChunk{
					Type:    EventTypeText,
					Content: content,
					Metadata: map[string]interface{}{
						"provider": selected.Provider.Name,
						"protocol": protocol,
						"model":    model,
					},
				}
			}
		}

		// 使用 adapter 处理流式响应
		_, err := adpt.HandleResponse(true, httpResp.Body, onContent)
		if err != nil {
			ch <- StreamChunk{
				Type:  EventTypeError,
				Error: err.Error(),
			}
		}

		// 发送结束块
		ch <- StreamChunk{
			Type: EventTypeError, // 使用 Error 类型表示流结束
		}
	}()

	return ch, nil
}

// CountTokens 统计 Token 数
func (c *GatewayClient) CountTokens(text string) int {
	if c.tokenizer == nil {
		c.tokenizer = NewTokenizer("openai")
	}
	return c.tokenizer.Count(text)
}

// GetCapabilities 获取模型能力
func (c *GatewayClient) GetCapabilities() *ModelCapabilities {
	return &ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsVision:    false,
		SupportsTools:     true,
		SupportsStreaming: true,
		SupportsJSONMode:  true,
	}
}

// CheckHealth 检查提供者健康状况
func (c *GatewayClient) CheckHealth(ctx context.Context) error {
	if c.resourceManager == nil {
		return fmt.Errorf("resource manager not initialized")
	}

	// 尝试选择一个 Provider
	retryInfo := RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      1,
		RequestedModel:   c.defaultModel,
		UseEnhancedRetry: true,
	}

	_, err := c.resourceManager.SelectResource(retryInfo)
	return err
}

// buildAdapterRequest 构建 Adapter 请求
func (c *GatewayClient) buildAdapterRequest(model string, req *LLMRequest, protocol string) adapter.RequestConfig {
	// 转换 Messages
	messages := make([]map[string]interface{}, len(req.Messages))
	for i, msg := range req.Messages {
		messages[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if msg.ToolCallID != "" {
			messages[i]["tool_call_id"] = msg.ToolCallID
		}
	}

	// 转换 Tools（根据协议生成正确格式）
	var tools interface{}
	if len(req.Tools) > 0 {
		tools = c.convertTools(req.Tools, protocol)
	}

	return adapter.RequestConfig{
		Model:           model,
		Messages:        messages,
		Stream:          req.Stream,
		MaxTokens:       req.MaxTokens,
		ReasoningEffort: resolveReasoningEffort(req.ReasoningEffort, req.Metadata),
		Thinking:        resolveThinkingConfig(req.Thinking, req.Metadata),
		Temperature:     req.Temperature,
		Functions:       tools,
		Timeout:         c.defaultTimeout,
	}
}

// convertTools 转换工具定义（根据协议类型生成正确格式）
// protocol: "openai" | "anthropic" | "codex" | "gemini"
func (c *GatewayClient) convertTools(tools []types.ToolDefinition, protocol string) interface{} {
	if len(tools) == 0 {
		return nil
	}
	normalized := make([]map[string]interface{}, 0, len(tools))
	for _, tool := range tools {
		normalized = append(normalized, map[string]interface{}{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	return buildToolDefinitionsForProtocol(normalized, protocol, true)
}

// convertToolCalls 转换 tool_calls 格式
func (c *GatewayClient) convertToolCalls(tcSlice []interface{}) []types.ToolCall {
	result := make([]types.ToolCall, 0, len(tcSlice))
	for _, tc := range tcSlice {
		if tcMap, ok := tc.(map[string]interface{}); ok {
			toolCall := types.ToolCall{}
			if id, ok := tcMap["id"].(string); ok {
				toolCall.ID = id
			}
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok {
					toolCall.Name = name
				}
				if args, ok := fn["arguments"].(string); ok {
					toolCall.Args = parseToolArguments(args)
				}
			}
			result = append(result, toolCall)
		}
	}
	return result
}

// parseToolCalls 解析工具调用参数
func parseToolCallArgs(argsStr string) map[string]interface{} {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return make(map[string]interface{})
	}
	return args
}

// parseToolArguments 解析工具参数
func parseToolArguments(argsStr string) map[string]interface{} {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsStr), &args); err != nil {
		return make(map[string]interface{})
	}
	return args
}

// SupportedModels 返回支持的模型列表
func (c *GatewayClient) SupportedModels() []string {
	// 从 loadbalancer 获取所有支持的模型
	// 这里返回一个默认列表
	return []string{
		"gpt-4", "gpt-4-turbo", "gpt-4o", "gpt-4o-mini",
		"claude-3-opus", "claude-3-sonnet", "claude-3.5-sonnet",
		"gemini-pro", "gemini-1.5-pro",
	}
}

// convertToInterfaceSlice 将 []types.Message 转换为 []interface{}
func convertToInterfaceSlice(messages []types.Message) []interface{} {
	result := make([]interface{}, len(messages))
	for i, msg := range messages {
		result[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
		}
		if msg.ToolCallID != "" {
			result[i].(map[string]interface{})["tool_call_id"] = msg.ToolCallID
		}
	}
	return result
}

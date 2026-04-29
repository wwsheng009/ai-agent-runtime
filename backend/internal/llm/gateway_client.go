package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
	retryTuning    RetryTuning
	retryRules     []RetryRule

	// HTTP 客户端
	httpClient *http.Client

	// Tokenizer 实现
	tokenizer *Tokenizer
}

type gatewayProviderError struct {
	message    string
	statusCode int
	retryable  bool
	retryAfter time.Duration
	cause      error
}

func (e *gatewayProviderError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *gatewayProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func (e *gatewayProviderError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

func (e *gatewayProviderError) RetryAfterDelay() time.Duration {
	if e == nil {
		return 0
	}
	return e.retryAfter
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

// SetRetryTuning 设置重试退避参数
func (c *GatewayClient) SetRetryTuning(tuning RetryTuning) {
	c.retryTuning = tuning
}

// SetRetryRules 设置细粒度重试规则
func (c *GatewayClient) SetRetryRules(rules []RetryRule) {
	c.retryRules = cloneRetryRules(rules)
}

// SetHTTPClient 自定义 HTTP 客户端
func (c *GatewayClient) SetHTTPClient(client *http.Client) {
	c.httpClient = client
}

// Name 返回提供者名称
func (c *GatewayClient) Name() string {
	return "gateway-client"
}

// ResolveModelCapability resolves capabilities for the provider resource that
// would serve the requested model on the next gateway request.
func (c *GatewayClient) ResolveModelCapability(requestedModel string) (string, agentconfig.ModelCapabilitySpec, bool) {
	if c == nil || c.resourceManager == nil {
		return strings.TrimSpace(requestedModel), agentconfig.ModelCapabilitySpec{}, false
	}

	model := strings.TrimSpace(requestedModel)
	if model == "" {
		model = strings.TrimSpace(c.defaultModel)
	}
	if model == "" {
		return "", agentconfig.ModelCapabilitySpec{}, false
	}
	maxAttempts := c.maxRetries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	selected, err := c.resourceManager.SelectResource(RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      maxAttempts,
		RequestedModel:   model,
		UseEnhancedRetry: true,
	})
	if err != nil || selected == nil {
		return model, agentconfig.ModelCapabilitySpec{}, false
	}

	resolvedModel := resolveGatewaySelectedModel(selected, model)
	capability, ok := ResolveModelCapabilitySpec(resolvedModel, selectedProviderModelCapabilities(selected))
	if !ok && resolvedModel != model {
		capability, ok = ResolveModelCapabilitySpec(model, selectedProviderModelCapabilities(selected))
		if ok {
			resolvedModel = model
		}
	}
	return strings.TrimSpace(resolvedModel), capability, ok
}

// RemoteCompact invokes a selected provider-native remote compaction endpoint.
func (c *GatewayClient) RemoteCompact(ctx context.Context, req RemoteCompactRequest) (*RemoteCompactResponse, error) {
	if c == nil || c.resourceManager == nil {
		return nil, fmt.Errorf("resource manager not initialized")
	}

	model := strings.TrimSpace(req.Model)
	if model == "" {
		model = strings.TrimSpace(c.defaultModel)
	}
	maxAttempts := c.maxRetries
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	selected, err := c.resourceManager.SelectResource(RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      maxAttempts,
		RequestedModel:   model,
		UseEnhancedRetry: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to select provider: %w", err)
	}
	if selected == nil || selected.Provider == nil {
		return nil, fmt.Errorf("selected provider is nil")
	}

	protocol := strings.TrimSpace(selected.Provider.Type)
	if protocol == "" {
		protocol = "openai"
	}
	if !strings.EqualFold(protocol, "codex") {
		return nil, ErrRemoteCompactUnsupported
	}

	resolvedModel := resolveGatewaySelectedModel(selected, model)
	requestBody := buildCodexRemoteCompactRequest(resolvedModel, req.History)
	url := resolveCompactURL(selected.Provider.BaseURL, selected.Provider.APIPath, (&adapter.CodexAdapter{}).GetAPIPath()+"/compact")
	bodyBytes, marshalErr := json.Marshal(requestBody)
	if marshalErr != nil {
		return nil, fmt.Errorf("failed to marshal remote compact request body: %w", marshalErr)
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            resolvedModel,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(nil, protocol, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
	})

	headers := buildCodexRemoteCompactHeaders(selected.KeyValue, c.defaultTimeout, requestBody, nil)
	client := c.httpClient
	if client == nil {
		client = &http.Client{Timeout: c.defaultTimeout}
	}

	startTime := time.Now()
	responseBody, statusCode, err := sendRemoteCompactRequest(ctx, client, url, headers, requestBody)
	latencyMs := time.Since(startTime).Milliseconds()
	if err != nil {
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "gateway_client",
			Phase:               "response",
			Provider:            selected.Provider.Name,
			Protocol:            protocol,
			Model:               resolvedModel,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  statusCode,
			ResponseBodyBytes:   len(responseBody),
			ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
			ResponseBodyRaw:     append([]byte(nil), responseBody...),
			Error:               err.Error(),
		})
		c.resourceManager.RecordResult(selected, false, err, statusCode, latencyMs)
		return nil, err
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "gateway_client",
		Phase:               "response",
		Provider:            selected.Provider.Name,
		Protocol:            protocol,
		Model:               resolvedModel,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  statusCode,
		ResponseBodyBytes:   len(responseBody),
		ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
		ResponseBodyRaw:     append([]byte(nil), responseBody...),
	})

	response, decodeErr := decodeCodexRemoteCompactResponse(req.History, responseBody)
	if decodeErr != nil {
		c.resourceManager.RecordResult(selected, false, decodeErr, statusCode, latencyMs)
		return nil, decodeErr
	}
	c.resourceManager.RecordResult(selected, true, nil, statusCode, latencyMs)
	return response, nil
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
	policy := newProviderRetryPolicy(c.maxRetries, c.retryTuning, c.retryRules)
	startedAt := time.Now()
	retryInfo := RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      policy.MaxAttempts,
		RequestedModel:   model,
		UseEnhancedRetry: true,
	}

	var lastError error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, policy.MaxAttempts)
		retryInfo.Attempt = attempt

		selected, err := c.resourceManager.SelectResource(retryInfo)
		if err != nil {
			return nil, fmt.Errorf("failed to select provider: %w", err)
		}

		// 构建请求
		response, err := c.callProvider(attemptCtx, selected, model, req)
		if err != nil {
			statusCode := gatewayProviderErrorStatusCode(err)
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
				false, err, statusCode,
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

			if !isRetryableGatewayProviderError(err) {
				return nil, err
			}

			retryResult, retryErr := prepareRetry(ctx, policy, startedAt, attempt, err, retryExecutionMeta{
				Source:   "gateway_client",
				Provider: gatewaySelectedProviderName(selected),
				Protocol: gatewaySelectedProviderProtocol(selected),
				Model:    resolveGatewaySelectedModel(selected, model),
			})
			if retryErr != nil {
				return nil, retryErr
			}
			if retryResult.Retry {
				continue
			}
			break
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

	return nil, markRetryExhausted("all retry attempts failed", policy.MaxAttempts, lastError)
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
	adapterRequest := c.buildAdapterRequest(model, req, selected, protocol)
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
	apiPath := resolveProviderAPIPath(selected.Provider, adpt.GetAPIPath())
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
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
		Model:       adapterRequest.Model,
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
			Model:    adapterRequest.Model,
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
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			ResponseBodyRaw:     append([]byte(nil), body...),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, newGatewayHTTPError(httpResp.StatusCode, string(body), httpResp.Header, c.retryRules)
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
		Model:               adapterRequest.Model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  httpResp.StatusCode,
		ResponseBodyBytes:   len(body),
		ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
		ResponseBodyRaw:     append([]byte(nil), body...),
	})

	// 使用 adapter 处理响应
	callbacks := adapter.StreamCallbacks{
		OnText: func(content string) {
			if !req.Stream || content == "" {
				return
			}
			reportStreamChunk(ctx, StreamChunk{
				Type:    EventTypeText,
				Content: content,
				Metadata: map[string]interface{}{
					"provider": selected.Provider.Name,
					"protocol": protocol,
					"model":    adapterRequest.Model,
				},
			})
		},
		OnReasoning: func(reasoning string) {
			if !req.Stream || reasoning == "" {
				return
			}
			reportStreamChunk(ctx, StreamChunk{
				Type:    EventTypeReasoning,
				Content: reasoning,
				Metadata: map[string]interface{}{
					"provider": selected.Provider.Name,
					"protocol": protocol,
					"model":    adapterRequest.Model,
				},
			})
		},
		OnImage: func(metadata map[string]interface{}) {
			if !req.Stream || len(metadata) == 0 {
				return
			}
			chunkMetadata := map[string]interface{}{
				"provider": selected.Provider.Name,
				"protocol": protocol,
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

	assistantMsg, err := adpt.HandleResponse(req.Stream, bytes.NewReader(body), callbacks)
	if err != nil {
		return nil, newGatewayResponseError("failed to handle response", err, c.retryRules)
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
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
	reasoningBlock := extractReasoningFromAssistantMessage(assistantMsg)
	usage, usageSource := resolveUnifiedTokenUsage(protocol, body, assistantMsg, req.Messages, stringValue(assistantMsg["content"]), c.tokenizer)

	// 构建响应
	response := &LLMResponse{
		Content: "",
		Usage:   usage,
		Model:   adapterRequest.Model,
		Metadata: map[string]interface{}{
			"provider":     selected.Provider.Name,
			"latency_ms":   latency.Milliseconds(),
			"protocol":     protocol,
			"usage_source": usageSource,
		},
	}

	// 提取 content
	if content, ok := assistantMsg["content"].(string); ok {
		response.Content = content
	}
	if metadata := decodeMapAny(assistantMsg["metadata"]); len(metadata) > 0 {
		for key, value := range metadata {
			response.Metadata[key] = value
		}
	}

	// 提取 tool_calls
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		if tcSlice := normalizeGatewayToolCalls(toolCalls); len(tcSlice) > 0 {
			response.ToolCalls = c.convertToolCalls(tcSlice)
		}
	}

	if reasoningBlock != nil {
		response.ReasoningBlock = reasoningBlock
		response.Reasoning = reasoningBlock.DisplayText()
		response.Metadata[assistantReasoningDetailsKey] = reasoningBlock.ToMap()
	} else if reasoning, ok := assistantMsg["reasoning_content"].(string); ok {
		response.Metadata["reasoning_content"] = reasoning
		if reasoning != "" {
			response.Reasoning = reasoning
		}
	}

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

	adapterRequest := c.buildAdapterRequest(model, req, selected, protocol)
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
	apiPath := resolveProviderAPIPath(selected.Provider, adpt.GetAPIPath())
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
	})

	httpReq, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	adaptConfig := adapter.AdapterConfig{
		Type:        protocol,
		APIKey:      selected.KeyValue,
		Timeout:     0,
		Model:       adapterRequest.Model,
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
			Model:    adapterRequest.Model,
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
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			ResponseBodyRaw:     append([]byte(nil), body...),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, newGatewayHTTPError(httpResp.StatusCode, string(body), httpResp.Header, c.retryRules)
	}

	emissionState := &streamEmissionState{}
	callbacks := adapter.StreamCallbacks{
		OnText: func(content string) {
			if content == "" {
				return
			}
			emissionState.markText(content)
			reportStreamChunk(ctx, StreamChunk{
				Type:    EventTypeText,
				Content: content,
				Metadata: map[string]interface{}{
					"provider": selected.Provider.Name,
					"protocol": protocol,
					"model":    adapterRequest.Model,
				},
			})
		},
		OnReasoning: func(reasoning string) {
			if reasoning == "" {
				return
			}
			emissionState.markReasoning(reasoning)
			reportStreamChunk(ctx, StreamChunk{
				Type:    EventTypeReasoning,
				Content: reasoning,
				Metadata: map[string]interface{}{
					"provider": selected.Provider.Name,
					"protocol": protocol,
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
				"provider": selected.Provider.Name,
				"protocol": protocol,
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
	var responseBuffer bytes.Buffer
	assistantMsg, err := adpt.HandleResponse(true, io.TeeReader(httpResp.Body, &responseBuffer), callbacks)
	responseBody := append([]byte(nil), responseBuffer.Bytes()...)
	if err == nil {
		err = validateStreamingAggregateResponse(protocol, responseBody, assistantMsg)
	}
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:              "gateway_client",
		Phase:               "response",
		Provider:            selected.Provider.Name,
		Protocol:            protocol,
		Model:               adapterRequest.Model,
		Method:              http.MethodPost,
		URL:                 url,
		ResponseStatusCode:  httpResp.StatusCode,
		ResponseBodyBytes:   len(responseBody),
		ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
		ResponseBodyRaw:     responseBody,
		Error:               errorString(err),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if emissionState.emittedAnything() {
			return nil, &gatewayProviderError{
				message:   fmt.Sprintf("failed to handle stream response: %v", err),
				retryable: false,
				cause:     err,
			}
		}
		return nil, newGatewayResponseError("failed to handle stream response", err, c.retryRules)
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
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
	reasoningBlock := extractReasoningFromAssistantMessage(assistantMsg)
	usage, usageSource := resolveUnifiedTokenUsage(protocol, responseBody, assistantMsg, req.Messages, stringValue(assistantMsg["content"]), c.tokenizer)

	response := &LLMResponse{
		Content: "",
		Usage:   usage,
		Model:   adapterRequest.Model,
		Metadata: map[string]interface{}{
			"provider":     selected.Provider.Name,
			"latency_ms":   latency.Milliseconds(),
			"protocol":     protocol,
			"usage_source": usageSource,
		},
	}
	if content, ok := assistantMsg["content"].(string); ok {
		response.Content = content
	}
	if metadata := decodeMapAny(assistantMsg["metadata"]); len(metadata) > 0 {
		for key, value := range metadata {
			response.Metadata[key] = value
		}
	}
	if toolCalls, ok := assistantMsg["tool_calls"]; ok {
		if tcSlice := normalizeGatewayToolCalls(toolCalls); len(tcSlice) > 0 {
			response.ToolCalls = c.convertToolCalls(tcSlice)
		}
	}
	if reasoningBlock != nil {
		response.ReasoningBlock = reasoningBlock
		response.Reasoning = reasoningBlock.DisplayText()
		response.Metadata[assistantReasoningDetailsKey] = reasoningBlock.ToMap()
	} else if reasoning, ok := assistantMsg["reasoning_content"].(string); ok {
		response.Metadata["reasoning_content"] = reasoning
		if reasoning != "" {
			response.Reasoning = reasoning
		}
	}
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
	adapterRequest := c.buildAdapterRequest(model, req, selected, protocol)
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
	apiPath := resolveProviderAPIPath(selected.Provider, adpt.GetAPIPath())
	url := baseURL + "/" + apiPath
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, requestBody),
		RequestBody:      truncateHTTPDebugText(string(bodyBytes), 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   append([]byte(nil), bodyBytes...),
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
		Model:       adapterRequest.Model,
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
			Model:    adapterRequest.Model,
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
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(body),
			ResponseBodyPreview: truncateHTTPDebugText(string(body), 4096),
			ResponseBodyRaw:     append([]byte(nil), body...),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, newGatewayHTTPError(httpResp.StatusCode, string(body), httpResp.Header, c.retryRules)
	}

	// 创建流式响应通道
	ch := make(chan StreamChunk, 100)

	// 启动 goroutine 处理流式响应
	go func() {
		defer httpResp.Body.Close()
		defer close(ch)
		var responseBuffer bytes.Buffer

		callbacks := adapter.StreamCallbacks{
			OnText: func(content string) {
				if content != "" {
					ch <- StreamChunk{
						Type:    EventTypeText,
						Content: content,
						Metadata: map[string]interface{}{
							"provider": selected.Provider.Name,
							"protocol": protocol,
							"model":    adapterRequest.Model,
						},
					}
				}
			},
			OnReasoning: func(reasoning string) {
				if reasoning != "" {
					ch <- StreamChunk{
						Type:    EventTypeReasoning,
						Content: reasoning,
						Metadata: map[string]interface{}{
							"provider": selected.Provider.Name,
							"protocol": protocol,
							"model":    adapterRequest.Model,
						},
					}
				}
			},
			OnImage: func(metadata map[string]interface{}) {
				if len(metadata) == 0 {
					return
				}
				chunkMetadata := map[string]interface{}{
					"provider": selected.Provider.Name,
					"protocol": protocol,
					"model":    adapterRequest.Model,
				}
				for key, value := range metadata {
					chunkMetadata[key] = value
				}
				ch <- StreamChunk{
					Type:     EventTypeImage,
					Metadata: chunkMetadata,
				}
			},
		}

		// 使用 adapter 处理流式响应
		_, err := adpt.HandleResponse(true, io.TeeReader(httpResp.Body, &responseBuffer), callbacks)
		responseBody := append([]byte(nil), responseBuffer.Bytes()...)
		reportHTTPDebug(ctx, HTTPDebugEvent{
			Source:              "gateway_client",
			Phase:               "response",
			Provider:            selected.Provider.Name,
			Protocol:            protocol,
			Model:               adapterRequest.Model,
			Method:              http.MethodPost,
			URL:                 url,
			ResponseStatusCode:  httpResp.StatusCode,
			ResponseBodyBytes:   len(responseBody),
			ResponseBodyPreview: truncateHTTPDebugText(string(responseBody), 4096),
			ResponseBodyRaw:     responseBody,
			Error:               errorString(err),
		})
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
func (c *GatewayClient) buildAdapterRequest(model string, req *LLMRequest, selected *SelectedResource, protocol string) adapter.RequestConfig {
	resolvedModel := resolveGatewaySelectedModel(selected, model)
	metadata := cloneMapStringAny(req.Metadata)
	modelCapabilities := selectedProviderModelCapabilities(selected)
	capability, _ := ResolveModelCapabilitySpec(resolvedModel, modelCapabilities)
	reasoningModel := ReasoningModelEnabled(capability, req.ReasoningModel)

	// 转换 Messages
	messages := make([]map[string]interface{}, len(req.Messages))
	providerHint := strings.TrimSpace(req.Provider)
	if providerHint == "" {
		providerHint = strings.TrimSpace(resolvedModel)
	}
	for i, msg := range req.Messages {
		messages[i] = runtimeMessageToAdapterMessage(msg, protocol, providerHint)
	}

	// 转换 Tools（根据协议生成正确格式）
	var tools interface{}
	if !metadataDisablesTools(metadata) {
		tools = c.convertTools(
			req.Tools,
			protocol,
			resolvedModel,
			selectedProviderModelCapabilities(selected),
			!metadataDisablesMetaTools(metadata),
		)
	}
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "codex":
		before := len(messages)
		messages = sanitizeCodexProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
		if !selectedProviderSupportsCodexMaxOutputTokens(selected) {
			metadata[codexSupportsMaxOutputTokensMetadataKey] = false
		}
	case "openai":
		before := len(messages)
		messages = sanitizeOpenAICompatibleProtocolMessages(messages)
		if dropped := before - len(messages); dropped > 0 {
			metadata["tool_replay_sanitized"] = true
			metadata["tool_replay_dropped_messages"] = dropped
		}
	}

	reasoningConfig := resolveRequestReasoningConfig(req.ReasoningEffort, req.Thinking, req.Metadata)

	return adapter.RequestConfig{
		Model:                  resolvedModel,
		Messages:               messages,
		Stream:                 req.Stream,
		MaxTokens:              req.MaxTokens,
		ReasoningEffort:        reasoningConfig.ReasoningEffort,
		ReasoningEffortBudgets: capability.ReasoningEffortBudgets,
		ReasoningModel:         reasoningModel,
		Thinking:               reasoningConfig.Thinking,
		Temperature:            req.Temperature,
		Functions:              tools,
		Timeout:                c.defaultTimeout,
		Metadata:               metadata,
	}
}

// convertTools 转换工具定义（根据协议类型生成正确格式）
// protocol: "openai" | "anthropic" | "codex" | "gemini"
func (c *GatewayClient) convertTools(
	tools []types.ToolDefinition,
	protocol string,
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
	includeMeta bool,
) interface{} {
	return BuildToolDefinitionsForRequest(tools, protocol, model, modelCapabilities, includeMeta)
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

func normalizeGatewayToolCalls(raw interface{}) []interface{} {
	switch tcSlice := raw.(type) {
	case []interface{}:
		return tcSlice
	case []map[string]interface{}:
		normalized := make([]interface{}, 0, len(tcSlice))
		for _, tc := range tcSlice {
			normalized = append(normalized, tc)
		}
		return normalized
	default:
		return nil
	}
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
		if strings.TrimSpace(argsStr) == "" {
			return make(map[string]interface{})
		}
		return map[string]interface{}{"_raw": argsStr}
	}
	return args
}

func resolveProviderAPIPath(provider *ProviderResource, defaultPath string) string {
	if provider != nil {
		if configured := strings.TrimSpace(provider.APIPath); configured != "" {
			return strings.TrimPrefix(configured, "/")
		}
	}
	return strings.TrimPrefix(defaultPath, "/")
}

func resolveGatewaySelectedModel(selected *SelectedResource, requestedModel string) string {
	if selected != nil {
		if resolved := strings.TrimSpace(selected.Model); resolved != "" {
			return resolved
		}
		if selected.Provider != nil {
			switch cfg := selected.Provider.Config.(type) {
			case *agentconfig.Provider:
				if resolved := strings.TrimSpace(agentconfig.ApplyModelMapping(cfg, requestedModel)); resolved != "" {
					return resolved
				}
			case agentconfig.Provider:
				if resolved := strings.TrimSpace(agentconfig.ApplyModelMapping(&cfg, requestedModel)); resolved != "" {
					return resolved
				}
			}
		}
	}
	return strings.TrimSpace(requestedModel)
}

func selectedProviderModelCapabilities(selected *SelectedResource) map[string]agentconfig.ModelCapabilitySpec {
	if selected == nil || selected.Provider == nil {
		return nil
	}
	if len(selected.Provider.ModelCapabilities) > 0 {
		return selected.Provider.ModelCapabilities
	}
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		return cfg.ModelCapabilities
	case agentconfig.Provider:
		return cfg.ModelCapabilities
	default:
		return nil
	}
}

func newGatewayHTTPError(statusCode int, body string, header http.Header, rules []RetryRule) error {
	providerErr := &gatewayProviderError{
		message:    fmt.Sprintf("HTTP %d: %s", statusCode, body),
		statusCode: statusCode,
	}
	var ok bool
	providerErr.retryAfter, ok = retryAfterDelayFromHeader(header, time.Time{})
	if !ok {
		providerErr.retryAfter, _ = retryAfterDelayFromBody(body)
	}
	decision := classifyRetryableLLMErrorWithRules(providerErr, rules)
	providerErr.retryable = decision.Retryable
	return providerErr
}

func newGatewayResponseError(prefix string, err error, rules []RetryRule) error {
	message := prefix
	if err != nil {
		message = fmt.Sprintf("%s: %v", prefix, err)
	}
	decision := classifyRetryableLLMErrorWithRules(err, rules)
	providerErr := &gatewayProviderError{
		message:   message,
		retryable: decision.Retryable,
		cause:     err,
	}
	providerErr.retryAfter, _ = errorRetryAfterDelay(err)
	return providerErr
}

func gatewayProviderErrorStatusCode(err error) int {
	var providerErr *gatewayProviderError
	if errors.As(err, &providerErr) {
		return providerErr.statusCode
	}
	return 0
}

func isRetryableGatewayProviderError(err error) bool {
	if err == nil {
		return false
	}
	var providerErr *gatewayProviderError
	if errors.As(err, &providerErr) {
		return providerErr.retryable
	}
	return true
}

func isRetryableGatewayHTTPStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooManyRequests:
		return true
	}
	return statusCode >= 500
}

func isRetryableGatewayResponseError(err error) bool {
	return isRetryableProviderResponseError(err)
}

func gatewaySelectedProviderName(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	return selected.Provider.Name
}

func gatewaySelectedProviderProtocol(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	return strings.TrimSpace(selected.Provider.Type)
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

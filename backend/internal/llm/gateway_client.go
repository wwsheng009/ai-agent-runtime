package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	runtimehttpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolargs"
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

	// streamReadTimeout 流式读取的空闲超时；<=0 表示不启用（默认）。
	// 只对"该有数据却没有数据"的空闲窗口生效，不影响持续产出数据的长任务。
	streamReadTimeout time.Duration

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
			Timeout:   30 * time.Second,
			Transport: runtimehttpclient.WithDefaultUserAgent(http.DefaultTransport),
		},
		tokenizer: NewTokenizer("openai"),
	}
}

// DefaultModelName returns the gateway-level default model.
func (c *GatewayClient) DefaultModelName() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.defaultModel)
}

// SetTimeout 设置默认超时
func (c *GatewayClient) SetTimeout(timeout time.Duration) {
	c.defaultTimeout = timeout
	if c.httpClient != nil {
		c.httpClient.Timeout = timeout
	}
}

// SetStreamReadTimeout 设置流式读取的空闲超时。<=0 表示不启用。
// 只对"该有数据却没有数据"的空闲窗口生效，不影响持续产出数据的长任务。
func (c *GatewayClient) SetStreamReadTimeout(timeout time.Duration) {
	c.streamReadTimeout = timeout
}

// StreamReadTimeout 返回当前流式读取空闲超时（0 表示未启用）。
func (c *GatewayClient) StreamReadTimeout() time.Duration {
	if c == nil {
		return 0
	}
	return c.streamReadTimeout
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
	if !ok {
		if fallback := c.GetCapabilities(); fallback != nil && fallback.MaxContextTokens > 0 {
			capability = agentconfig.ModelCapabilitySpec{
				MaxContextTokens: fallback.MaxContextTokens,
				MaxTokens:        fallback.MaxOutputTokens,
			}
			ok = true
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
	requestBody := buildCodexRemoteCompactRequest(resolvedModel, req.History, req.SessionID)
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
		RequestMetadata:  buildHTTPDebugRequestMetadata(nil, protocol, compatibilityProfileFromSelected(selected), requestBody),
		RequestBody:      truncateHTTPDebugBytes(bodyBytes, 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   boundHTTPDebugRawBody(bodyBytes),
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
			ResponseBodyPreview: truncateHTTPDebugBytes(responseBody, 4096),
			ResponseBodyRaw:     boundHTTPDebugRawBody(responseBody),
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
		ResponseBodyPreview: truncateHTTPDebugBytes(responseBody, 4096),
		ResponseBodyRaw:     boundHTTPDebugRawBody(responseBody),
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
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	ctx = withHTTPDebugRequestMetadata(ctx, req.Metadata)

	// 设置默认模型
	model := req.Model
	if model == "" {
		model = c.defaultModel
	}

	// 选择 Provider
	policy := newProviderRetryPolicy(c.maxRetries, 0, c.retryTuning, c.retryRules)
	policy = applyRequestRetryPolicy(policy, req.Metadata)
	startedAt := time.Now()
	activeMaxAttempts := policy.initialMaxAttempts()
	retryInfo := RetryInfo{
		TargetGroup:      "default",
		Attempt:          1,
		MaxAttempts:      activeMaxAttempts,
		RequestedModel:   model,
		UseEnhancedRetry: true,
	}

	var lastError error
	maxTokensRecovered := false
	for attempt := 1; retryAttemptAllowed(policy.MaxAttempts, attempt); attempt++ {
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, activeMaxAttempts)
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
			// Deterministic max_tokens ceiling rejections can be repaired once
			// without rotating providers, using the provider-reported limit.
			if !maxTokensRecovered && applyMaxTokensLimitRecovery(&req.MaxTokens, err) {
				maxTokensRecovered = true
				if activeMaxAttempts < attempt+1 {
					activeMaxAttempts = attempt + 1
				}
				if policy.MaxAttempts > 0 && policy.MaxAttempts < activeMaxAttempts {
					policy.MaxAttempts = activeMaxAttempts
				}
				retryInfo.MaxAttempts = activeMaxAttempts
				continue
			}
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

			retryResult, retryErr := prepareRetry(attemptCtx, policy, startedAt, attempt, err, retryExecutionMeta{
				Source:        "gateway_client",
				Provider:      gatewaySelectedProviderName(selected),
				Protocol:      gatewaySelectedProviderProtocol(selected),
				Model:         resolveGatewaySelectedModel(selected, model),
				PartialOutput: errHasPartialOutput(err),
			})
			if retryErr != nil {
				return nil, retryErr
			}
			activeMaxAttempts = retryResult.MaxAttempts
			retryInfo.MaxAttempts = activeMaxAttempts
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

	// Transport/stream-class failures (SSE stream EOF, connection drops)
	// are handed to the enclosing runtime loop instead of becoming terminal:
	// the gateway fast-fails on its own budget, the runtime owns the total
	// retry guarantee across providers/routes.
	if isHandoffEligibleError(lastError) {
		return nil, markRetryExhaustedForNextLayer("all retry attempts failed", policy.MaxAttempts, lastError)
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
	requestBody = prepareGatewayRequestBody(selected, protocol, adapterRequest.Model, requestBody)
	propagatePromptCacheKey(requestBody, req.Metadata, protocol)
	scopeGatewayPromptCacheKey(requestBody, selected, protocol, adapterRequest.Model)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	url := buildGatewayProviderURL(selected.Provider, adpt.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, compatibilityProfileFromSelected(selected), requestBody),
		RequestBody:      truncateHTTPDebugBytes(bodyBytes, 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   boundHTTPDebugRawBody(bodyBytes),
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
			ResponseBodyPreview: truncateHTTPDebugBytes(body, 4096),
			ResponseBodyRaw:     boundHTTPDebugRawBody(body),
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
		ResponseBodyPreview: truncateHTTPDebugBytes(body, 4096),
		ResponseBodyRaw:     boundHTTPDebugRawBody(body),
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
	assistantMsg = normalizeGatewayAssistantMessage(selected, protocol, adapterRequest.Model, assistantMsg)
	if err := validateAssistantMessageSemantics(assistantMsg); err != nil {
		return nil, newGatewayResponseError("invalid assistant response", err, c.retryRules)
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
		if outputDir := strings.TrimSpace(stringValue(req.Metadata[MetadataKeyGeneratedImageOutputDir])); outputDir != "" {
			if _, imageErr := ProcessCodexAssistantImageGenerationWithOptions(assistantMsg, outputDir, CodexImageGenerationOptionsFromMetadata(req.Metadata)); imageErr != nil {
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
		Content:      "",
		Usage:        usage,
		Model:        adapterRequest.Model,
		FinishReason: assistantMessageFinishReason(assistantMsg),
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
			if response.FinishReason == "stop" {
				response.FinishReason = "tool_calls"
			}
		}
	}
	response.Metadata["finish_reason"] = response.FinishReason

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
	requestBody = prepareGatewayRequestBody(selected, protocol, adapterRequest.Model, requestBody)
	propagatePromptCacheKey(requestBody, req.Metadata, protocol)
	scopeGatewayPromptCacheKey(requestBody, selected, protocol, adapterRequest.Model)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := buildGatewayProviderURL(selected.Provider, adpt.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, compatibilityProfileFromSelected(selected), requestBody),
		RequestBody:      truncateHTTPDebugBytes(bodyBytes, 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   boundHTTPDebugRawBody(bodyBytes),
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
			ResponseBodyPreview: truncateHTTPDebugBytes(body, 4096),
			ResponseBodyRaw:     boundHTTPDebugRawBody(body),
			Error:               fmt.Sprintf("HTTP %d", httpResp.StatusCode),
		})
		return nil, newGatewayHTTPError(httpResp.StatusCode, string(body), httpResp.Header, c.retryRules)
	}

	// Guard the streaming read against upstreams that stop producing bytes:
	// a stuck connection would otherwise block the whole turn forever. Only
	// idle (no data at all) trips the timeout; slow-but-active streams keep
	// refreshing it, so long-running legitimate tasks are unaffected.
	if c.streamReadTimeout > 0 {
		httpResp.Body = wrapStreamIdleTimeout(httpResp.Body, c.streamReadTimeout)
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
	streamReader := normalizeGatewayStreamReadCloser(selected, protocol, adapterRequest.Model, newTeeReadCloser(httpResp.Body, &responseBuffer))
	defer streamReader.Close()
	assistantMsg, err := adpt.HandleResponse(true, streamReader, callbacks)
	_ = streamReader.Close()
	responseBody := append([]byte(nil), responseBuffer.Bytes()...)
	if err == nil {
		assistantMsg = normalizeGatewayAssistantMessage(selected, protocol, adapterRequest.Model, assistantMsg)
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
		ResponseBodyPreview: truncateHTTPDebugBytes(responseBody, 4096),
		ResponseBodyRaw:     boundHTTPDebugRawBody(responseBody),
		Error:               errorString(err),
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Partial-output replay policy: transient failures keep retrying even
		// when partial text was already emitted (duplicated partial output is
		// accepted; the llm.retry event carries the partial_output marker).
		// Only non-retryable causes stay suppressed after emission.
		if emissionState.emittedAnything() {
			err = withPartialOutputMarker(err)
			if mustSuppressRetryAfterEmission(err) {
				return nil, &gatewayProviderError{
					message:   fmt.Sprintf("failed to handle stream response: %v", err),
					retryable: false,
					cause:     err,
				}
			}
		}
		return nil, newGatewayResponseError("failed to handle stream response", err, c.retryRules)
	}
	if strings.EqualFold(strings.TrimSpace(protocol), "codex") {
		if outputDir := strings.TrimSpace(stringValue(req.Metadata[MetadataKeyGeneratedImageOutputDir])); outputDir != "" {
			if _, imageErr := ProcessCodexAssistantImageGenerationWithOptions(assistantMsg, outputDir, CodexImageGenerationOptionsFromMetadata(req.Metadata)); imageErr != nil {
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
		Content:      "",
		Usage:        usage,
		Model:        adapterRequest.Model,
		FinishReason: assistantMessageFinishReason(assistantMsg),
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
			if response.FinishReason == "stop" {
				response.FinishReason = "tool_calls"
			}
		}
	}
	response.Metadata["finish_reason"] = response.FinishReason
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
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	ctx = withHTTPDebugRequestMetadata(ctx, req.Metadata)

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
	requestBody = prepareGatewayRequestBody(selected, protocol, adapterRequest.Model, requestBody)
	propagatePromptCacheKey(requestBody, req.Metadata, protocol)
	scopeGatewayPromptCacheKey(requestBody, selected, protocol, adapterRequest.Model)
	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	// 构建请求 URL
	url := buildGatewayProviderURL(selected.Provider, adpt.GetAPIPath())
	reportHTTPDebug(ctx, HTTPDebugEvent{
		Source:           "gateway_client",
		Phase:            "request",
		Provider:         selected.Provider.Name,
		Protocol:         protocol,
		Model:            adapterRequest.Model,
		Method:           http.MethodPost,
		URL:              url,
		RequestMetadata:  buildHTTPDebugRequestMetadata(req.Metadata, protocol, compatibilityProfileFromSelected(selected), requestBody),
		RequestBody:      truncateHTTPDebugBytes(bodyBytes, 32768),
		RequestBodyBytes: len(bodyBytes),
		RequestBodyRaw:   boundHTTPDebugRawBody(bodyBytes),
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
			ResponseBodyPreview: truncateHTTPDebugBytes(body, 4096),
			ResponseBodyRaw:     boundHTTPDebugRawBody(body),
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
		streamReader := normalizeGatewayStreamReadCloser(selected, protocol, adapterRequest.Model, newTeeReadCloser(httpResp.Body, &responseBuffer))
		defer streamReader.Close()
		assistantMsg, err := adpt.HandleResponse(true, streamReader, callbacks)
		_ = streamReader.Close()
		responseBody := append([]byte(nil), responseBuffer.Bytes()...)
		if err == nil {
			assistantMsg = normalizeGatewayAssistantMessage(selected, protocol, adapterRequest.Model, assistantMsg)
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
			ResponseBodyPreview: truncateHTTPDebugBytes(responseBody, 4096),
			ResponseBodyRaw:     boundHTTPDebugRawBody(responseBody),
			Error:               errorString(err),
		})
		if err != nil {
			ch <- StreamChunk{
				Type:  EventTypeError,
				Error: err.Error(),
				Done:  true,
			}
			return
		}

		finishReason := assistantMessageFinishReason(assistantMsg)
		ch <- StreamChunk{
			Type:     EventTypeDone,
			Done:     true,
			Metadata: map[string]interface{}{"finish_reason": finishReason},
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
		MaxContextTokens:  DefaultContextWindowTokens,
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

// propagatePromptCacheKey copies the stable caller key into adapters
// (notably OpenAI chat, whose adapter deliberately does not copy arbitrary
// metadata). Gateway callers scope the copied key to the selected provider
// generation afterwards; direct provider calls keep the stable key unchanged.
func propagatePromptCacheKey(requestBody map[string]interface{}, metadata map[string]interface{}, protocol string) {
	if requestBody == nil || !gatewayPromptCacheKeySupported(protocol) {
		return
	}
	if existing, _ := requestBody["prompt_cache_key"].(string); strings.TrimSpace(existing) != "" {
		return
	}
	for _, metadataKey := range []string{"prompt_cache_key", "session_id", "conversation_id"} {
		value, ok := metadata[metadataKey]
		if !ok || value == nil {
			continue
		}
		key := strings.TrimSpace(fmt.Sprint(value))
		if key == "" || key == "<nil>" {
			continue
		}
		requestBody["prompt_cache_key"] = key
		return
	}
}

func gatewayPromptCacheKeySupported(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "openai", "codex":
		return true
	default:
		return false
	}
}

// scopeGatewayPromptCacheKey binds the caller's stable session key to the
// selected provider generation and the final wire tool surface. Gateway retry
// may select another protocol/model/capability set; that is an explicit cache
// generation change rather than silently reusing one key with different tools.
// tool_choice is intentionally excluded so compact requests (choice=none) keep
// sharing the same tools prefix as normal turns.
func scopeGatewayPromptCacheKey(requestBody map[string]interface{}, selected *SelectedResource, protocol, model string) {
	if requestBody == nil || !gatewayPromptCacheKeySupported(protocol) {
		return
	}
	original, _ := requestBody["prompt_cache_key"].(string)
	original = strings.TrimSpace(original)
	if original == "" {
		return
	}

	providerName := ""
	baseURL := ""
	apiPath := ""
	if selected != nil && selected.Provider != nil {
		providerName = strings.TrimSpace(selected.Provider.Name)
		baseURL = strings.TrimSpace(selected.Provider.BaseURL)
		apiPath = strings.TrimSpace(selected.Provider.APIPath)
	}
	scope := struct {
		Original     string      `json:"original"`
		ProviderName string      `json:"provider_name,omitempty"`
		BaseURL      string      `json:"base_url,omitempty"`
		APIPath      string      `json:"api_path,omitempty"`
		Protocol     string      `json:"protocol"`
		Model        string      `json:"model"`
		Tools        interface{} `json:"tools,omitempty"`
	}{
		Original:     original,
		ProviderName: providerName,
		BaseURL:      baseURL,
		APIPath:      apiPath,
		Protocol:     strings.ToLower(strings.TrimSpace(protocol)),
		Model:        strings.TrimSpace(model),
		Tools:        requestBody["tools"],
	}
	encoded, err := json.Marshal(scope)
	if err != nil {
		return
	}
	digest := sha256.Sum256(encoded)
	requestBody["prompt_cache_key"] = "gw_" + hex.EncodeToString(digest[:])[:60]
}

// buildAdapterRequest 构建 Adapter 请求
func (c *GatewayClient) buildAdapterRequest(model string, req *LLMRequest, selected *SelectedResource, protocol string) adapter.RequestConfig {
	resolvedModel := resolveGatewaySelectedModel(selected, model)
	metadata := cloneMapStringAny(req.Metadata)
	modelCapabilities := selectedProviderModelCapabilities(selected)

	messages := make([]map[string]interface{}, len(req.Messages))
	providerHint := strings.TrimSpace(req.Provider)
	if providerHint == "" {
		providerHint = strings.TrimSpace(resolvedModel)
	}
	for i, msg := range req.Messages {
		messages[i] = runtimeMessageToAdapterMessage(msg, protocol, providerHint, resolvedModel)
	}

	var supportsMaxOutputTokens *bool
	if selected != nil && selected.Provider != nil {
		supportsMaxOutputTokens = selected.Provider.SupportsMaxOutputTokens
	}

	return buildProviderAdapterRequest(providerAdapterRequestInput{
		ProviderName:            providerNameFromSelected(selected),
		Protocol:                protocol,
		BaseURL:                 baseURLFromSelected(selected),
		APIPath:                 apiPathFromSelected(selected),
		CompatibilityProfile:    compatibilityProfileFromSelected(selected),
		Model:                   resolvedModel,
		SupportsMaxOutputTokens: supportsMaxOutputTokens,
		ModelCapabilities:       modelCapabilities,
		EnableImageGeneration:   enableImageGenerationFromSelected(selected),
		Messages:                messages,
		Tools:                   req.Tools,
		Metadata:                metadata,
		ReasoningEffort:         req.ReasoningEffort,
		ReasoningModel:          req.ReasoningModel,
		Thinking:                req.Thinking,
		Stream:                  req.Stream,
		MaxTokens:               req.MaxTokens,
		Temperature:             req.Temperature,
		Timeout:                 c.defaultTimeout,
	})
}

// convertTools 转换工具定义（根据协议类型生成正确格式）
// protocol: "openai" | "anthropic" | "codex" | "gemini"
//
// enableNativeImageGeneration must be provided by the caller from the provider
// enable_image_generation flag; model capabilities alone never auto-enable it.
func (c *GatewayClient) convertTools(
	tools []types.ToolDefinition,
	protocol string,
	model string,
	modelCapabilities map[string]agentconfig.ModelCapabilitySpec,
	includeMeta bool,
	enableNativeImageGeneration bool,
) interface{} {
	return BuildToolDefinitionsForRequest(tools, protocol, model, modelCapabilities, includeMeta, enableNativeImageGeneration)
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
			if toolCall.ID == "" {
				toolCall.ID, _ = tcMap["call_id"].(string)
			}
			toolCall.Type, _ = tcMap["type"].(string)
			toolCall.Name, _ = tcMap["name"].(string)
			if strings.EqualFold(strings.TrimSpace(toolCall.Type), "custom_tool_call") {
				toolCall.RawInput, _ = tcMap["input"].(string)
				if toolCall.RawInput == "" {
					toolCall.RawInput, _ = tcMap["arguments"].(string)
				}
				toolCall.Args = toolargs.DecodeFreeform(toolCall.RawInput)
			}
			if fn, ok := tcMap["function"].(map[string]interface{}); ok {
				if name, ok := fn["name"].(string); ok {
					toolCall.Name = name
				}
				if args, ok := fn["arguments"].(string); ok && toolCall.Args == nil {
					toolCall.Args = parseToolArguments(args)
				}
			}
			if toolCall.Args == nil {
				if args, ok := tcMap["arguments"].(string); ok {
					toolCall.Args = parseToolArguments(args)
				} else {
					toolCall.Args = map[string]interface{}{}
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

// parseToolArguments 解析工具参数
func parseToolArguments(argsStr string) map[string]interface{} {
	return toolargs.DecodeJSON(argsStr)
}

func resolveProviderAPIPath(provider *ProviderResource, defaultPath string) string {
	if provider != nil {
		if configured := strings.TrimSpace(provider.APIPath); configured != "" {
			return strings.TrimPrefix(configured, "/")
		}
	}
	return strings.TrimPrefix(defaultPath, "/")
}

func buildGatewayProviderURL(provider *ProviderResource, defaultPath string) string {
	baseURL := ""
	if provider != nil {
		baseURL = provider.BaseURL
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://api.openai.com"
	}
	apiPath := resolveProviderAPIPath(provider, defaultPath)
	if strings.TrimSpace(apiPath) == "" {
		return strings.TrimRight(baseURL, "/") + "/"
	}
	return agentconfig.JoinBaseURLAndPath(baseURL, apiPath)
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
	providerName := selectedProviderName(selected)
	protocol := strings.TrimSpace(selected.Provider.Type)
	baseURL := selectedProviderBaseURL(selected)
	modelCapabilities := selected.Provider.ModelCapabilities
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		if protocol == "" {
			protocol = cfg.GetProtocol()
		}
		if baseURL == "" {
			baseURL = cfg.BaseURL
		}
		if len(modelCapabilities) == 0 {
			modelCapabilities = cfg.ModelCapabilities
		}
	case agentconfig.Provider:
		if protocol == "" {
			protocol = cfg.GetProtocol()
		}
		if baseURL == "" {
			baseURL = cfg.BaseURL
		}
		if len(modelCapabilities) == 0 {
			modelCapabilities = cfg.ModelCapabilities
		}
	}
	return providerModelCapabilitiesWithFallback(modelCapabilities, providerName, protocol, baseURL)
}

func enableImageGenerationFromSelected(selected *SelectedResource) *bool {
	if selected == nil || selected.Provider == nil {
		return nil
	}
	if selected.Provider.EnableImageGeneration != nil {
		return selected.Provider.EnableImageGeneration
	}
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		return cfg.EnableImageGeneration
	case agentconfig.Provider:
		return cfg.EnableImageGeneration
	}
	return nil
}

func providerNameFromSelected(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	return selectedProviderName(selected)
}

func selectedProviderName(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	return strings.TrimSpace(selected.Provider.Name)
}

func baseURLFromSelected(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	return selectedProviderBaseURL(selected)
}

func selectedProviderBaseURL(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	baseURL := strings.TrimSpace(selected.Provider.BaseURL)
	if baseURL != "" {
		return baseURL
	}
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		return strings.TrimSpace(cfg.BaseURL)
	case agentconfig.Provider:
		return strings.TrimSpace(cfg.BaseURL)
	}
	return ""
}

func apiPathFromSelected(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	apiPath := strings.TrimSpace(selected.Provider.APIPath)
	if apiPath != "" {
		return apiPath
	}
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		return strings.TrimSpace(cfg.APIPath)
	case agentconfig.Provider:
		return strings.TrimSpace(cfg.APIPath)
	}
	return ""
}

func compatibilityProfileFromSelected(selected *SelectedResource) string {
	if selected == nil || selected.Provider == nil {
		return ""
	}
	profile := strings.TrimSpace(selected.Provider.CompatibilityProfile)
	if profile != "" {
		return profile
	}
	switch cfg := selected.Provider.Config.(type) {
	case *agentconfig.Provider:
		return strings.TrimSpace(cfg.Compatibility.Profile)
	case agentconfig.Provider:
		return strings.TrimSpace(cfg.Compatibility.Profile)
	}
	return ""
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

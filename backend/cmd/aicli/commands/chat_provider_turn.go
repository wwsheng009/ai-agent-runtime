package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

type aicliProviderTurnExecutor struct {
	session        *ChatSession
	exposureReport *aicliFunctionExposureReport
	exposureLogged bool
	nextScope      func() aicliLogScope
}

func (e *aicliProviderTurnExecutor) Complete(ctx context.Context, req runtimechatcore.ProviderTurnRequest) (*runtimechatcore.ProviderTurnResponse, error) {
	if e == nil || e.session == nil {
		return nil, fmt.Errorf("chat session is not configured")
	}
	session := e.session
	if ctx == nil {
		ctx = context.Background()
	}
	if session.IsInterrupted() {
		return nil, fmt.Errorf("用户中断")
	}

	logScope := aicliLogScope{}
	if e.nextScope != nil {
		logScope = e.nextScope()
	}

	protocolMessages := runtimellm.RuntimeMessagesToProtocolMessages(req.Messages, session.Provider.GetProtocol(), session.ProviderName, session.Model)
	config := adapterRequestConfig(session, protocolMessages, req)
	requestBody := session.Adapter.BuildRequest(config)

	bodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		return nil, fmt.Errorf("序列化请求失败: %w", err)
	}

	logpkg.Info("AICLI chat request",
		logpkg.String("provider", session.ProviderName),
		logpkg.String("protocol", session.Provider.GetProtocol()),
		logpkg.String("model", session.Model),
		logpkg.URL(session.BaseURL),
		logpkg.Bool("stream", req.Stream),
		logpkg.Int("messages", len(protocolMessages)),
		logpkg.Int("body_bytes", len(bodyBytes)),
	)

	if session.Logger != nil && session.Logger.logDir != "" {
		exposureReport := e.exposureReport
		if e.exposureLogged {
			exposureReport = nil
		} else if exposureReport != nil {
			e.exposureLogged = true
		}
		session.Logger.LogRequest(logScope, buildRequestLogContent(session.BaseURL, requestBody, exposureReport))
	}

	httpReq, err := http.NewRequest(http.MethodPost, session.BaseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %w", err)
	}
	httpReq = httpReq.WithContext(ctx)

	headers := session.Adapter.BuildHeaders(adapterAdapterConfig(session))
	for key, value := range headers {
		httpReq.Header.Set(key, value)
	}

	startTime := time.Now()

	retryCfg := session.RetryConfig
	if req.Stream {
		retryCfg.Streaming = true
	}
	if shouldRenderInteractiveOutput(session) && session.Interaction != nil {
		retryCfg.RetryNotice = func(notice string) {
			session.Interaction.RefreshStatus("Retrying")
			session.Interaction.RenderAsyncLine(notice)
		}
	}

	resp, responseBody, httpReport, err := sendHTTPRequest(session.HTTPClient, httpReq, retryCfg)
	if err != nil {
		durationMs := time.Since(startTime).Milliseconds()
		logpkg.Error("AICLI chat request failed",
			logpkg.String("provider", session.ProviderName),
			logpkg.String("protocol", session.Provider.GetProtocol()),
			logpkg.String("model", session.Model),
			logpkg.URL(session.BaseURL),
			logpkg.Err(err),
		)
		if session.HTTPDebug && session.Logger != nil && session.Logger.logDir != "" {
			if writeErr := session.Logger.WriteDebugInfo(session.Logger.logDir, formatHTTPDebugReport(httpReq, bodyBytes, httpReport)); writeErr != nil {
				fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", writeErr)
			}
		}
		if session.Logger != nil && session.Logger.logDir != "" {
			content := map[string]interface{}{}
			if httpReport != nil {
				content["http_debug"] = httpReport
			}
			if len(content) == 0 {
				content = nil
			}
			session.Logger.LogResponse(logScope, content, responseBody, req.Stream, err, durationMs)
		}
		return nil, fmt.Errorf("请求失败: %w", err)
	}

	if session.HTTPDebug && session.Logger != nil && session.Logger.logDir != "" {
		if writeErr := session.Logger.WriteDebugInfo(session.Logger.logDir, formatHTTPDebugReport(httpReq, bodyBytes, httpReport)); writeErr != nil {
			fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", writeErr)
		}
	}

	responseBytes := len(responseBody)
	needStreamBody := req.Stream && responseBody == nil && resp.Body != nil

	logpkg.Info("AICLI chat response received",
		logpkg.String("provider", session.ProviderName),
		logpkg.String("protocol", session.Provider.GetProtocol()),
		logpkg.String("model", session.Model),
		logpkg.URL(session.BaseURL),
		logpkg.Status(resp.StatusCode),
		logpkg.Latency(time.Since(startTime).Milliseconds()),
		logpkg.Int("response_bytes", responseBytes),
	)

	if resp.StatusCode != http.StatusOK {
		durationMs := time.Since(startTime).Milliseconds()
		logpkg.Warn("AICLI chat response non-200",
			logpkg.String("provider", session.ProviderName),
			logpkg.String("protocol", session.Provider.GetProtocol()),
			logpkg.String("model", session.Model),
			logpkg.URL(session.BaseURL),
			logpkg.Status(resp.StatusCode),
		)
		if session.Logger != nil && session.Logger.logDir != "" {
			session.Logger.LogResponse(logScope, nil, responseBody, req.Stream, fmt.Errorf("HTTP %d", resp.StatusCode), durationMs)
		}
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(responseBody))
	}

	callbacks := adapter.StreamCallbacks{}
	if req.Stream {
		callbacks = adapter.StreamCallbacks{
			OnText: func(content string) {
				if session.IsInterrupted() {
					return
				}
				if req.EventSink != nil {
					req.EventSink(runtimechatcore.ChatEvent{
						Type:    runtimechatcore.EventResult,
						Content: content,
					})
				}
			},
			OnReasoning: func(reasoning string) {
				if session.IsInterrupted() {
					return
				}
				if req.EventSink != nil {
					req.EventSink(runtimechatcore.ChatEvent{
						Type:    runtimechatcore.EventPlanning,
						Content: reasoning,
					})
				}
			},
		}
	}

	var respReader io.Reader
	var streamCapture bytes.Buffer
	if needStreamBody {
		respReader = io.TeeReader(resp.Body, &streamCapture)
	} else {
		respReader = bytes.NewReader(responseBody)
	}

	assistantMsg, err := session.Adapter.HandleResponse(req.Stream, io.NopCloser(respReader), callbacks)

	if needStreamBody {
		resp.Body.Close()
	}

	if session.IsInterrupted() {
		return nil, fmt.Errorf("用户中断")
	}
	if err != nil {
		if session.Logger != nil && session.Logger.logDir != "" {
			capturedBody := responseBody
			if capturedBody == nil {
				capturedBody = streamCapture.Bytes()
			}
			session.Logger.LogResponse(logScope, nil, capturedBody, req.Stream, err, time.Since(startTime).Milliseconds())
		}
		return nil, fmt.Errorf("响应处理失败: %w", err)
	}

	if needStreamBody {
		responseBody = streamCapture.Bytes()
	}

	if strings.EqualFold(strings.TrimSpace(session.Provider.GetProtocol()), "codex") {
		_, imageErr := runtimellm.ProcessCodexAssistantImageGeneration(
			assistantMsg,
			currentGeneratedImageArtifactDir(session),
		)
		if imageErr != nil {
			logpkg.Warnf("AICLI codex image_generation save failed: %v", imageErr)
			if req.EventSink != nil {
				req.EventSink(runtimechatcore.ChatEvent{
					Type:    runtimechatcore.EventWarning,
					Content: fmt.Sprintf("保存生成图片失败: %v", imageErr),
				})
			}
		}
	}

	content, _ := assistantMsg["content"].(string)
	reasoning, _ := assistantMsg["reasoning_content"].(string)
	rawToolCalls, hasToolCalls := assistantMsg["tool_calls"].([]map[string]interface{})

	finishReason, _ := assistantMsg["finish_reason"].(string)
	if finishReason == "length" {
		warnMsg := "[输出因 token 限制被截断，可配置 max_token / max_tokens_limit 增大上限]"
		if req.EventSink != nil && shouldRenderInteractiveOutput(session) {
			req.EventSink(runtimechatcore.ChatEvent{
				Type:    runtimechatcore.EventWarning,
				Content: warnMsg,
			})
		}
		logpkg.Warn("AICLI response truncated by token limit",
			logpkg.String("provider", session.ProviderName),
			logpkg.String("model", session.Model),
			logpkg.String("finish_reason", finishReason),
		)
	}
	if responseHasTruncatedToolCalls(assistantMsg) {
		return nil, fmt.Errorf("模型输出在工具调用前被 token 限制截断，请缩短写入内容后重试")
	}

	if session.Logger != nil && session.Logger.logDir != "" {
		logContent := map[string]interface{}{"streamed": req.Stream}
		if content != "" {
			logContent["content"] = content
		}
		if reasoning != "" {
			logContent["reasoning_content"] = reasoning
		}
		if hasToolCalls {
			logContent["tool_calls"] = rawToolCalls
		}
		if usage := extractUsageFromResponseBody(responseBody); len(usage) > 0 {
			logContent["usage"] = usage
		}
		if session.HTTPDebug && httpReport != nil {
			logContent["http_debug"] = httpReport
		}
		session.Logger.LogResponse(logScope, logContent, responseBody, req.Stream, nil, time.Since(startTime).Milliseconds())
	}

	runtimeMessage, err := runtimeMessageFromAICLIMessage(assistantMsg)
	if err != nil {
		return nil, fmt.Errorf("构建共享 assistant 消息失败: %w", err)
	}
	if reasoning != "" {
		runtimeMessage.Metadata.Set(chatcoreReasoningMetadataKey, reasoning)
	}

	usage := tokenUsageFromResponseBody(responseBody)
	if usage != nil && usage.TotalTokens > 0 {
		session.TokenCount += usage.TotalTokens
		if session.Interaction != nil {
			session.Interaction.RefreshStatus("")
		}
	}

	return &runtimechatcore.ProviderTurnResponse{
		Message: &runtimeMessage,
		Usage:   usage,
	}, nil
}

func responseHasTruncatedToolCalls(msg map[string]interface{}) bool {
	if len(msg) == 0 {
		return false
	}
	content := payloadStringValue(msg["content"])
	if hasIncompleteToolCallMarkup(content) {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(payloadStringValue(msg["finish_reason"])), "length") {
		return false
	}
	return len(normalizeMapSlice(msg["tool_calls"])) > 0
}

func hasIncompleteToolCallMarkup(content string) bool {
	if !strings.Contains(content, "<tool_call>") {
		return false
	}
	return !strings.Contains(content, "</tool_call>")
}

func resolveMaxTokens(session *ChatSession) int {
	if limit := session.Provider.GetMaxTokensLimit(); limit > 0 {
		return limit
	}
	switch strings.ToLower(strings.TrimSpace(session.Provider.GetProtocol())) {
	case "anthropic":
		return 8192
	case "gemini":
		return 8192
	default:
		return 4096
	}
}

func adapterRequestConfig(session *ChatSession, messages []map[string]interface{}, req runtimechatcore.ProviderTurnRequest) adapter.RequestConfig {
	config := adapter.RequestConfig{
		Model:           session.Model,
		Messages:        messages,
		Stream:          req.Stream,
		MaxTokens:       resolveMaxTokens(session),
		ReasoningEffort: runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort),
		Temperature:     0.7,
	}
	config.Metadata = map[string]interface{}{}
	for key, value := range req.Metadata {
		config.Metadata[key] = value
	}
	if reasoningEffort := runtimetypes.NormalizeReasoningEffort(session.ReasoningEffort); reasoningEffort != "" {
		config.Metadata["reasoning_effort"] = reasoningEffort
	}
	if session.RuntimeSession != nil && strings.TrimSpace(session.RuntimeSession.ID) != "" {
		config.Metadata["prompt_cache_key"] = strings.TrimSpace(session.RuntimeSession.ID)
		config.Metadata["session_id"] = strings.TrimSpace(session.RuntimeSession.ID)
	}
	if outputDir := currentGeneratedImageArtifactDir(session); strings.TrimSpace(outputDir) != "" {
		config.Metadata[runtimellm.MetadataKeyGeneratedImageOutputDir] = outputDir
	}
	if defs := req.Tools; len(defs) > 0 {
		if strings.EqualFold(strings.TrimSpace(session.Provider.GetProtocol()), "codex") {
			config.Functions = runtimellm.BuildToolDefinitionsForRequest(
				defs,
				session.Provider.GetProtocol(),
				session.Model,
				session.Provider.ModelCapabilities,
				false,
			)
		} else {
			catalog := ensureFunctionCatalog(session)
			builder := catalog.Builder(session.Provider.GetProtocol())
			config.Functions = builder.BuildFunctions(toolDefinitionsToSchemas(defs))
		}
	} else if strings.EqualFold(strings.TrimSpace(session.Provider.GetProtocol()), "codex") {
		config.Functions = runtimellm.BuildToolDefinitionsForRequest(
			nil,
			session.Provider.GetProtocol(),
			session.Model,
			session.Provider.ModelCapabilities,
			false,
		)
	}
	return config
}

func adapterAdapterConfig(session *ChatSession) adapter.AdapterConfig {
	return adapter.AdapterConfig{
		Type:    session.Provider.GetProtocol(),
		APIKey:  session.Provider.GetAPIKey(),
		Timeout: 120 * time.Second,
	}
}

func tokenUsageFromResponseBody(raw []byte) *runtimetypes.TokenUsage {
	usage := extractUsageFromResponseBody(raw)
	if len(usage) == 0 {
		return nil
	}
	result := &runtimetypes.TokenUsage{}
	if value, ok := usage["input_tokens"].(float64); ok {
		result.PromptTokens = int(value)
	} else if value, ok := usage["prompt_tokens"].(float64); ok {
		result.PromptTokens = int(value)
	}
	if value, ok := usage["output_tokens"].(float64); ok {
		result.CompletionTokens = int(value)
	} else if value, ok := usage["completion_tokens"].(float64); ok {
		result.CompletionTokens = int(value)
	}
	if value, ok := usage["total_tokens"].(float64); ok {
		result.TotalTokens = int(value)
	}
	if result.TotalTokens == 0 {
		result.TotalTokens = result.PromptTokens + result.CompletionTokens
	}
	return result
}

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

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	runtimechatcore "github.com/wwsheng009/ai-agent-runtime/internal/chatcore"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm/adapter"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

const chatcoreReasoningMetadataKey = "chatcore_reasoning_content"

var executeToolLoop = runtimechatcore.ExecuteToolLoop

type aicliChatExecutor interface {
	Execute(ctx context.Context, session *ChatSession, prompt string) (string, error)
}

type aicliSharedChatExecutor struct{}

func newAICLISharedChatExecutor() aicliChatExecutor {
	return &aicliSharedChatExecutor{}
}

func ensureChatExecutor(session *ChatSession) aicliChatExecutor {
	if session == nil {
		return newAICLISharedChatExecutor()
	}
	if session.ChatExecutor == nil {
		if session.ActorFirstReady {
			session.ChatExecutor = newAICLIActorChatExecutor()
		} else {
			session.ChatExecutor = newAICLISharedChatExecutor()
		}
	}
	return session.ChatExecutor
}

func (e *aicliSharedChatExecutor) Execute(ctx context.Context, session *ChatSession, prompt string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	history, err := buildRuntimeHistoryFromAICLIMessages(session.Messages)
	if err != nil {
		return "", fmt.Errorf("构建共享 chat history 失败: %w", err)
	}

	var selection *aicliFunctionSelection
	var exposureDetails *skillExposureDetails
	var exposureReport *aicliFunctionExposureReport
	if !session.DisableTools {
		if catalog := ensureFunctionCatalog(session); catalog != nil && catalog.Registry() != nil {
			selection, exposureDetails = catalog.SelectRequestFunctions(session, prompt)
			exposureReport = buildFunctionExposureReport(catalog, prompt, selection, exposureDetails)
			if session.SkillsDebug {
				fmt.Printf("\n%s\n", formatSkillExposureDebug(exposureReport))
			}
		}
	}

	renderer := newAICLIEventRenderer(session)
	var currentScope aicliLogScope
	scopePrompt := prompt
	provider := &aicliProviderTurnExecutor{
		session:        session,
		exposureReport: exposureReport,
		nextScope: func() aicliLogScope {
			currentScope = nextLogScope(session, scopePrompt)
			scopePrompt = ""
			return currentScope
		},
	}
	toolExec := &aicliToolExecutor{
		session: session,
		scopeProvider: func() aicliLogScope {
			return currentScope
		},
	}

	loopResult, err := executeToolLoop(ctx, runtimechatcore.ToolLoopRequest{
		Prompt:       prompt,
		History:      history,
		Stream:       session.Stream,
		Tools:        toolDefinitionsFromSelection(selection),
		Provider:     provider,
		ToolExecutor: toolExec,
		EventSink:    renderer.Handle,
	})
	if err != nil {
		return "", err
	}
	if loopResult == nil || loopResult.Response == nil {
		return "", fmt.Errorf("共享 chatcore 未返回结果")
	}

	messages, err := buildAICLIMessagesFromRuntimeHistory(loopResult.History)
	if err != nil {
		return "", fmt.Errorf("共享 chat history 转回 CLI 消息失败: %w", err)
	}
	session.Messages = messages
	warnIfChatSessionSyncFails(session, "shared chatcore sync", syncRuntimeSessionFromChat(session))

	if session.Logger != nil && len(loopResult.Response.ToolExecutions) > 0 {
		callSummaries := make([]aicliToolExecutionCallSummary, 0, len(loopResult.Response.ToolExecutions))
		successCount := 0
		errorCount := 0
		for _, exec := range loopResult.Response.ToolExecutions {
			summary := aicliToolExecutionCallSummary{
				ToolCallID: exec.ToolCallID,
				Function:   exec.ToolName,
				Success:    exec.Success,
			}
			if exec.Success {
				successCount++
				summary.ResultPreview = truncateOutputPreview(exec.Output, maxToolResultPreviewLines, maxToolResultPreviewBytes)
				summary.ResultBytes = len(exec.Output)
			} else {
				errorCount++
				summary.Error = exec.Error
			}
			callSummaries = append(callSummaries, summary)
		}
		summary := buildToolExecutionSummary(callSummaries, successCount, errorCount)
		session.Logger.LogToolExecutionSummary(currentScope, summary)
		writeSessionDebugInfo(session, formatToolExecutionSummaryDebug(summary), true)
	}

	var finalMessage *runtimetypes.Message
	if count := len(loopResult.History); count > 0 {
		finalMessage = &loopResult.History[count-1]
	}
	renderer.Finalize(loopResult.Response, finalMessage)

	return loopResult.Response.Output, nil
}

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

	protocolMessages := runtimellm.RuntimeMessagesToProtocolMessages(req.Messages, session.Provider.GetProtocol())
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
	resp, responseBody, httpReport, err := sendHTTPRequest(session.HTTPClient, httpReq, session.RetryConfig)
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

	logpkg.Info("AICLI chat response received",
		logpkg.String("provider", session.ProviderName),
		logpkg.String("protocol", session.Provider.GetProtocol()),
		logpkg.String("model", session.Model),
		logpkg.URL(session.BaseURL),
		logpkg.Status(resp.StatusCode),
		logpkg.Latency(time.Since(startTime).Milliseconds()),
		logpkg.Int("response_bytes", len(responseBody)),
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

	assistantMsg, err := session.Adapter.HandleResponse(req.Stream, io.NopCloser(bytes.NewReader(responseBody)), callbacks)
	if session.IsInterrupted() {
		return nil, fmt.Errorf("用户中断")
	}
	if err != nil {
		if session.Logger != nil && session.Logger.logDir != "" {
			session.Logger.LogResponse(logScope, nil, responseBody, req.Stream, err, time.Since(startTime).Milliseconds())
		}
		return nil, fmt.Errorf("响应处理失败: %w", err)
	}

	content, _ := assistantMsg["content"].(string)
	reasoning, _ := assistantMsg["reasoning_content"].(string)
	rawToolCalls, hasToolCalls := assistantMsg["tool_calls"].([]map[string]interface{})

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

	return &runtimechatcore.ProviderTurnResponse{
		Message: &runtimeMessage,
		Usage:   tokenUsageFromResponseBody(responseBody),
	}, nil
}

type aicliToolExecutor struct {
	session       *ChatSession
	scopeProvider func() aicliLogScope
}

func (e *aicliToolExecutor) ExecuteTool(ctx context.Context, call runtimetypes.ToolCall) runtimechatcore.ToolResult {
	result := runtimechatcore.ToolResult{}
	if e == nil || e.session == nil {
		result.Error = "chat session is not configured"
		result.Content = result.Error
		return result
	}
	session := e.session
	scope := aicliLogScope{}
	if e.scopeProvider != nil {
		scope = e.scopeProvider()
	}

	if session.Logger != nil {
		session.Logger.LogToolCall(scope, call.ID, call.Name, call.Args)
	}
	writeSessionDebugInfo(session, formatToolExecutionStartDebug(toolCallFromRuntime(call)), true)

	catalog := ensureFunctionCatalog(session)
	output, err := catalog.ExecuteFunction(ctx, call.Name, call.Args)
	if err != nil {
		result.Error = err.Error()
		result.Content = fmt.Sprintf("执行失败: %v", err)
		if session.Logger != nil {
			session.Logger.LogToolResult(scope, call.ID, call.Name, nil, err)
		}
		writeSessionDebugInfo(session, formatToolExecutionResultDebug(toolCallFromRuntime(call), "", err), true)
		return result
	}

	result.Content = output
	if session.Logger != nil {
		session.Logger.LogToolResult(scope, call.ID, call.Name, output, nil)
	}
	writeSessionDebugInfo(session, formatToolExecutionResultDebug(toolCallFromRuntime(call), output, nil), true)
	return result
}

type aicliEventRenderer struct {
	session        *ChatSession
	streamBuffer   strings.Builder
	streamLines    int
	reasoningOpen  bool
	spinnerCleared bool
}

func newAICLIEventRenderer(session *ChatSession) *aicliEventRenderer {
	return &aicliEventRenderer{session: session}
}

func (r *aicliEventRenderer) Handle(event runtimechatcore.ChatEvent) {
	if r == nil || r.session == nil {
		return
	}
	switch event.Type {
	case runtimechatcore.EventPlanning:
		if !r.session.Stream || !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if r.session.Interaction != nil {
			r.session.Interaction.RenderReasoningDelta(&runtimetypes.ReasoningBlock{
				Format:     "stream_delta",
				Summary:    event.Content,
				Streamable: true,
				Visibility: runtimetypes.ReasoningVisibilitySummary,
			})
			r.reasoningOpen = true
			return
		}
		if !r.reasoningOpen {
			fmt.Println("\n--- Thinking ---")
			r.reasoningOpen = true
		}
		fmt.Print(event.Content)
	case runtimechatcore.EventResult:
		if !r.session.Stream || !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Content == "" {
			return
		}
		r.clearSpinner()
		if r.session.Interaction != nil {
			if r.reasoningOpen {
				r.session.Interaction.FinalizeReasoningDelta()
				r.reasoningOpen = false
			}
			r.streamBuffer.WriteString(event.Content)
			r.streamLines += strings.Count(event.Content, "\n")
			r.session.Interaction.RenderAssistantDelta(event.Content)
			return
		}
		if r.reasoningOpen {
			fmt.Print("\n--- End Thinking ---\n\n")
			r.reasoningOpen = false
		}
		r.streamBuffer.WriteString(event.Content)
		r.streamLines += strings.Count(event.Content, "\n")
		fmt.Print(event.Content)
	case runtimechatcore.EventTool:
		if !shouldRenderInteractiveOutput(r.session) {
			return
		}
		if event.Stage == "batch_start" {
			r.flushAssistantTurnForToolBatch()
		}
		rendered := renderSharedChatToolEvent(event)
		if strings.TrimSpace(rendered) == "" {
			return
		}
		if r.session.Interaction != nil {
			r.session.Interaction.RenderAsyncLine(rendered)
			return
		}
		fmt.Println(rendered)
	}
}

func (r *aicliEventRenderer) Finalize(response *runtimechatcore.ChatResult, finalMessage *runtimetypes.Message) {
	if r == nil || r.session == nil {
		return
	}

	reasoningBlock := finalReasoningBlock(finalMessage)
	if reasoningBlock != nil && shouldRenderInteractiveOutput(r.session) && !r.session.Stream {
		r.clearSpinner()
		lines := chatReasoningLines(reasoningBlock)
		if len(lines) > 0 {
			if r.session.Interaction != nil {
				r.session.Interaction.RenderAsyncLine(strings.Join(lines, "\n"))
			} else {
				fmt.Println(strings.Join(lines, "\n"))
			}
		}
	}

	if r.session.Stream && shouldRenderInteractiveOutput(r.session) {
		if r.session.Interaction != nil {
			if r.reasoningOpen {
				r.session.Interaction.FinalizeReasoningDelta()
				r.reasoningOpen = false
			}
			if response != nil && (strings.TrimSpace(response.Output) != "" || r.streamBuffer.Len() > 0) {
				if !r.session.Interaction.CompleteAssistantResponse(response.Output) {
					r.session.Interaction.FinalizeAssistantDelta()
				}
			} else {
				r.session.Interaction.FinalizeAssistantDelta()
			}
			return
		}
		if r.reasoningOpen {
			fmt.Print("\n--- End Thinking ---\n\n")
			r.reasoningOpen = false
		}
		content := r.streamBuffer.String()
		if content != "" && r.session.Formatter != nil && r.session.Formatter.IsMarkdown(content) {
			fmt.Printf("\033[%dF", r.streamLines+1)
			fmt.Printf("\033[J")
			fmt.Printf("助手> %s\n\n", r.session.Formatter.Format(content))
		} else if r.spinnerCleared {
			fmt.Println()
		}
		return
	}

	r.clearSpinner()
}

func finalReasoningBlock(finalMessage *runtimetypes.Message) *runtimetypes.ReasoningBlock {
	if finalMessage == nil || finalMessage.Metadata == nil {
		return nil
	}
	if block := runtimetypes.GetReasoningBlock(finalMessage.Metadata); block != nil {
		return block
	}
	if reasoning := finalMessage.Metadata.GetString(chatcoreReasoningMetadataKey, ""); strings.TrimSpace(reasoning) != "" {
		return &runtimetypes.ReasoningBlock{
			Summary:    strings.TrimSpace(reasoning),
			Visibility: runtimetypes.ReasoningVisibilitySummary,
		}
	}
	return nil
}

func (r *aicliEventRenderer) clearSpinner() {
	if r.spinnerCleared {
		return
	}
	if r.session == nil || r.session.NoInteractive {
		r.spinnerCleared = true
		return
	}
	if r.session.Interaction != nil {
		r.session.Interaction.ClearThinking()
		r.spinnerCleared = true
		return
	}
	fmt.Print("\r   \r")
	r.spinnerCleared = true
}

func (r *aicliEventRenderer) flushAssistantTurnForToolBatch() {
	if r == nil || r.session == nil {
		return
	}
	r.clearSpinner()
	if r.session.Interaction != nil {
		if r.reasoningOpen {
			r.session.Interaction.FinalizeReasoningDelta()
			r.reasoningOpen = false
		}
		r.session.Interaction.FinalizeAssistantDelta()
		r.resetAssistantStreamState()
		return
	}
	if r.reasoningOpen {
		fmt.Print("\n--- End Thinking ---\n\n")
		r.reasoningOpen = false
	}
	r.resetAssistantStreamState()
}

func (r *aicliEventRenderer) resetAssistantStreamState() {
	if r == nil {
		return
	}
	r.streamBuffer.Reset()
	r.streamLines = 0
}

func adapterRequestConfig(session *ChatSession, messages []map[string]interface{}, req runtimechatcore.ProviderTurnRequest) adapter.RequestConfig {
	config := adapter.RequestConfig{
		Model:           session.Model,
		Messages:        messages,
		Stream:          req.Stream,
		MaxTokens:       2000,
		ReasoningEffort: session.ReasoningEffort,
		Temperature:     0.7,
	}
	if defs := req.Tools; len(defs) > 0 {
		catalog := ensureFunctionCatalog(session)
		builder := catalog.Builder(session.Provider.GetProtocol())
		config.Functions = builder.BuildFunctions(toolDefinitionsToSchemas(defs))
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

func toolDefinitionsFromSelection(selection *aicliFunctionSelection) []runtimetypes.ToolDefinition {
	if selection == nil || len(selection.Schemas) == 0 {
		return nil
	}
	definitions := make([]runtimetypes.ToolDefinition, 0, len(selection.Schemas))
	for _, schema := range selection.Schemas {
		name, _ := schema["name"].(string)
		if strings.TrimSpace(name) == "" {
			continue
		}
		description, _ := schema["description"].(string)
		parameters, _ := schema["parameters"].(map[string]interface{})
		definitions = append(definitions, runtimetypes.ToolDefinition{
			Name:        strings.TrimSpace(name),
			Description: strings.TrimSpace(description),
			Parameters:  cloneFunctionSchema(parameters),
		})
	}
	return definitions
}

func toolDefinitionsToSchemas(defs []runtimetypes.ToolDefinition) []map[string]interface{} {
	if len(defs) == 0 {
		return nil
	}
	schemas := make([]map[string]interface{}, 0, len(defs))
	for _, def := range defs {
		schemas = append(schemas, map[string]interface{}{
			"name":        def.Name,
			"description": def.Description,
			"parameters":  cloneFunctionSchema(def.Parameters),
		})
	}
	return schemas
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

func toolCallFromRuntime(call runtimetypes.ToolCall) functions.ToolCall {
	return functions.ToolCall{
		ID:       call.ID,
		Function: call.Name,
		Args:     call.Args,
	}
}

func shouldRenderInteractiveOutput(session *ChatSession) bool {
	return session != nil && !session.NoInteractive && !session.JSONOutput
}

func chatEventInt(event runtimechatcore.ChatEvent, key string) int {
	if event.Metadata == nil {
		return 0
	}
	switch value := event.Metadata[key].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

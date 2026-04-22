package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

// truncateOutput 截断输出内容，只保留前几行
func truncateOutput(text string, maxLines int) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	truncated := strings.Join(lines[:maxLines], "\n")
	return fmt.Sprintf("%s\n... (已省略剩余 %d 行)", truncated, len(lines)-maxLines)
}

func truncateUTF8Bytes(text string, maxBytes int) string {
	if text == "" || maxBytes <= 0 {
		return ""
	}
	if len(text) <= maxBytes {
		return text
	}
	for maxBytes > 0 && !utf8.ValidString(text[:maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}

func truncateOutputPreview(text string, maxLines, maxBytes int) string {
	preview := truncateOutput(text, maxLines)
	if preview == "" || maxBytes <= 0 || len(preview) <= maxBytes {
		return preview
	}

	const suffix = "\n... (已省略剩余内容)"
	if len(suffix) >= maxBytes {
		return truncateUTF8Bytes(preview, maxBytes)
	}

	prefix := truncateUTF8Bytes(preview, maxBytes-len(suffix))
	if prefix == "" {
		return truncateUTF8Bytes(preview, maxBytes)
	}
	return prefix + suffix
}

// 重试配置默认值
const (
	defaultMaxRetryTime       = 60 * time.Second // 最大重试总时长（60秒）
	defaultFastRetryCount     = 5                // 前 N 次使用快速重试
	defaultFastRetryInterval  = 2 * time.Second  // 快速重试间隔（2秒）
	defaultSlowRetryInterval  = 5 * time.Second  // 慢速重试间隔（5秒）
	maxToolResultPreviewLines = 6                // tool execution summary 只保留前几行结果
	maxToolResultPreviewBytes = 1024             // tool execution summary 预览结果的最大字节数
)

// RetryConfig 重试配置
type RetryConfig struct {
	MaxRetryTime      time.Duration
	FastRetryCount    int
	FastRetryInterval time.Duration
	SlowRetryInterval time.Duration
	DisableRetries    bool
	Streaming         bool // 为 true 时，成功(2xx)响应不读取 body，由调用方直接消费 resp.Body
}

type httpAttemptReport struct {
	Attempt         int    `json:"attempt"`
	StatusCode      int    `json:"status_code,omitempty"`
	Error           string `json:"error,omitempty"`
	ResponseBytes   int    `json:"response_bytes,omitempty"`
	ResponsePreview string `json:"response_preview,omitempty"`
	DurationMs      int64  `json:"duration_ms,omitempty"`
}

type httpRequestReport struct {
	Method              string              `json:"method"`
	URL                 string              `json:"url"`
	DisableRetries      bool                `json:"disable_retries"`
	Attempts            []httpAttemptReport `json:"attempts,omitempty"`
	FinalStatusCode     int                 `json:"final_status_code,omitempty"`
	FinalError          string              `json:"final_error,omitempty"`
	LastResponseBytes   int                 `json:"last_response_bytes,omitempty"`
	LastResponsePreview string              `json:"last_response_preview,omitempty"`
}

type aicliToolExecutionCallSummary struct {
	ToolCallID    string `json:"tool_call_id,omitempty"`
	Function      string `json:"function"`
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
	ResultPreview string `json:"result_preview,omitempty"`
	ResultBytes   int    `json:"result_bytes,omitempty"`
}

type aicliToolExecutionSummary struct {
	CallCount    int                             `json:"call_count"`
	SuccessCount int                             `json:"success_count"`
	ErrorCount   int                             `json:"error_count"`
	Functions    []string                        `json:"functions,omitempty"`
	Calls        []aicliToolExecutionCallSummary `json:"calls,omitempty"`
}

func defaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetryTime:      defaultMaxRetryTime,
		FastRetryCount:    defaultFastRetryCount,
		FastRetryInterval: defaultFastRetryInterval,
		SlowRetryInterval: defaultSlowRetryInterval,
	}
}

// isRetryableHTTPCode 判断 HTTP 状态码是否应该重试
func isRetryableHTTPCode(statusCode int) bool {
	// 不应该重试的 4xx 错误（客户端错误，参数/权限问题）
	switch statusCode {
	case 400, // Bad Request - 参数错误
		401, // Unauthorized - 认证失败
		403, // Forbidden - 权限不足
		404, // Not Found - 资源不存在
		405, // Method Not Allowed - 方法不支持
		411, // Length Required - 缺少 Content-Length
		413, // Payload Too Large - 请求体过大
		414, // URI Too Long - URL 过长
		415: // Unsupported Media Type - 不支持的媒体类型
		return false
	}

	// 应该重试的状态码
	switch {
	case statusCode >= 500: // 5xx 服务器错误 - 应该重试
		return true
	case statusCode == 408: // Request Timeout - 请求超时，应该重试
		return true
	case statusCode == 429: // Too Many Requests - 限流，应该重试
		return true
	default:
		// 其他 4xx 错误（如 422 等）默认不重试
		return false
	}
}

// isNetworkError 判断错误是否为网络错误（可重试）
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// 网络错误检查
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	// TCP 连接错误检查
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
	// 超时错误
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, context.Canceled) {
		return true
	}
	return false
}

// sendHTTPRequest 发送 HTTP 请求（支持重试）
func sendHTTPRequest(client *http.Client, req *http.Request, retryCfg RetryConfig) (*http.Response, []byte, *httpRequestReport, error) {
	var lastErr error
	var lastResp *http.Response
	var body []byte
	report := &httpRequestReport{
		Method:         req.Method,
		URL:            req.URL.String(),
		DisableRetries: retryCfg.DisableRetries,
	}

	// 应用默认值
	maxRetryTime := retryCfg.MaxRetryTime
	if maxRetryTime <= 0 {
		maxRetryTime = defaultMaxRetryTime
	}
	fastRetryCount := retryCfg.FastRetryCount
	if fastRetryCount <= 0 {
		fastRetryCount = defaultFastRetryCount
	}
	fastRetryInterval := retryCfg.FastRetryInterval
	if fastRetryInterval <= 0 {
		fastRetryInterval = defaultFastRetryInterval
	}
	slowRetryInterval := retryCfg.SlowRetryInterval
	if slowRetryInterval <= 0 {
		slowRetryInterval = defaultSlowRetryInterval
	}

	// 提前保存原始请求体，用于重试
	var originalBody []byte
	if req.Body != nil {
		originalBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	// 重置原始请求体
	req.Body = io.NopCloser(bytes.NewReader(originalBody))

	startTime := time.Now() // 记录开始时间

	for attempt := 0; ; attempt++ { // 移除固定最大次数，改用时间限制
		// 检查是否超过最大重试时间
		if elapsed := time.Since(startTime); elapsed > maxRetryTime {
			report.FinalError = fmt.Sprintf("重试超时（已耗时 %.0fs，超过最大 %.0fs）", elapsed.Seconds(), maxRetryTime.Seconds())
			if lastResp != nil {
				report.FinalStatusCode = lastResp.StatusCode
			}
			report.LastResponseBytes = len(body)
			report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
			return lastResp, body, report, errors.New(report.FinalError)
		}

		// 检查是否中断（包括重试前）
		if ctx := req.Context(); ctx.Err() != nil {
			report.FinalError = ctx.Err().Error()
			return nil, nil, report, ctx.Err()
		}

		if attempt > 0 {
			// 如果是 context canceled，不进行重试（用户已中断）
			if errors.Is(lastErr, context.Canceled) {
				report.FinalError = lastErr.Error()
				return nil, nil, report, lastErr
			}
			// 重试提示
			elapsed := time.Since(startTime)
			retryInterval := fastRetryInterval
			if attempt >= fastRetryCount {
				retryInterval = slowRetryInterval
			}

			// 格式化重试间隔描述
			intervalDesc := "2秒"
			if retryInterval >= slowRetryInterval {
				intervalDesc = "5秒"
			}

			if isNetworkError(lastErr) {
				fmt.Printf("\n[重试 %d] 网络连接失败，%s后重试... (已耗时 %.1fs) ", attempt, intervalDesc, elapsed.Seconds())
			} else if lastResp != nil {
				var errorType string
				if lastResp.StatusCode >= 400 && lastResp.StatusCode < 500 {
					errorType = "客户端错误"
				} else if lastResp.StatusCode >= 500 {
					errorType = "服务器错误"
				} else {
					errorType = fmt.Sprintf("HTTP %d", lastResp.StatusCode)
				}
				fmt.Printf("\n[重试 %d] %s (%d)，%s后重试... (已耗时 %.1fs) ", attempt, errorType, lastResp.StatusCode, intervalDesc, elapsed.Seconds())
			}

			time.Sleep(retryInterval)
		}

		// 每次请求前创建新的请求对象（避免 http.Client 修改原始请求的状态）
		// 克隆请求
		attemptStart := time.Now()
		newReq, err := http.NewRequest(req.Method, req.URL.String(), bytes.NewReader(originalBody))
		if err != nil {
			report.FinalError = fmt.Sprintf("创建重试请求失败: %v", err)
			return nil, nil, report, fmt.Errorf("创建重试请求失败: %w", err)
		}
		// 复制 Context
		newReq = newReq.WithContext(req.Context())
		// 复制 Header
		for k, v := range req.Header {
			for _, vv := range v {
				newReq.Header.Add(k, vv)
			}
		}

		// 发送请求
		resp, err := client.Do(newReq)
		if err != nil {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				Error:      err.Error(),
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
			// 网络错误，记录并重试
			if isNetworkError(err) {
				if retryCfg.DisableRetries {
					report.FinalError = err.Error()
					return nil, nil, report, err
				}
				lastErr = err
				continue
			}
			// 非网络错误，直接返回
			report.FinalError = err.Error()
			return nil, nil, report, err
		}

		// 流式请求：成功(2xx)响应不读取 body，由调用方直接消费 resp.Body
		if retryCfg.Streaming && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				StatusCode: resp.StatusCode,
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
			report.FinalStatusCode = resp.StatusCode
			return resp, nil, report, nil
		}

		// 读取响应体
		body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				StatusCode: resp.StatusCode,
				Error:      err.Error(),
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
			// 读取响应失败，可能是网络问题，重试
			if retryCfg.DisableRetries {
				report.FinalStatusCode = resp.StatusCode
				report.FinalError = err.Error()
				return resp, nil, report, err
			}
			lastErr = err
			continue
		}
		report.Attempts = append(report.Attempts, httpAttemptReport{
			Attempt:         attempt + 1,
			StatusCode:      resp.StatusCode,
			ResponseBytes:   len(body),
			ResponsePreview: truncateUTF8Bytes(string(body), 2048),
			DurationMs:      time.Since(attemptStart).Milliseconds(),
		})

		// 检查 HTTP 状态码
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			// 只有可重试的状态码才重试
			if isRetryableHTTPCode(resp.StatusCode) {
				if retryCfg.DisableRetries {
					report.FinalStatusCode = resp.StatusCode
					report.FinalError = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))
					report.LastResponseBytes = len(body)
					report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
					return resp, body, report, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
				}
				lastResp = resp
				lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
				continue
			}
			// 不可重试的客户端错误（如 400），直接返回
			report.FinalStatusCode = resp.StatusCode
			report.FinalError = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))
			report.LastResponseBytes = len(body)
			report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
			return resp, body, report, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}

		// 成功
		report.FinalStatusCode = resp.StatusCode
		report.LastResponseBytes = len(body)
		report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
		return resp, body, report, nil
	}

	// 所有重试都失败
	if lastResp != nil {
		report.FinalStatusCode = lastResp.StatusCode
		report.FinalError = lastErr.Error()
		report.LastResponseBytes = len(body)
		report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
		return lastResp, body, report, lastErr
	}
	if lastErr != nil {
		report.FinalError = lastErr.Error()
	}
	return nil, nil, report, lastErr
}

// sendMessage 发送消息
func sendMessage(session *ChatSession, userMessage string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("chat session is nil")
	}
	if session.IsInterrupted() {
		return "", fmt.Errorf("用户中断")
	}
	ensureChatSystemPromptMessage(session)
	executor := ensureChatExecutor(session)

	if !session.NoInteractive && shouldShowInitialThinkingIndicator(session, executor) {
		if session.Interaction != nil {
			session.Interaction.StartThinking()
		} else {
			fmt.Print("助手正在思考...")
		}
	}

	ctx := session.cancelCtx
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if session.RequestTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, session.RequestTimeout)
		defer cancel()
	}

	response, err := executor.Execute(ctx, session, userMessage)
	if session.Interaction != nil {
		session.Interaction.ClearThinking()
	} else if err != nil && !session.NoInteractive {
		fmt.Print("\r   \r")
	}
	return response, err
}

func shouldShowInitialThinkingIndicator(session *ChatSession, executor aicliChatExecutor) bool {
	if session == nil || session.NoInteractive {
		return false
	}
	if session.LocalRuntimeHost != nil || session.ActorFirstReady {
		return false
	}
	if _, ok := executor.(*aicliActorChatExecutor); ok {
		return false
	}
	return true
}

// handleToolCalls 处理 Function Call
func handleToolCalls(session *ChatSession, logScope aicliLogScope, toolCalls []functions.ToolCall, content, reasoning string) (string, error) {
	// 构建 tool_calls 的原始格式（用于适配器）
	// 注意：OpenAI API 的 arguments 必须是 JSON 字符串
	rawToolCalls := make([]map[string]interface{}, len(toolCalls))
	for i, tc := range toolCalls {
		// 将 Args 序列化为 JSON 字符串
		argsJSON, _ := json.Marshal(tc.Args)
		rawToolCalls[i] = map[string]interface{}{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]interface{}{
				"name":      tc.Function,
				"arguments": string(argsJSON),
			},
		}
	}

	// 构建并添加 assistant 消息（包含 reasoning_content 和 tool_calls）
	assistantMsg := session.Adapter.BuildAssistantMessage(content, rawToolCalls, reasoning)
	session.Messages = append(session.Messages, assistantMsg)
	warnIfChatSessionSyncFails(session, "append assistant tool call message", syncRuntimeSessionFromChat(session))

	// 打印工具调用开始标题
	if session == nil || (!session.NoInteractive && !session.JSONOutput) {
		ui.PrintToolCallsStart(len(toolCalls))
	}

	// 执行工具调用
	var toolResults []string
	var successCount, errorCount int
	callSummaries := make([]aicliToolExecutionCallSummary, 0, len(toolCalls))

	for _, tc := range toolCalls {
		// 检查中断状态
		if session.IsInterrupted() {
			return "", fmt.Errorf("用户中断")
		}
		writeSessionDebugInfo(session, formatToolExecutionStartDebug(tc), true)

		// 记录工具调用
		if session.Logger != nil {
			session.Logger.LogToolCall(logScope, tc.ID, tc.Function, tc.Args)
		}

		// 执行工具函数（使用 session.cancelCtx 以支持中断）
		catalog := ensureFunctionCatalog(session)
		result, err := catalog.ExecuteFunction(session.cancelCtx, tc.Function, tc.Args)

		// 打印工具调用结果（只打印一次）
		if err != nil {
			result = fmt.Sprintf("执行失败: %v", err)
			if session.Logger != nil {
				session.Logger.LogToolResult(logScope, tc.ID, tc.Function, nil, err)
			}
			errorCount++
			callSummaries = append(callSummaries, aicliToolExecutionCallSummary{
				ToolCallID: tc.ID,
				Function:   tc.Function,
				Success:    false,
				Error:      err.Error(),
			})
			if session == nil || (!session.NoInteractive && !session.JSONOutput) {
				ui.PrintToolCallResult(tc.Function, tc.Args, false, err.Error())
			}
			writeSessionDebugInfo(session, formatToolExecutionResultDebug(tc, "", err), true)
		} else {
			if session.Logger != nil {
				session.Logger.LogToolResult(logScope, tc.ID, tc.Function, result, nil)
			}
			successCount++
			callSummaries = append(callSummaries, aicliToolExecutionCallSummary{
				ToolCallID:    tc.ID,
				Function:      tc.Function,
				Success:       true,
				ResultPreview: truncateOutputPreview(result, maxToolResultPreviewLines, maxToolResultPreviewBytes),
				ResultBytes:   len(result),
			})
			if session == nil || (!session.NoInteractive && !session.JSONOutput) {
				ui.PrintToolCallResult(tc.Function, tc.Args, true, result)
			}
			writeSessionDebugInfo(session, formatToolExecutionResultDebug(tc, result, nil), true)
		}

		// 添加工具结果到历史（成功或失败都会发送给AI）
		session.Messages = append(session.Messages, map[string]interface{}{
			"tool_call_id": tc.ID,
			"role":         "tool",
			"content":      result,
		})
		warnIfChatSessionSyncFails(session, "append tool result", syncRuntimeSessionFromChat(session))

		toolResults = append(toolResults, fmt.Sprintf("%s: %v", tc.Function, result))
	}

	// 检查中断状态（工具执行完成后）
	if session.IsInterrupted() {
		// 工具执行已被中断，直接返回（不打印额外消息，避免重复）
		return "", fmt.Errorf("用户中断")
	}

	// 打印工具调用完成标题
	if session == nil || (!session.NoInteractive && !session.JSONOutput) {
		ui.PrintToolCallsEnd(successCount, errorCount)
	}
	if session.Logger != nil {
		summary := buildToolExecutionSummary(callSummaries, successCount, errorCount)
		session.Logger.LogToolExecutionSummary(logScope, summary)
		writeSessionDebugInfo(session, formatToolExecutionSummaryDebug(summary), true)
	}

	// 递归调用，将工具结果发送给 AI 继续处理
	return sendMessage(session, "")
}

func buildRequestLogContent(baseURL string, requestBody map[string]interface{}, exposureReport *aicliFunctionExposureReport) map[string]interface{} {
	content := map[string]interface{}{
		"url":  baseURL,
		"body": requestBody,
	}
	if exposureReport != nil {
		content["function_exposure"] = exposureReport
		content["exposed_function_count"] = len(exposureReport.FinalFunctionNames)
		if len(exposureReport.FinalFunctionNames) > 0 {
			content["exposed_functions"] = append([]string(nil), exposureReport.FinalFunctionNames...)
		}
	}
	return content
}

func formatHTTPDebugReport(req *http.Request, requestBody []byte, report *httpRequestReport) string {
	if report == nil {
		return "[http-debug] no report available"
	}

	var lines []string
	lines = append(lines, fmt.Sprintf("[http-debug] %s %s", report.Method, report.URL))
	lines = append(lines, fmt.Sprintf("[http-debug] disable_retries=%t attempts=%d final_status=%d", report.DisableRetries, len(report.Attempts), report.FinalStatusCode))
	if report.FinalError != "" {
		lines = append(lines, fmt.Sprintf("[http-debug] final_error=%s", report.FinalError))
	}
	if report.LastResponsePreview != "" {
		lines = append(lines, fmt.Sprintf("[http-debug] last_response_preview=%s", report.LastResponsePreview))
	}
	if len(requestBody) > 0 {
		lines = append(lines, fmt.Sprintf("[http-debug] request_body_bytes=%d", len(requestBody)))
		lines = append(lines, fmt.Sprintf("[http-debug] request_body=%s", truncateUTF8Bytes(string(requestBody), 4096)))
	}
	if req != nil {
		lines = append(lines, fmt.Sprintf("[http-debug] request_headers=%s", marshalIndentedJSON(sanitizeHeadersForDebug(req.Header))))
	}
	for _, attempt := range report.Attempts {
		lines = append(lines, fmt.Sprintf("[http-debug] attempt=%d status=%d duration_ms=%d response_bytes=%d error=%q preview=%q",
			attempt.Attempt, attempt.StatusCode, attempt.DurationMs, attempt.ResponseBytes, attempt.Error, attempt.ResponsePreview))
	}

	return strings.Join(lines, "\n")
}

func isSessionDebugEnabled(session *ChatSession) bool {
	return session != nil && (session.HTTPDebug || session.SkillsDebug)
}

func writeSessionDebugInfo(session *ChatSession, debugInfo string, printToConsole bool) {
	if !isSessionDebugEnabled(session) || strings.TrimSpace(debugInfo) == "" {
		return
	}
	if session != nil && session.Logger != nil && session.Logger.logDir != "" {
		if err := session.Logger.WriteDebugInfo(session.Logger.logDir, debugInfo); err != nil {
			fmt.Fprintf(os.Stderr, "[调试日志写入失败] %v\n", err)
		}
	}
	if printToConsole {
		fmt.Printf("\n%s\n", debugInfo)
	}
}

func formatToolCallsDebugReport(toolCalls []functions.ToolCall) string {
	if len(toolCalls) == 0 {
		return "[tool-debug] no tool calls parsed"
	}
	lines := []string{
		fmt.Sprintf("[tool-debug] parsed_tool_calls=%d", len(toolCalls)),
	}
	for _, tc := range toolCalls {
		lines = append(lines, fmt.Sprintf("[tool-debug] parsed id=%s function=%s args=%s",
			tc.ID, tc.Function, marshalCompactJSON(tc.Args)))
	}
	return strings.Join(lines, "\n")
}

func formatToolExecutionStartDebug(tc functions.ToolCall) string {
	return fmt.Sprintf("[tool-debug] execute id=%s function=%s args=%s",
		tc.ID, tc.Function, marshalCompactJSON(tc.Args))
}

func formatToolExecutionResultDebug(tc functions.ToolCall, result string, err error) string {
	if err != nil {
		return fmt.Sprintf("[tool-debug] result id=%s function=%s success=false error=%q",
			tc.ID, tc.Function, err.Error())
	}
	return fmt.Sprintf("[tool-debug] result id=%s function=%s success=true bytes=%d preview=%q",
		tc.ID, tc.Function, len(result), truncateOutputPreview(result, maxToolResultPreviewLines, maxToolResultPreviewBytes))
}

func formatToolExecutionSummaryDebug(summary *aicliToolExecutionSummary) string {
	if summary == nil {
		return "[tool-debug] no tool execution summary"
	}
	lines := []string{
		fmt.Sprintf("[tool-debug] summary calls=%d success=%d error=%d",
			summary.CallCount, summary.SuccessCount, summary.ErrorCount),
	}
	if len(summary.Functions) > 0 {
		lines = append(lines, fmt.Sprintf("[tool-debug] functions=%s", strings.Join(summary.Functions, ", ")))
	}
	return strings.Join(lines, "\n")
}

func marshalCompactJSON(value interface{}) string {
	if value == nil {
		return "null"
	}
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(data)
}

func sanitizeHeadersForDebug(header http.Header) map[string][]string {
	sanitized := make(map[string][]string, len(header))
	for key, values := range header {
		copied := append([]string(nil), values...)
		if strings.EqualFold(key, "Authorization") {
			for i, value := range copied {
				copied[i] = redactAuthorizationValue(value)
			}
		}
		sanitized[key] = copied
	}
	return sanitized
}

func extractUsageFromResponseBody(raw []byte) map[string]interface{} {
	if len(raw) == 0 || !json.Valid(raw) {
		return nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil
	}

	usage, _ := payload["usage"].(map[string]interface{})
	if len(usage) == 0 {
		return nil
	}
	return usage
}

func redactAuthorizationValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	parts := strings.SplitN(trimmed, " ", 2)
	if len(parts) != 2 {
		if len(trimmed) <= 8 {
			return "***"
		}
		return trimmed[:4] + "***" + trimmed[len(trimmed)-4:]
	}
	token := parts[1]
	if len(token) <= 8 {
		return parts[0] + " ***"
	}
	return parts[0] + " " + token[:4] + "***" + token[len(token)-4:]
}

func nextLogScope(session *ChatSession, userMessage string) aicliLogScope {
	if session == nil {
		return aicliLogScope{}
	}
	if strings.TrimSpace(userMessage) != "" {
		session.MsgCount++
		session.TurnRequestCount = 0
	}

	turnIndex := session.MsgCount
	if turnIndex <= 0 {
		turnIndex = 1
	}
	session.TurnRequestCount++

	turnID := fmt.Sprintf("turn-%04d", turnIndex)
	return aicliLogScope{
		TurnID:    turnID,
		RequestID: fmt.Sprintf("%s-req-%02d", turnID, session.TurnRequestCount),
	}
}

func buildToolExecutionSummary(calls []aicliToolExecutionCallSummary, successCount, errorCount int) *aicliToolExecutionSummary {
	summary := &aicliToolExecutionSummary{
		CallCount:    len(calls),
		SuccessCount: successCount,
		ErrorCount:   errorCount,
		Calls:        append([]aicliToolExecutionCallSummary(nil), calls...),
	}
	functions := make([]string, 0, len(calls))
	seen := make(map[string]struct{}, len(calls))
	for _, call := range calls {
		if call.Function == "" {
			continue
		}
		if _, exists := seen[call.Function]; exists {
			continue
		}
		seen[call.Function] = struct{}{}
		functions = append(functions, call.Function)
	}
	if len(functions) > 0 {
		summary.Functions = functions
	}
	return summary
}

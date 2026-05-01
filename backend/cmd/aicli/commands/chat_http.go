package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

const (
	defaultMaxRetryTime      = 60 * time.Second
	defaultFastRetryCount    = 5
	defaultFastRetryInterval = 2 * time.Second
	defaultSlowRetryInterval = 5 * time.Second
)

// RetryConfig 重试配置
type RetryConfig struct {
	MaxRetryTime      time.Duration
	FastRetryCount    int
	FastRetryInterval time.Duration
	SlowRetryInterval time.Duration
	DisableRetries    bool
	Streaming         bool
	RetryNotice       func(string)
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
	switch statusCode {
	case 400,
		401,
		403,
		404,
		405,
		411,
		413,
		414,
		415:
		return false
	}

	switch {
	case statusCode >= 500:
		return true
	case statusCode == 408:
		return true
	case statusCode == 429:
		return true
	default:
		return false
	}
}

// isNetworkError 判断错误是否为网络错误（可重试）
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.ECONNABORTED) {
		return true
	}
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

	var originalBody []byte
	if req.Body != nil {
		originalBody, _ = io.ReadAll(req.Body)
		req.Body.Close()
	}
	req.Body = io.NopCloser(bytes.NewReader(originalBody))

	startTime := time.Now()

	for attempt := 0; ; attempt++ {
		if elapsed := time.Since(startTime); elapsed > maxRetryTime {
			report.FinalError = fmt.Sprintf("重试超时（已耗时 %.0fs，超过最大 %.0fs）", elapsed.Seconds(), maxRetryTime.Seconds())
			if lastResp != nil {
				report.FinalStatusCode = lastResp.StatusCode
			}
			report.LastResponseBytes = len(body)
			report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
			return lastResp, body, report, errors.New(report.FinalError)
		}

		if ctx := req.Context(); ctx.Err() != nil {
			report.FinalError = ctx.Err().Error()
			return nil, nil, report, ctx.Err()
		}

		if attempt > 0 {
			if errors.Is(lastErr, context.Canceled) {
				report.FinalError = lastErr.Error()
				return nil, nil, report, lastErr
			}

			elapsed := time.Since(startTime)
			retryInterval := fastRetryInterval
			if attempt >= fastRetryCount {
				retryInterval = slowRetryInterval
			}

			intervalDesc := "2秒"
			if retryInterval >= slowRetryInterval {
				intervalDesc = "5秒"
			}

			if isNetworkError(lastErr) {
				emitRetryNotice(retryCfg, fmt.Sprintf("[重试 %d] 网络连接失败，%s后重试... (已耗时 %.1fs)", attempt, intervalDesc, elapsed.Seconds()))
			} else if lastResp != nil {
				var errorType string
				if lastResp.StatusCode >= 400 && lastResp.StatusCode < 500 {
					errorType = "客户端错误"
				} else if lastResp.StatusCode >= 500 {
					errorType = "服务器错误"
				} else {
					errorType = fmt.Sprintf("HTTP %d", lastResp.StatusCode)
				}
				emitRetryNotice(retryCfg, fmt.Sprintf("[重试 %d] %s (%d)，%s后重试... (已耗时 %.1fs)", attempt, errorType, lastResp.StatusCode, intervalDesc, elapsed.Seconds()))
			}

			time.Sleep(retryInterval)
		}

		attemptStart := time.Now()
		newReq, err := http.NewRequest(req.Method, req.URL.String(), bytes.NewReader(originalBody))
		if err != nil {
			report.FinalError = fmt.Sprintf("创建重试请求失败: %v", err)
			return nil, nil, report, fmt.Errorf("创建重试请求失败: %w", err)
		}
		newReq = newReq.WithContext(req.Context())
		for key, values := range req.Header {
			for _, value := range values {
				newReq.Header.Add(key, value)
			}
		}

		resp, err := client.Do(newReq)
		if err != nil {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				Error:      err.Error(),
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
			if isNetworkError(err) {
				if retryCfg.DisableRetries {
					report.FinalError = err.Error()
					return nil, nil, report, err
				}
				lastErr = err
				continue
			}
			report.FinalError = err.Error()
			return nil, nil, report, err
		}

		if retryCfg.Streaming && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				StatusCode: resp.StatusCode,
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
			report.FinalStatusCode = resp.StatusCode
			return resp, nil, report, nil
		}

		body, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			report.Attempts = append(report.Attempts, httpAttemptReport{
				Attempt:    attempt + 1,
				StatusCode: resp.StatusCode,
				Error:      err.Error(),
				DurationMs: time.Since(attemptStart).Milliseconds(),
			})
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

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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
			report.FinalStatusCode = resp.StatusCode
			report.FinalError = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, string(body))
			report.LastResponseBytes = len(body)
			report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
			return resp, body, report, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
		}

		report.FinalStatusCode = resp.StatusCode
		report.LastResponseBytes = len(body)
		report.LastResponsePreview = truncateUTF8Bytes(string(body), 2048)
		return resp, body, report, nil
	}
}

func emitRetryNotice(retryCfg RetryConfig, notice string) {
	notice = strings.TrimSpace(notice)
	if notice == "" {
		return
	}
	if retryCfg.RetryNotice != nil {
		retryCfg.RetryNotice(notice)
		return
	}
	fmt.Printf("\n%s ", notice)
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
	return runtimellm.TokenUsageToMap(runtimellm.ExtractTokenUsageFromResponseBody(raw))
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

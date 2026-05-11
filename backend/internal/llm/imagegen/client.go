package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimehttpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

// Generator is the minimal interface implemented by Client and useful for tests.
type Generator interface {
	Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error)
}

// Client wraps an OpenAI-compatible image generations endpoint.
type Client struct {
	httpClient *http.Client
	baseURL    string
	apiPath    string
	apiKey     string
	headers    map[string]string
}

// NewClient constructs a new image generations client using the shared provider
// HTTP transport stack.
func NewClient(provider agentconfig.Provider, timeout time.Duration, proxy *agentconfig.ProxyConfig) *Client {
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	headers := make(map[string]string, len(provider.Headers))
	for key, value := range provider.Headers {
		if key == "" {
			continue
		}
		headers[key] = value
	}

	// Determine the request path from provider config, falling back to the
	// standard OpenAI images generations path.
	apiPath := strings.TrimSpace(provider.ForwardURL)
	if apiPath == "" {
		apiPath = strings.TrimSpace(provider.APIPath)
	}
	if apiPath == "" {
		apiPath = "/v1/images/generations"
	}

	return &Client{
		httpClient: runtimehttpclient.NewProviderHTTPClient(timeout, proxy, false),
		baseURL:    strings.TrimRight(strings.TrimSpace(provider.BaseURL), "/"),
		apiPath:    apiPath,
		apiKey:     strings.TrimSpace(provider.GetAPIKey()),
		headers:    headers,
	}
}

// Generate invokes POST /v1/images/generations and returns the parsed response.
func (c *Client) Generate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	if c == nil {
		return nil, fmt.Errorf("image generations client is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if req == nil {
		return nil, fmt.Errorf("generate request is nil")
	}

	normalized := *req
	req = &normalized
	NormalizeGenerateRequest(req)
	if err := Validate(req); err != nil {
		return nil, err
	}
	if strings.TrimSpace(c.baseURL) == "" {
		return nil, fmt.Errorf("provider base url is empty")
	}
	if c.httpClient == nil {
		return nil, fmt.Errorf("http client is not configured")
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		response, err := c.doGenerate(ctx, req)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !isRetryableImageGenError(err) || attempt == 3 {
			break
		}
		delay := retryDelayFromImageGenError(err)
		if delay <= 0 {
			delay = retryBackoffDelay(attempt)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}

func (c *Client) doGenerate(ctx context.Context, req *GenerateRequest) (*GenerateResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal image generation request: %w", err)
	}

	// Use JoinBaseURLAndPath which automatically deduplicates overlapping path
	// segments (e.g. baseURL ending with /v1 + apiPath /v1/images/generations
	// produces /v1/images/generations, not /v1/v1/images/generations).
	requestURL := agentconfig.JoinBaseURLAndPath(c.baseURL, c.apiPath)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create image generation request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	for key, value := range c.headers {
		httpReq.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, wrapImageGenHTTPError(0, "", nil, err)
	}
	defer resp.Body.Close()

	respBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read image generation response: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, parseImageGenAPIError(resp.StatusCode, respBody, resp.Header)
	}

	var out GenerateResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode image generation response: %w", err)
	}
	if len(out.Data) == 0 {
		return nil, fmt.Errorf("image generation response contained no data")
	}
	for i, item := range out.Data {
		if !item.HasB64JSON() && !item.HasURL() {
			return nil, fmt.Errorf("image generation response item %d contained neither b64_json nor url", i)
		}
	}
	return &out, nil
}

// APIError captures non-2xx responses from the image generations endpoint.
type APIError struct {
	StatusCode int
	Type       string
	Code       string
	Message    string
	Body       string
	retryAfter time.Duration
}

func (e *APIError) Error() string {
	if e == nil {
		return ""
	}
	parts := make([]string, 0, 3)
	if e.Message != "" {
		parts = append(parts, e.Message)
	} else if e.Body != "" {
		parts = append(parts, e.Body)
	}
	if e.Type != "" {
		parts = append([]string{"type=" + e.Type}, parts...)
	}
	if e.Code != "" {
		parts = append([]string{"code=" + e.Code}, parts...)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("HTTP %d", e.StatusCode)
	}
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, strings.Join(parts, " "))
}

func (e *APIError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.StatusCode
}

func (e *APIError) RetryAfterDelay() time.Duration {
	if e == nil {
		return 0
	}
	return e.retryAfter
}

type retryAfterHinter interface {
	RetryAfterDelay() time.Duration
}

type httpStatusCoder interface {
	HTTPStatusCode() int
}

func wrapImageGenHTTPError(statusCode int, body string, header http.Header, err error) error {
	if err == nil {
		return nil
	}
	if statusCode == 0 {
		return err
	}
	apiErr := &APIError{
		StatusCode: statusCode,
		Body:       strings.TrimSpace(body),
	}
	if delay, ok := retryAfterDelayFromHeader(header, time.Time{}); ok {
		apiErr.retryAfter = delay
	} else if delay, ok := retryAfterDelayFromBody(body); ok {
		apiErr.retryAfter = delay
	}
	return fmt.Errorf("%w", apiErr)
}

func parseImageGenAPIError(statusCode int, body []byte, header http.Header) error {
	apiErr := &APIError{
		StatusCode: statusCode,
		Body:       strings.TrimSpace(string(body)),
	}
	if delay, ok := retryAfterDelayFromHeader(header, time.Time{}); ok {
		apiErr.retryAfter = delay
	} else if delay, ok := retryAfterDelayFromBody(string(body)); ok {
		apiErr.retryAfter = delay
	}

	var payload map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err == nil {
		if detail, ok := payload["error"].(map[string]interface{}); ok {
			apiErr.Message = firstNonEmptyString(detail["message"], payload["message"])
			apiErr.Type = firstNonEmptyString(detail["type"], payload["type"])
			apiErr.Code = firstNonEmptyString(detail["code"], payload["code"])
			if apiErr.Message == "" {
				apiErr.Message = firstNonEmptyString(detail["error"], payload["error"])
			}
		} else {
			apiErr.Message = firstNonEmptyString(payload["message"], payload["error"])
			apiErr.Type = firstNonEmptyString(payload["type"], payload["error_type"])
			apiErr.Code = firstNonEmptyString(payload["code"], payload["error_code"])
		}
	}
	return apiErr
}

func isRetryableImageGenError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *APIError
	if errors.As(err, &apiErr) {
		switch {
		case apiErr.StatusCode == http.StatusTooManyRequests:
			return true
		case apiErr.StatusCode >= 500:
			return true
		default:
			return false
		}
	}

	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() || netErr.Temporary() {
			return true
		}
	}

	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"timeout",
		"timed out",
		"connection reset",
		"connection refused",
		"broken pipe",
		"temporary failure",
		"eof",
		"unexpected end of file",
		"server closed connection",
		"transport is closing",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func retryDelayFromImageGenError(err error) time.Duration {
	if err == nil {
		return 0
	}
	var hinter retryAfterHinter
	if errors.As(err, &hinter) {
		if delay := hinter.RetryAfterDelay(); delay > 0 {
			return delay
		}
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		if apiErr.retryAfter > 0 {
			return apiErr.retryAfter
		}
	}
	return 0
}

func retryBackoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(1<<uint(attempt-1)) * 500 * time.Millisecond
}

func retryAfterDelayFromHeader(header http.Header, now time.Time) (time.Duration, bool) {
	if len(header) == 0 {
		return 0, false
	}
	if delay, ok := parseRetryAfterMillisecondsValue(header.Get("Retry-After-Ms")); ok {
		return delay, true
	}
	return parseRetryAfterHeaderValue(header.Get("Retry-After"), now)
}

func parseRetryAfterHeaderValue(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration, true
	}
	if delay, ok := parseRetryAfterDurationValue(value); ok {
		return delay, true
	}
	parsedTime, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	if now.IsZero() {
		now = time.Now()
	}
	delay := parsedTime.Sub(now)
	if delay <= 0 {
		return 0, false
	}
	return delay, true
}

func parseRetryAfterDurationValue(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return time.Duration(number * float64(time.Second)), true
}

func parseRetryAfterMillisecondsValue(value string) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if duration, err := time.ParseDuration(value); err == nil && duration > 0 {
		return duration, true
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || number <= 0 {
		return 0, false
	}
	return time.Duration(number * float64(time.Millisecond)), true
}

func retryAfterDelayFromBody(body string) (time.Duration, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return 0, false
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	decoder.UseNumber()
	var payload interface{}
	if err := decoder.Decode(&payload); err != nil {
		return 0, false
	}
	return findRetryAfterDelayInValue(payload)
}

func findRetryAfterDelayInValue(value interface{}) (time.Duration, bool) {
	switch typed := value.(type) {
	case map[string]interface{}:
		for _, key := range []string{"retry_after_ms", "retryAfterMs", "retry_after_milliseconds"} {
			if delay, ok := parseRetryDelayValue(typed[key], true); ok {
				return delay, true
			}
		}
		for _, key := range []string{"retry_after", "retryAfter"} {
			if delay, ok := parseRetryDelayValue(typed[key], false); ok {
				return delay, true
			}
		}
		for _, nested := range typed {
			if delay, ok := findRetryAfterDelayInValue(nested); ok {
				return delay, true
			}
		}
	case []interface{}:
		for _, nested := range typed {
			if delay, ok := findRetryAfterDelayInValue(nested); ok {
				return delay, true
			}
		}
	}
	return 0, false
}

func parseRetryDelayValue(value interface{}, milliseconds bool) (time.Duration, bool) {
	if value == nil {
		return 0, false
	}
	switch typed := value.(type) {
	case json.Number:
		number, err := typed.Float64()
		if err != nil || number <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(number, milliseconds), true
	case float64:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(typed, milliseconds), true
	case float32:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return retryDelayFromFloat(float64(typed), milliseconds), true
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return 0, false
		}
		if duration, err := time.ParseDuration(text); err == nil && duration > 0 {
			return duration, true
		}
		if milliseconds {
			return parseRetryAfterMillisecondsValue(text)
		}
		return parseRetryAfterHeaderValue(text, time.Time{})
	default:
		return 0, false
	}
}

func retryDelayFromFloat(value float64, milliseconds bool) time.Duration {
	if milliseconds {
		return time.Duration(value * float64(time.Millisecond))
	}
	return time.Duration(value * float64(time.Second))
}

func firstNonEmptyString(values ...interface{}) string {
	for _, value := range values {
		if value == nil {
			continue
		}
		switch typed := value.(type) {
		case string:
			if trimmed := strings.TrimSpace(typed); trimmed != "" {
				return trimmed
			}
		default:
			if trimmed := strings.TrimSpace(fmt.Sprint(typed)); trimmed != "" && trimmed != "<nil>" {
				return trimmed
			}
		}
	}
	return ""
}

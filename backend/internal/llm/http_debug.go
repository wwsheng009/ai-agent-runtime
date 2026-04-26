package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// HTTPDebugEvent captures a low-level HTTP request/response snapshot emitted by runtime LLM providers.
type HTTPDebugEvent struct {
	Source              string                 `json:"source,omitempty"`
	Phase               string                 `json:"phase,omitempty"`
	Provider            string                 `json:"provider,omitempty"`
	Protocol            string                 `json:"protocol,omitempty"`
	Model               string                 `json:"model,omitempty"`
	Attempt             int                    `json:"attempt,omitempty"`
	MaxAttempts         int                    `json:"max_attempts,omitempty"`
	Method              string                 `json:"method,omitempty"`
	URL                 string                 `json:"url,omitempty"`
	RequestMetadata     map[string]interface{} `json:"request_metadata,omitempty"`
	RequestBody         string                 `json:"request_body,omitempty"`
	RequestBodyBytes    int                    `json:"request_body_bytes,omitempty"`
	RequestBodyRaw      []byte                 `json:"-"`
	ResponseStatusCode  int                    `json:"response_status_code,omitempty"`
	ResponseBodyPreview string                 `json:"response_body_preview,omitempty"`
	ResponseBodyBytes   int                    `json:"response_body_bytes,omitempty"`
	ResponseBodyRaw     []byte                 `json:"-"`
	Error               string                 `json:"error,omitempty"`
	RetryReason         string                 `json:"retry_reason,omitempty"`
	RetryDelayMS        int64                  `json:"retry_delay_ms,omitempty"`
}

// HTTPDebugReporter consumes runtime HTTP debug events.
type HTTPDebugReporter func(HTTPDebugEvent)

type httpDebugReporterContextKey struct{}
type httpDebugRetryAttemptContextKey struct{}

type httpDebugRetryAttemptState struct {
	Attempt     int
	MaxAttempts int
}

const httpDebugRequestDiagnosticsKey = "_request_debug"

// WithHTTPDebugReporter attaches a runtime HTTP debug reporter to the context.
func WithHTTPDebugReporter(ctx context.Context, reporter HTTPDebugReporter) context.Context {
	if reporter == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, httpDebugReporterContextKey{}, reporter)
}

func reportHTTPDebug(ctx context.Context, event HTTPDebugEvent) {
	if ctx == nil {
		return
	}
	if state, ok := ctx.Value(httpDebugRetryAttemptContextKey{}).(httpDebugRetryAttemptState); ok {
		if event.Attempt <= 0 {
			event.Attempt = state.Attempt
		}
		if event.MaxAttempts <= 0 {
			event.MaxAttempts = state.MaxAttempts
		}
	}
	reporter, _ := ctx.Value(httpDebugReporterContextKey{}).(HTTPDebugReporter)
	if reporter == nil {
		return
	}
	reporter(event)
}

func withHTTPDebugRetryAttempt(ctx context.Context, attempt int, maxAttempts int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if attempt <= 0 && maxAttempts <= 0 {
		return ctx
	}
	return context.WithValue(ctx, httpDebugRetryAttemptContextKey{}, httpDebugRetryAttemptState{
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
	})
}

func truncateHTTPDebugText(text string, maxBytes int) string {
	if text == "" || maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	return text[:maxBytes]
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func cloneHTTPDebugMetadata(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = cloneHTTPDebugValue(value)
	}
	return cloned
}

func cloneHTTPDebugValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return cloneHTTPDebugMetadata(typed)
	case []interface{}:
		cloned := make([]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneHTTPDebugValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	case []map[string]interface{}:
		cloned := make([]map[string]interface{}, len(typed))
		for index, item := range typed {
			cloned[index] = cloneHTTPDebugMetadata(item)
		}
		return cloned
	case []byte:
		return append([]byte(nil), typed...)
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	default:
		return typed
	}
}

func buildHTTPDebugRequestMetadata(metadata map[string]interface{}, protocol string, requestBody map[string]interface{}) map[string]interface{} {
	cloned := cloneHTTPDebugMetadata(metadata)
	diagnostics := buildHTTPDebugRequestDiagnostics(protocol, requestBody)
	if layout := strings.TrimSpace(fmt.Sprint(metadataValueAny(cloned, "prompt_layout"))); layout != "" && layout != "<nil>" {
		if diagnostics == nil {
			diagnostics = make(map[string]interface{}, 2)
		}
		diagnostics["prompt_layout_sha256"] = canonicalHTTPDebugValueSHA256(layout)
		diagnostics["prompt_layout_length"] = len(layout)
	}
	if len(diagnostics) == 0 {
		return cloned
	}
	if cloned == nil {
		cloned = make(map[string]interface{}, 1)
	}
	cloned[httpDebugRequestDiagnosticsKey] = diagnostics
	return cloned
}

func buildHTTPDebugRequestDiagnostics(protocol string, requestBody map[string]interface{}) map[string]interface{} {
	if len(requestBody) == 0 {
		return nil
	}

	diagnostics := map[string]interface{}{
		"request_sha256": canonicalHTTPDebugValueSHA256(requestBody),
	}
	if protocol = strings.TrimSpace(protocol); protocol != "" {
		diagnostics["protocol"] = protocol
	}

	cacheSurface := make(map[string]interface{})
	if value, ok := requestBody["messages"]; ok {
		diagnostics["messages_sha256"] = canonicalHTTPDebugValueSHA256(value)
		diagnostics["message_count"] = httpDebugSliceLen(value)
		cacheSurface["messages"] = value
	}
	if value, ok := requestBody["input"]; ok {
		diagnostics["input_sha256"] = canonicalHTTPDebugValueSHA256(value)
		diagnostics["input_count"] = httpDebugSliceLen(value)
		cacheSurface["input"] = value
	}
	if value, ok := requestBody["tools"]; ok {
		diagnostics["tools_sha256"] = canonicalHTTPDebugValueSHA256(value)
		diagnostics["tool_count"] = httpDebugSliceLen(value)
		cacheSurface["tools"] = value
	}
	if value, ok := requestBody["instructions"]; ok {
		diagnostics["instructions_sha256"] = canonicalHTTPDebugValueSHA256(value)
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
			diagnostics["instructions_length"] = len(text)
		}
		cacheSurface["instructions"] = value
	}
	if value, ok := requestBody["prompt_cache_key"]; ok {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
			diagnostics["prompt_cache_key"] = text
			cacheSurface["prompt_cache_key"] = text
		}
	}
	if len(cacheSurface) > 0 {
		diagnostics["cache_surface_sha256"] = canonicalHTTPDebugValueSHA256(cacheSurface)
	}

	return diagnostics
}

func canonicalHTTPDebugValueSHA256(value interface{}) string {
	if value == nil {
		return ""
	}
	payload, err := json.Marshal(value)
	if err != nil || len(payload) == 0 {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func httpDebugSliceLen(value interface{}) int {
	switch typed := value.(type) {
	case []interface{}:
		return len(typed)
	case []map[string]interface{}:
		return len(typed)
	case []string:
		return len(typed)
	default:
		return 0
	}
}

// HTTPDebugRequestDiagnostics returns the request fingerprint diagnostics embedded in request metadata.
func HTTPDebugRequestDiagnostics(metadata map[string]interface{}) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	raw, ok := metadata[httpDebugRequestDiagnosticsKey].(map[string]interface{})
	if !ok || len(raw) == 0 {
		return nil
	}
	return cloneHTTPDebugMetadata(raw)
}

func metadataValueAny(metadata map[string]interface{}, key string) interface{} {
	if len(metadata) == 0 {
		return nil
	}
	return metadata[key]
}

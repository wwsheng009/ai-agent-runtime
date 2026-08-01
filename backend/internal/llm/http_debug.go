package llm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
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
	ErrorCode           string                 `json:"error_code,omitempty"`
	RetryDelayMS        int64                  `json:"retry_delay_ms,omitempty"`
	LogicalTurnID       string                 `json:"logical_turn_id,omitempty"`
	LLMRequestID        string                 `json:"llm_request_id,omitempty"`
	RetryAttemptID      string                 `json:"retry_attempt_id,omitempty"`
	ProviderRequestID   string                 `json:"provider_request_id,omitempty"`
	StreamID            string                 `json:"stream_id,omitempty"`
}

// HTTPDebugReporter consumes runtime HTTP debug events.
type HTTPDebugReporter func(HTTPDebugEvent)

type httpDebugReporterContextKey struct{}
type httpDebugRetryAttemptContextKey struct{}
type httpDebugRequestContextKey struct{}

type httpDebugRetryAttemptState struct {
	Attempt           int
	MaxAttempts       int
	LogicalTurnID     string
	LLMRequestID      string
	RetryAttemptID    string
	ProviderRequestID string
	StreamID          string
}

type httpDebugRequestState struct {
	LogicalTurnID string
	LLMRequestID  string
	StreamID      string
}

const httpDebugRequestDiagnosticsKey = "_request_debug"

const maxHTTPDebugRawBodyBytes = 256 * 1024

var httpDebugOmissionMarker = []byte("\n...[http debug body omitted middle bytes]...\n")

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
		copyHTTPDebugCorrelation(&event, state)
	}
	if state, ok := ctx.Value(httpDebugRequestContextKey{}).(httpDebugRequestState); ok {
		if event.LogicalTurnID == "" {
			event.LogicalTurnID = state.LogicalTurnID
		}
		if event.LLMRequestID == "" {
			event.LLMRequestID = state.LLMRequestID
		}
		if event.StreamID == "" {
			event.StreamID = state.StreamID
		}
	}
	reporter, _ := ctx.Value(httpDebugReporterContextKey{}).(HTTPDebugReporter)
	if reporter == nil {
		return
	}
	reporter(event)
}

func copyHTTPDebugCorrelation(event *HTTPDebugEvent, state httpDebugRetryAttemptState) {
	if event == nil {
		return
	}
	if event.LogicalTurnID == "" {
		event.LogicalTurnID = state.LogicalTurnID
	}
	if event.LLMRequestID == "" {
		event.LLMRequestID = state.LLMRequestID
	}
	if event.RetryAttemptID == "" {
		event.RetryAttemptID = state.RetryAttemptID
	}
	if event.ProviderRequestID == "" {
		event.ProviderRequestID = state.ProviderRequestID
	}
	if event.StreamID == "" {
		event.StreamID = state.StreamID
	}
}

func withHTTPDebugRequestMetadata(ctx context.Context, metadata map[string]interface{}) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	state := httpDebugRequestState{
		LogicalTurnID: metadataText(metadata, "logical_turn_id"),
		LLMRequestID:  metadataText(metadata, "llm_request_id"),
		StreamID:      metadataText(metadata, "stream_id"),
	}
	return context.WithValue(ctx, httpDebugRequestContextKey{}, state)
}

func metadataText(metadata map[string]interface{}, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, _ := metadata[key].(string)
	return strings.TrimSpace(value)
}

// boundHTTPDebugRawBody prevents diagnostics from duplicating an arbitrarily
// large request or response. It retains both ends because provider errors and
// streaming terminators commonly appear at the tail.
func boundHTTPDebugRawBody(body []byte) []byte {
	if len(body) <= maxHTTPDebugRawBodyBytes {
		return append([]byte(nil), body...)
	}
	available := maxHTTPDebugRawBodyBytes - len(httpDebugOmissionMarker)
	if available <= 0 {
		return append([]byte(nil), body[:maxHTTPDebugRawBodyBytes]...)
	}
	head := available / 2
	tail := available - head
	bounded := make([]byte, 0, maxHTTPDebugRawBodyBytes)
	bounded = append(bounded, body[:head]...)
	bounded = append(bounded, httpDebugOmissionMarker...)
	bounded = append(bounded, body[len(body)-tail:]...)
	return bounded
}

func withHTTPDebugRetryAttempt(ctx context.Context, attempt int, maxAttempts int) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if attempt <= 0 && maxAttempts <= 0 {
		return ctx
	}
	requestState, _ := ctx.Value(httpDebugRequestContextKey{}).(httpDebugRequestState)
	return context.WithValue(ctx, httpDebugRetryAttemptContextKey{}, httpDebugRetryAttemptState{
		Attempt:           attempt,
		MaxAttempts:       maxAttempts,
		LogicalTurnID:     requestState.LogicalTurnID,
		LLMRequestID:      requestState.LLMRequestID,
		RetryAttemptID:    "attempt_" + uuid.NewString(),
		ProviderRequestID: "provider_req_" + uuid.NewString(),
		StreamID:          requestState.StreamID,
	})
}

func truncateHTTPDebugText(text string, maxBytes int) string {
	if text == "" || maxBytes <= 0 || len(text) <= maxBytes {
		return text
	}
	end := maxBytes
	for end > 0 && !utf8.ValidString(text[:end]) {
		end--
	}
	return text[:end]
}

func truncateHTTPDebugBytes(data []byte, maxBytes int) string {
	if len(data) == 0 || maxBytes <= 0 {
		return ""
	}
	if len(data) <= maxBytes {
		return string(data)
	}
	end := maxBytes
	for end > 0 && !utf8.Valid(data[:end]) {
		end--
	}
	return string(data[:end])
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

func buildHTTPDebugRequestMetadata(metadata map[string]interface{}, protocol, profile string, requestBody map[string]interface{}) map[string]interface{} {
	cloned := cloneHTTPDebugMetadata(metadata)
	diagnostics := buildHTTPDebugRequestDiagnostics(protocol, profile, requestBody)
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

func buildHTTPDebugRequestDiagnostics(protocol, profile string, requestBody map[string]interface{}) map[string]interface{} {
	if len(requestBody) == 0 {
		return nil
	}

	diagnostics := map[string]interface{}{
		"request_sha256": canonicalHTTPDebugValueSHA256(requestBody),
	}
	if protocol = strings.TrimSpace(protocol); protocol != "" {
		diagnostics["protocol"] = protocol
	}
	if profile = strings.TrimSpace(profile); profile != "" {
		diagnostics["compatibility_profile"] = profile
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
	hasher := sha256.New()
	if err := json.NewEncoder(hasher).Encode(value); err != nil {
		return ""
	}
	return hex.EncodeToString(hasher.Sum(nil))
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

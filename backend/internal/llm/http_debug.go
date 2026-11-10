package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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

// httpDebugActive reports whether ctx carries an HTTP debug reporter.
//
// 生产 pprof 依据：buildHTTPDebugRequestMetadata 会对整个请求体做至多 6 次
// JSON 编码（canonicalHTTPDebugValueSHA256：request / messages / input /
// tools / instructions / cache_surface），而它是在 reportHTTPDebug 的**实参
// 位置**求值的——reportHTTPDebug 内部发现没有 reporter 就立即 return，此时
// 编码开销已经白付。长会话实测该路径独占 runLoop 累计分配的 75%（8.46GB）。
// 调用方应在构造这类昂贵字段前先查这个开关。
func httpDebugActive(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	reporter, _ := ctx.Value(httpDebugReporterContextKey{}).(HTTPDebugReporter)
	return reporter != nil
}

// buildHTTPDebugRequestMetadataActive 仅在有 reporter 时计算请求指纹；
// 无监听者时返回 nil，避免为一次注定被丢弃的事件编码整个请求体。
func buildHTTPDebugRequestMetadataActive(ctx context.Context, metadata map[string]interface{}, protocol, profile string, requestBody map[string]interface{}) map[string]interface{} {
	if !httpDebugActive(ctx) {
		return nil
	}
	return buildHTTPDebugRequestMetadata(metadata, protocol, profile, requestBody)
}

// buildHTTPDebugRequestMetadataActiveWithBody 与上面的 Active 版本相同，
// 但复用调用方已经序列化好的请求体 bytes。诊断指纹直接哈希这些原始字段
// 字节，不再为 messages/tools/instructions 各做一次完整 JSON 编码。
//
// 为什么重要：生产 pprof 中 buildHTTPDebugRequestMetadata 独占约 20 GB
// 累计分配；字段哈希的输入本来就是即将发送的请求体，重新编码一遍只增加
// 峰值内存与 GC 压力。bodyBytes 为空或不是合法顶层对象时回退到旧路径。
func buildHTTPDebugRequestMetadataActiveWithBody(ctx context.Context, metadata map[string]interface{}, protocol, profile string, requestBody map[string]interface{}, bodyBytes []byte) map[string]interface{} {
	if !httpDebugActive(ctx) {
		return nil
	}
	return buildHTTPDebugRequestMetadataWithBody(metadata, protocol, profile, requestBody, bodyBytes)
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
	return buildHTTPDebugRequestMetadataWithDigest(metadata, protocol, profile, requestBody, nil)
}

func buildHTTPDebugRequestMetadataWithBody(metadata map[string]interface{}, protocol, profile string, requestBody map[string]interface{}, bodyBytes []byte) map[string]interface{} {
	rawFields, ok := parseHTTPDebugRawFields(bodyBytes)
	if !ok {
		return buildHTTPDebugRequestMetadata(metadata, protocol, profile, requestBody)
	}
	return buildHTTPDebugRequestMetadataWithDigest(metadata, protocol, profile, requestBody, func(key string, value interface{}) string {
		if value == nil {
			return ""
		}
		if raw, ok := rawFields[key]; ok {
			return sha256HexJSONValue(raw)
		}
		return canonicalHTTPDebugValueSHA256(value)
	})
}

func buildHTTPDebugRequestMetadataWithDigest(metadata map[string]interface{}, protocol, profile string, requestBody map[string]interface{}, digest func(key string, value interface{}) string) map[string]interface{} {
	cloned := cloneHTTPDebugMetadata(metadata)
	diagnostics := buildHTTPDebugRequestDiagnosticsWithDigest(protocol, profile, requestBody, digest)
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
	return buildHTTPDebugRequestDiagnosticsWithDigest(protocol, profile, requestBody, nil)
}

func buildHTTPDebugRequestDiagnosticsWithBody(protocol, profile string, requestBody map[string]interface{}, bodyBytes []byte) map[string]interface{} {
	rawFields, ok := parseHTTPDebugRawFields(bodyBytes)
	if !ok {
		return buildHTTPDebugRequestDiagnostics(protocol, profile, requestBody)
	}
	return buildHTTPDebugRequestDiagnosticsWithDigest(protocol, profile, requestBody, func(key string, value interface{}) string {
		if value == nil {
			return ""
		}
		if raw, ok := rawFields[key]; ok {
			return sha256HexJSONValue(raw)
		}
		return canonicalHTTPDebugValueSHA256(value)
	})
}

func buildHTTPDebugRequestDiagnosticsWithDigest(protocol, profile string, requestBody map[string]interface{}, digest func(key string, value interface{}) string) map[string]interface{} {
	if len(requestBody) == 0 {
		return nil
	}
	valueDigest := func(key string, value interface{}) string {
		if digest != nil {
			return digest(key, value)
		}
		return canonicalHTTPDebugValueSHA256(value)
	}

	diagnostics := make(map[string]interface{}, 16)
	if protocol = strings.TrimSpace(protocol); protocol != "" {
		diagnostics["protocol"] = protocol
	}
	if profile = strings.TrimSpace(profile); profile != "" {
		diagnostics["compatibility_profile"] = profile
	}

	// Each content field is already hashed above. Compose the cache-surface
	// fingerprint from those digests in a stable order instead of re-encoding
	// a map that contains the full messages/tools/instructions payload again.
	// The digest is opaque to consumers; it must stay stable and must change
	// whenever any included field (or its order-sensitive digest) changes.
	surface := make([]httpDebugSurfaceEntry, 0, 5)
	if value, ok := requestBody["messages"]; ok {
		digest := valueDigest("messages", value)
		diagnostics["messages_sha256"] = digest
		diagnostics["message_count"] = httpDebugSliceLen(value)
		surface = append(surface, httpDebugSurfaceEntry{name: "messages", digest: digest})
	}
	if value, ok := requestBody["input"]; ok {
		digest := valueDigest("input", value)
		diagnostics["input_sha256"] = digest
		diagnostics["input_count"] = httpDebugSliceLen(value)
		surface = append(surface, httpDebugSurfaceEntry{name: "input", digest: digest})
	}
	if value, ok := requestBody["tools"]; ok {
		digest := valueDigest("tools", value)
		diagnostics["tools_sha256"] = digest
		diagnostics["tool_count"] = httpDebugSliceLen(value)
		surface = append(surface, httpDebugSurfaceEntry{name: "tools", digest: digest})
	}
	if value, ok := requestBody["instructions"]; ok {
		digest := valueDigest("instructions", value)
		diagnostics["instructions_sha256"] = digest
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
			diagnostics["instructions_length"] = len(text)
		}
		surface = append(surface, httpDebugSurfaceEntry{name: "instructions", digest: digest})
	}
	if value, ok := requestBody["prompt_cache_key"]; ok {
		if text := strings.TrimSpace(fmt.Sprint(value)); text != "" && text != "<nil>" {
			diagnostics["prompt_cache_key"] = text
			surface = append(surface, httpDebugSurfaceEntry{name: "prompt_cache_key", digest: text})
		}
	}
	if len(surface) > 0 {
		hasher := sha256.New()
		for _, entry := range surface {
			_, _ = hasher.Write([]byte(entry.name))
			_, _ = hasher.Write([]byte{0})
			_, _ = hasher.Write([]byte(entry.digest))
			_, _ = hasher.Write([]byte{0})
		}
		diagnostics["cache_surface_sha256"] = hex.EncodeToString(hasher.Sum(nil))
	}

	// Compose the request fingerprint from sorted top-level fields. Large
	// content fields contribute their already-computed digest; only the
	// remaining scalar fields need a small JSON encoding. This avoids a second
	// full encode of messages/tools/instructions while still changing for any
	// top-level field change.
	keys := make([]string, 0, len(requestBody))
	for key := range requestBody {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	requestHasher := sha256.New()
	for _, key := range keys {
		_, _ = requestHasher.Write([]byte(key))
		_, _ = requestHasher.Write([]byte{0})
		if digest, ok := diagnostics[key+"_sha256"].(string); ok && digest != "" {
			_, _ = requestHasher.Write([]byte(digest))
		} else if key == "prompt_cache_key" {
			if text, ok := diagnostics["prompt_cache_key"].(string); ok {
				_, _ = requestHasher.Write([]byte(text))
			} else {
				_, _ = requestHasher.Write([]byte("<nil>"))
			}
		} else if encoded, err := json.Marshal(requestBody[key]); err == nil {
			_, _ = requestHasher.Write(encoded)
		} else {
			_, _ = requestHasher.Write([]byte("<marshal_error>"))
		}
		_, _ = requestHasher.Write([]byte{0})
	}
	diagnostics["request_sha256"] = hex.EncodeToString(requestHasher.Sum(nil))

	return diagnostics
}

type httpDebugSurfaceEntry struct {
	name   string
	digest string
}

// httpDebugRawFieldNames 是请求体里需要独立指纹的顶层字段。与
// buildHTTPDebugRequestDiagnostics 的读取集合保持一致。
var httpDebugRawFieldNames = []string{
	"messages",
	"input",
	"tools",
	"instructions",
	"prompt_cache_key",
}

// parseHTTPDebugRawFields 从已经序列化的请求体中切出顶层字段的原始
// JSON 字节，不复制、不反序列化大字段。调用方随后直接对这些切片做
// SHA-256，避免为 messages/tools/instructions 再各编码一遍。
//
// 输入来自本包的 MarshalProviderRequestBody，形态是 single-line compact
// JSON；这里仍按完整 JSON 语法扫描字符串转义与嵌套对象/数组，遇到非法
// 输入返回 false，由调用方回退到旧的 map 编码路径。
func parseHTTPDebugRawFields(body []byte) (map[string][]byte, bool) {
	if len(body) == 0 {
		return nil, false
	}
	fields := make(map[string][]byte, len(httpDebugRawFieldNames))
	i := skipJSONSpace(body, 0)
	if i >= len(body) || body[i] != '{' {
		return nil, false
	}
	i = skipJSONSpace(body, i+1)

	for i < len(body) {
		switch body[i] {
		case '}':
			return fields, true
		case ',':
			i = skipJSONSpace(body, i+1)
			continue
		case '"':
			keyStart := i
			keyEnd, ok := skipJSONString(body, i)
			if !ok {
				return nil, false
			}
			i = skipJSONSpace(body, keyEnd)
			if i >= len(body) || body[i] != ':' {
				return nil, false
			}
			i = skipJSONSpace(body, i+1)
			valueStart := i
			valueEnd, ok := skipJSONValue(body, i)
			if !ok {
				return nil, false
			}
			i = skipJSONSpace(body, valueEnd)
			for _, name := range httpDebugRawFieldNames {
				if jsonKeyEquals(body[keyStart:keyEnd], name) {
					fields[name] = body[valueStart:valueEnd]
					break
				}
			}
		default:
			return nil, false
		}
	}
	return nil, false
}

func skipJSONSpace(data []byte, i int) int {
	for i < len(data) {
		switch data[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}

func skipJSONString(data []byte, i int) (int, bool) {
	if i >= len(data) || data[i] != '"' {
		return i, false
	}
	for i++; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return i, false
}

func skipJSONValue(data []byte, i int) (int, bool) {
	if i >= len(data) {
		return i, false
	}
	switch data[i] {
	case '"':
		return skipJSONString(data, i)
	case '{', '[':
		depth := 0
		for i < len(data) {
			switch data[i] {
			case '"':
				next, ok := skipJSONString(data, i)
				if !ok {
					return i, false
				}
				i = next
			case '{', '[':
				depth++
				i++
			case '}', ']':
				depth--
				i++
				if depth == 0 {
					return i, true
				}
			default:
				i++
			}
		}
		return i, false
	default:
		for i < len(data) {
			switch data[i] {
			case ',', '}', ']', ' ', '\t', '\n', '\r':
				return i, true
			default:
				i++
			}
		}
		return i, true
	}
}

func jsonKeyEquals(raw []byte, want string) bool {
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return false
	}
	inner := raw[1 : len(raw)-1]
	if bytes.IndexByte(inner, '\\') < 0 {
		if len(inner) != len(want) {
			return false
		}
		for i := range inner {
			if inner[i] != want[i] {
				return false
			}
		}
		return true
	}
	var decoded string
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return false
	}
	return decoded == want
}

// sha256HexJSONValue 对序列化后的 JSON 值做 SHA-256。canonicalHTTPDebugValueSHA256
// 使用 json.Encoder，其输出在值末尾带一个换行；这里直接在原始字段字节后
// 追加同一个换行，保持两条路径的指纹逐字节一致。
func sha256HexJSONValue(data []byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write(data)
	_, _ = hasher.Write([]byte{'\n'})
	return hex.EncodeToString(hasher.Sum(nil))
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

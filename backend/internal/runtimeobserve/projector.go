package runtimeobserve

import (
	"sort"
	"strings"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// Projector 负责把底层 runtime/LLM 事件投影为低敏允许的观测事件。
// 采用纯 allowlist：未知事件类型直接丢弃，任意 payload 字段不会自动透传。
type Projector struct {
	redactor                *Redactor
	exposeProviderRequestID bool
	maxEventBytes           int
}

// NewProjector 创建 projector。
func NewProjector(redactor *Redactor, exposeProviderRequestID bool, maxEventBytes int) *Projector {
	if maxEventBytes <= 0 {
		maxEventBytes = int(DefaultConfig().MaxEventBytes)
	}
	return &Projector{
		redactor:                redactor,
		exposeProviderRequestID: exposeProviderRequestID,
		maxEventBytes:           maxEventBytes,
	}
}

// eventAllowlist 是 v1 允许发布的业务事件类型集合（方案 §6.3）。
var eventAllowlist = map[string]bool{
	EventRuntimeStarted:   true,
	EventRuntimeReady:     true,
	EventRuntimeShutdown:  true,
	EventSessionStarted:   true,
	EventSessionState:     true,
	EventSessionFinished:  true,
	EventAgentTurnStarted: true,
	EventAgentTurnDone:    true,
	EventLLMRequestStart:  true,
	EventLLMRequestDone:   true,
	EventLLMAttemptStart:  true,
	EventLLMAttemptDone:   true,
	EventLLMRetry:         true,
	EventLLMStreamSummary: true,
	EventUsageUpdated:     true,
	EventToolStarted:      true,
	EventToolFinished:     true,
	EventToolFailed:       true,
	EventToolProgress:     true,
	EventRendererChanged:  true,
	EventObservationGap:   true,
	EventResyncRequired:   true,
}

// IsAllowedType 返回事件类型是否在 v1 白名单内。
func IsAllowedType(eventType string) bool {
	return eventAllowlist[eventType]
}

// ContentFingerprintKeys 是需要计算内容存在性指纹的低敏字段（域名按字段区分）。
var contentFingerprintKeys = map[string]string{
	"content":  FingerprintDomainContent,
	"text":     FingerprintDomainContent,
	"reasoning": FingerprintDomainContent,
	"prompt":   FingerprintDomainPrompt,
}

// payloadAllowKeys 是允许从任意 runtime payload 透传的扁平字段名。
// 仅数值/布尔/短 enum 性质的字段；任何字符串内容类字段都不会出现在这里。
var payloadAllowKeys = map[string]bool{
	"provider":          true,
	"model":             true,
	"protocol":          true,
	"status":            true,
	"state":             true,
	"phase":             true,
	"kind":              true,
	"attempt":           true,
	"max_attempts":      true,
	"retryable":         true,
	"error_code":        true,
	"error_category":    true,
	"duration_ms":       true,
	"ttfb_ms":           true,
	"prompt_tokens":     true,
	"completion_tokens": true,
	"total_tokens":      true,
	"cache_read_tokens": true,
	"reasoning_tokens":  true,
	"usage_source":      true,
	"aggregation_level": true,
	"tool_name":         true,
	"tool_count":        true,
	"tool_call_count":   true,
	"stream_count":      true,
	"chunk_count":       true,
	"delta_bytes":       true,
	"stream_id":         true,
	"finish_reason":     true,
	"prompt_chars":      true,
	"prompt_tokens_estimated": true,
	"context_window":    true,
	"max_output_tokens": true,
	"message_count":     true,
	"reasoning_enabled": true,
	"reasoning_visibility": true,
	"scene_revision":    true,
	"renderer_id":       true,
	"publisher_epoch":   true,
	"layout_generation": true,
	"agent_count":       true,
	"turn_id":           true,
	"attempt_id":        true,
}

// ProjectRuntimeEvent 把 bus 事件投影为观测事件。
// 返回值 (Event{}, false) 表示事件不在白名单（调用方应计入 unknown drop）。
func (p *Projector) ProjectRuntimeEvent(event runtimeevents.Event) (Event, bool) {
	if p == nil {
		return Event{}, false
	}
	eventType := strings.TrimSpace(event.Type)
	if !IsAllowedType(eventType) {
		return Event{}, false
	}
	corr := Correlation{
		SessionID: event.SessionID,
		TraceID:   event.TraceID,
		AgentID:   event.AgentName,
		ToolCallID: event.ToolName,
	}
	payload := p.projectPayload(eventType, event.Payload)
	proj := Event{
		Timestamp:     event.Timestamp,
		Type:          eventType,
		Source:        "agent_loop",
		SchemaVersion: SchemaVersionEvent,
		Correlation:   corr,
		Payload:       payload,
	}
	proj = p.enforceEventSize(proj)
	return proj, true
}

// eventContentCapableTypes 是允许对正文类字段计算存在性指纹的事件类型。
var eventContentCapableTypes = map[string]bool{
	EventLLMRequestStart:  true,
	EventLLMRequestDone:   true,
	EventLLMStreamSummary: true,
	EventToolFinished:     true,
}

// projectPayload 从任意 map 精确投影允许字段，并对正文类字段只保留存在性。
func (p *Projector) projectPayload(eventType string, payload map[string]interface{}) map[string]interface{} {
	if len(payload) == 0 {
		return nil
	}
	out := make(map[string]interface{})
	for key, value := range payload {
		if !payloadAllowKeys[key] {
			continue
		}
		switch typed := value.(type) {
		case string:
			// 仅允许明确低敏的短枚举/名称类字段。
			switch key {
			case "provider", "model", "protocol", "status", "state", "phase", "kind",
				"error_code", "error_category", "usage_source", "aggregation_level",
				"tool_name", "stream_id", "finish_reason", "reasoning_visibility",
				"renderer_id", "attempt_id", "turn_id":
				out[key] = boundUTF8String(typed, 512)
			default:
				// 其他字符串（可能的 URL/路径/内容）一律丢弃。
			}
		case bool:
			out[key] = typed
		default:
			// 数值、json.Number 等标量透传（JSON 编码范围安全）。
			out[key] = typed
		}
	}

	// 正文类字段只计算存在性指纹，绝不透传正文。
	if eventContentCapableTypes[eventType] {
		for key, domain := range contentFingerprintKeys {
			if raw, ok := payload[key]; ok {
				if s, ok := raw.(string); ok && s != "" {
					out["content_present"] = true
					out["content_bytes"] = len(s)
					out[key+"_fingerprint"] = p.fingerprint(domain, s)
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// fingerprint 通过 redactor 计算域分离指纹。
func (p *Projector) fingerprint(domain, data string) string {
	if p == nil || p.redactor == nil {
		return ""
	}
	return p.redactor.HMACFingerprint(domain, data)
}

// enforceEventSize 保证单事件不超过 MaxEventBytes；超限时收缩为统计摘要。
func (p *Projector) enforceEventSize(proj Event) Event {
	size, err := JSONSize(proj.Payload)
	if err != nil || size <= p.maxEventBytes {
		return proj
	}
	// 收缩：只保留关联与最基本信息。
	proj.Payload = map[string]interface{}{
		"truncated":      true,
		"payload_bytes":  size,
		"original_type": proj.Type,
	}
	return proj
}

// ProjectRetryEvent 映射 llm.RetryEvent → llm.retry 观测事件。
func (p *Projector) ProjectRetryEvent(event llm.RetryEvent) (Event, bool) {
	if p == nil {
		return Event{}, false
	}
	corr := Correlation{
		LLMRequestID:   event.LLMRequestID,
		RetryAttemptID: event.RetryAttemptID,
		StreamID:       event.StreamID,
	}
	if event.LogicalTurnID != "" {
		corr.TurnID = event.LogicalTurnID
	}
	if event.ProviderRequestID != "" && p.exposeProviderRequestID {
		corr.ProviderRequestID = event.ProviderRequestID
	}
	payload := map[string]interface{}{
		"provider":    boundUTF8String(event.Provider, 512),
		"protocol":    boundUTF8String(event.Protocol, 512),
		"model":       boundUTF8String(event.Model, 512),
		"attempt":     event.Attempt,
		"max_attempts": event.MaxAttempts,
		"retryable":   true,
		"retry_reason_category": errorCategory(event.RetryReason),
		"error_code":  boundUTF8String(event.ErrorCode, 512),
		"delay_ms":    event.RetryDelayMS,
	}
	proj := Event{
		Timestamp:     time.Now().UTC(),
		Type:          EventLLMRetry,
		Source:        "llm_retry",
		SchemaVersion: SchemaVersionEvent,
		Correlation:   corr,
		Payload:       payload,
	}
	return proj, true
}

// ProjectHTTPDebug 映射 llm.HTTPDebugEvent → attempt/stream 观测事件。
func (p *Projector) ProjectHTTPDebug(event llm.HTTPDebugEvent) (Event, uint64, bool) {
	if p == nil {
		return Event{}, 0, false
	}
	corr := Correlation{
		LLMRequestID: event.LLMRequestID,
		RetryAttemptID: event.RetryAttemptID,
		StreamID:       event.StreamID,
	}
	if event.LogicalTurnID != "" {
		corr.TurnID = event.LogicalTurnID
	}
	if event.ProviderRequestID != "" && p.exposeProviderRequestID {
		corr.ProviderRequestID = event.ProviderRequestID
	}

	// 原始 body/url/headers 永不进入 safe stream；只投影受控域。
	payload := p.projectHTTPDebugPayload(event)
	var eventType string
	switch {
	case strings.Contains(strings.ToLower(event.Phase), "retry") || event.RetryReason != "" || event.RetryDelayMS > 0:
		eventType = EventLLMRetry
	case strings.Contains(strings.ToLower(event.Phase), "start"):
		eventType = EventLLMAttemptStart
	default:
		eventType = EventLLMAttemptDone
	}
	dedupKey := dedupKeyFor(event.Source, "", event.LLMRequestID, eventType, event.RetryAttemptID)
	proj := Event{
		Timestamp:     time.Now().UTC(),
		Type:          eventType,
		Source:        "llm_http_debug",
		SchemaVersion: SchemaVersionEvent,
		Correlation:   corr,
		Payload:       payload,
	}
	return proj, dedupKey, true
}

// projectHTTPDebugPayload 只投影 provider/protocol/model/attempt/status/error 类别。
func (p *Projector) projectHTTPDebugPayload(event llm.HTTPDebugEvent) map[string]interface{} {
	payload := map[string]interface{}{
		"provider":    boundUTF8String(event.Provider, 256),
		"protocol":    boundUTF8String(event.Protocol, 256),
		"model":       boundUTF8String(event.Model, 256),
		"attempt":     event.Attempt,
		"max_attempts": event.MaxAttempts,
	}
	if event.ResponseStatusCode > 0 {
		payload["status_class"] = statusClass(event.ResponseStatusCode)
		payload["http_status"] = event.ResponseStatusCode
	}
	if event.Error != "" || event.ErrorCode != "" {
		payload["error_code"] = boundUTF8String(event.ErrorCode, 256)
		payload["error_category"] = errorCategory(event.Error + " " + event.ErrorCode)
		payload["retryable"] = event.RetryReason != "" || event.RetryDelayMS > 0
	}
	if event.RequestBodyBytes > 0 {
		payload["request_bytes"] = event.RequestBodyBytes
	}
	if event.ResponseBodyBytes > 0 {
		payload["response_bytes"] = event.ResponseBodyBytes
	}
	return payload
}

// statusClass 把 HTTP 状态码映射为粗粒度类别。
func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "other"
	}
}

// errorCategory 把原始错误文本映射为稳定类别，不返回原始错误原文。
func errorCategory(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "rate limit") || strings.Contains(lower, "429"):
		return "rate_limit"
	case strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		return "timeout"
	case strings.Contains(lower, "auth") || strings.Contains(lower, "unauthor") || strings.Contains(lower, "apikey") || strings.Contains(lower, "permission"):
		return "auth"
	case strings.Contains(lower, "context_length") || strings.Contains(lower, "max context") || strings.Contains(lower, "tokens exceeded"):
		return "context_length"
	case strings.Contains(lower, "connect") || strings.Contains(lower, "dial") || strings.Contains(lower, "tls"):
		return "network"
	case lower == "":
		return "unknown"
	default:
		return "provider"
	}
}

// dedupKeyFor 生成用于去重的事实键：同一事实从 bus 与 durable store 双到达时只计一次。
func dedupKeyFor(source, sessionID, llmReqID, eventType, attemptID string) uint64 {
	key := strings.Join([]string{source, sessionID, llmReqID, eventType, attemptID}, "\x1f")
	return fnv1a(key)
}

func fnv1a(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// sortStringKeys 供测试与诊断使用。
func sortStringKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

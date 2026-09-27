package llm

import (
	stderrs "errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type httpStatusCoder interface {
	HTTPStatusCode() int
}

type providerHTTPError struct {
	message    string
	statusCode int
	retryAfter time.Duration
}

func newProviderHTTPError(statusCode int, body string, header http.Header) error {
	retryAfter, ok := retryAfterDelayFromHeader(header, time.Time{})
	if !ok {
		retryAfter, _ = retryAfterDelayFromBody(body)
	}
	return &providerHTTPError{
		message:    fmt.Sprintf("HTTP %d: %s", statusCode, body),
		statusCode: statusCode,
		retryAfter: retryAfter,
	}
}

func (e *providerHTTPError) Error() string {
	if e == nil {
		return ""
	}
	return e.message
}

func (e *providerHTTPError) HTTPStatusCode() int {
	if e == nil {
		return 0
	}
	return e.statusCode
}

func (e *providerHTTPError) RetryAfterDelay() time.Duration {
	if e == nil {
		return 0
	}
	return e.retryAfter
}

func isRetryableProviderCallError(err error) bool {
	return classifyRetryableLLMError(err).Retryable
}

func isRetryableProviderResponseError(err error) bool {
	if err == nil {
		return true
	}

	var exhaustedErr *retryExhaustedError
	if stderrs.As(err, &exhaustedErr) {
		return false
	}
	var suppressedErr *retrySuppressedError
	if stderrs.As(err, &suppressedErr) {
		return false
	}

	if IsContextWindowError(err) {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"invalid_request_error",
		"missing required parameter",
		"no tool call found for function call output",
		"no tool call found for function call",
		"unsupported parameter",
		"unrecognized request argument",
		"unknown parameter",
		"unexpected parameter",
		"invalid api key",
		"incorrect api key",
		"api_key_expired",
		"api key expired",
		"expired api key",
		"credential expired",
	} {
		if strings.Contains(lower, needle) {
			return false
		}
	}

	return true
}

// IsContextWindowError reports deterministic provider failures caused by an
// oversized prompt. Callers can compact or reduce the request before retrying.
func IsContextWindowError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	for _, needle := range []string{
		"context_length_exceeded",
		"context window exceeded",
		"exceeds the context window",
		"input exceeds the context",
		"input is too long for the context",
		"maximum context length",
		"prompt is too long",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

var maxTokensLimitPatterns = []*regexp.Regexp{
	// Anthropic-compatible: "max_tokens: 131072 > 128000, which is the maximum allowed number of output tokens for claude-fable-5"
	regexp.MustCompile(`(?i)max[_ ]?tokens[^0-9]{0,40}(\d+)\s*>\s*(\d+)`),
	// Alternate forms: "max_tokens must be <= 128000" / "maximum allowed ... is 128000"
	regexp.MustCompile(`(?i)max[_ ]?(?:output[_ ]?)?tokens[^0-9]{0,80}(?:must be|<=|at most|maximum(?: allowed)?(?: number of output tokens)?(?: for [^,]+)?(?: is|:))\s*(\d+)`),
	regexp.MustCompile(`(?i)maximum allowed number of output tokens(?: for [^,]+)?(?: is|:)\s*(\d+)`),
}

// ParseMaxTokensLimitError extracts the provider-reported output-token ceiling
// from deterministic max_tokens validation failures.
func ParseMaxTokensLimitError(err error) (limit int, ok bool) {
	if err == nil {
		return 0, false
	}
	message := err.Error()
	if strings.TrimSpace(message) == "" {
		return 0, false
	}
	lower := strings.ToLower(message)
	if !strings.Contains(lower, "max_tokens") &&
		!strings.Contains(lower, "max tokens") &&
		!strings.Contains(lower, "output tokens") &&
		!strings.Contains(lower, "max_output_tokens") {
		return 0, false
	}
	for _, pattern := range maxTokensLimitPatterns {
		matches := pattern.FindStringSubmatch(message)
		if len(matches) == 0 {
			continue
		}
		// Prefer the second capture group when present (requested > limit).
		candidate := matches[len(matches)-1]
		parsed, convErr := strconv.Atoi(candidate)
		if convErr != nil || parsed <= 0 {
			continue
		}
		return parsed, true
	}
	return 0, false
}

// applyMaxTokensLimitRecovery lowers currentMaxTokens to the provider-reported
// ceiling when the request budget was rejected. Returns true when the caller
// should rebuild and retry the request with the adjusted budget.
func applyMaxTokensLimitRecovery(currentMaxTokens *int, err error) bool {
	if currentMaxTokens == nil || err == nil {
		return false
	}
	limit, ok := ParseMaxTokensLimitError(err)
	if !ok || limit <= 0 {
		return false
	}
	// Only recover when the current budget exceeds the provider ceiling, or
	// when the budget was unset (0) and the provider reported an explicit limit.
	if *currentMaxTokens > 0 && *currentMaxTokens <= limit {
		return false
	}
	*currentMaxTokens = limit
	return true
}

// IsMaxTokensLimitError reports provider rejections caused by an oversized
// max_tokens / max_output_tokens request parameter.
func IsMaxTokensLimitError(err error) bool {
	_, ok := ParseMaxTokensLimitError(err)
	return ok
}

// MetadataKeyStripReasoningClientState asks protocol adapters to omit replayed
// client-side reasoning state (Codex reasoning items carrying
// encrypted_content) from this request. It is set internally by the one-shot
// invalid-encrypted-content recovery; adapter/codex.go mirrors the literal.
const MetadataKeyStripReasoningClientState = "strip_reasoning_client_state"

// isInvalidEncryptedContentError reports the deterministic upstream rejection
// of a replayed reasoning item whose encrypted_content the provider cannot
// verify: HTTP 400 with code invalid_encrypted_content ("The encrypted content
// for item rs_... could not be verified. Reason: Encrypted content could not be
// decrypted or parsed.").
func isInvalidEncryptedContentError(err error) bool {
	if err == nil {
		return false
	}
	if statusCode, ok := providerCallHTTPStatus(err); ok && statusCode != http.StatusBadRequest {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "invalid_encrypted_content") ||
		(strings.Contains(lower, "encrypted content") && strings.Contains(lower, "could not be verified"))
}

// applyInvalidEncryptedContentRecovery reports whether the caller should rebuild
// the request with client-side reasoning replay stripped and try once more.
// stripped is the one-shot latch: after the first recovery the same error is
// terminal again, so an unverifiable transcript cannot be replayed forever.
func applyInvalidEncryptedContentRecovery(stripped *bool, enabled bool, err error) bool {
	if stripped == nil || *stripped || !enabled || err == nil {
		return false
	}
	if !isInvalidEncryptedContentError(err) {
		return false
	}
	*stripped = true
	return true
}

// withStrippedReasoningClientState clones request metadata with the client-state
// strip flag set. The clone keeps the caller's metadata map untouched, so the
// flag cannot leak into the session history or a later request.
func withStrippedReasoningClientState(metadata map[string]interface{}) map[string]interface{} {
	clone := cloneMapStringAny(metadata)
	clone[MetadataKeyStripReasoningClientState] = true
	return clone
}

const (
	// outputBudgetEscalationMaxCount bounds how many times a single request may
	// widen its output budget after a completion-budget-bound degenerate reply
	// (reasoning-only empty reply, truncated tool call, malformed tool-call
	// arguments).
	outputBudgetEscalationMaxCount = 2

	// outputBudgetEscalationCeiling caps the widened output budget. Aligned with
	// the caller-side one-shot escalation target (EscalatedMaxTokens): for a
	// request whose budget is above the capped default the loop deliberately does
	// not escalate (shouldEscalate* requires MaxTokens <= CappedDefaultMaxTokens),
	// so this ceiling is the only lever left and must not stop below the budget
	// the loop itself would have used.
	outputBudgetEscalationCeiling = EscalatedMaxTokens

	// degenerateOutputReplyMaxStreak bounds how many consecutive degenerate
	// replies (reasoning-only / empty) a single call may sample before the retry
	// loop stops. The widening path above gets its two escalations, so a third
	// identical sample means the model is stuck on the same prompt and replaying
	// it again only burns wall-clock: the incident signature was 10 attempts at
	// ~64s each while every attempt returned reasoning only (finish_reason=length)
	// and neither content nor a tool call.
	degenerateOutputReplyMaxStreak = 3
)

// isOutputBudgetEscalationReason reports whether a retry reason is bound to an
// exhausted completion budget, where doubling max_tokens is what changes the
// next sample: reasoning_only_empty_reply spends the whole budget on reasoning
// and returns neither content nor a tool call, while truncated_tool_call cuts a
// tool call mid-markup (typically finish_reason=length). Other degenerate
// reasons (empty_reply) are not budget-bound and must not widen the request.
//
// invalid_tool_arguments is deliberately NOT listed here: the retry reason only
// says the aggregated arguments failed to parse as a JSON object, which covers
// both budget cut-offs and plain JSON syntax degeneration (e.g. `{"timeout":
// 60s}` with finish_reason=tool_calls). Escalation for that class requires
// truncation evidence, see escalateOutputBudgetForDegenerateReply.
func isOutputBudgetEscalationReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "reasoning_only_empty_reply", "truncated_tool_call":
		return true
	default:
		return false
	}
}

// toolCallTruncationEvidence is implemented by adapter.MalformedToolCallError:
// it reports whether the malformed arguments were cut off by the completion
// budget (finish_reason=length/max_tokens) rather than being a JSON syntax
// degeneration. Declared as a narrow interface here so the retry policy does
// not depend on adapter error construction details.
type toolCallTruncationEvidence interface {
	ToolCallArgumentsTruncated() bool
}

// hasToolCallTruncationEvidence reports whether err carries positive evidence
// that the tool-call arguments were truncated by the output budget. Wrapped
// errors are supported via errors.As.
func hasToolCallTruncationEvidence(err error) bool {
	if err == nil {
		return false
	}
	var evidence toolCallTruncationEvidence
	if stderrs.As(err, &evidence) {
		return evidence.ToolCallArgumentsTruncated()
	}
	return false
}

// escalateOutputBudgetForDegenerateReply widens max_tokens after a degenerate
// reply that exhausted the completion budget (a reasoning-only empty reply, or
// a tool call truncated mid-markup). Replaying the identical budget mostly
// reproduces the same degenerate sample, so the budget handed to the next
// attempt is doubled instead. The retry policy is untouched; the widening is
// bounded by an absolute ceiling and a per-call escalation count. A request
// without an explicit budget (0) is no longer left alone: the provider default
// still governs the wire value, but replaying the identical request never
// changes a degenerate sample, so the model default budget
// (DefaultModelMaxOutputTokens, before the 8k slot-reservation cap) is used as
// the escalation baseline instead. The widened value only ever moves upward,
// and stays bounded by the same ceiling and per-call escalation count.
func escalateOutputBudgetForDegenerateReply(currentMaxTokens *int, escalations int, err error) bool {
	if currentMaxTokens == nil || err == nil || escalations >= outputBudgetEscalationMaxCount {
		return false
	}
	reason := classifyRetryableLLMError(err).Reason
	if !isOutputBudgetEscalationReason(reason) {
		// invalid_tool_arguments 只有确认被 completion 预算截断时才扩大预算；
		// 裸 duration / 双重字符串等语法退化与预算无关，翻倍只会用同样的
		// prompt 重复采样（2026-09-27 证据：同参数哈希连续 3 次 attempt
		// 完全相同，max_tokens 32000→64000，completion_tokens 仅 98/259）。
		if reason != "invalid_tool_arguments" || !hasToolCallTruncationEvidence(err) {
			return false
		}
	}
	current := *currentMaxTokens
	if current <= 0 {
		// 请求未显式设置预算：provider 默认值不受本层控制，但「原样重放」无法
		// 改变退化样本，所以以模型默认预算作为升级起点（只升不降）。
		current = DefaultModelMaxOutputTokens
	}
	if current <= 0 || current >= outputBudgetEscalationCeiling {
		return false
	}
	widened := current * 2
	if widened > outputBudgetEscalationCeiling {
		widened = outputBudgetEscalationCeiling
	}
	if widened <= *currentMaxTokens {
		return false
	}
	*currentMaxTokens = widened
	return true
}

// trackDegenerateOutputReply advances the consecutive-degenerate-reply streak
// and reports whether the retry loop must stop. A degenerate sample means the
// model spent its completion budget on reasoning and returned neither content
// nor a tool call, or cut a tool call off mid-markup; widening the budget is the
// only lever that changes the next sample, so once the streak reaches
// degenerateOutputReplyMaxStreak the loop returns a terminal exhaustion error
// instead of replaying the identical request until the whole attempt budget is
// gone. Any other error (transport, server, quota, ...) resets the streak,
// mirroring trackHeaderTimeoutStreak.
func trackDegenerateOutputReply(consecutive *int, err error) (exhausted bool) {
	if err == nil {
		return false
	}
	if !isDegenerateOutputRetryReason(classifyRetryableLLMError(err).Reason) {
		*consecutive = 0
		return false
	}
	*consecutive++
	return *consecutive >= degenerateOutputReplyMaxStreak
}

// malformedSyntaxResampleLimit 是「语法类 invalid_tool_arguments」在 provider 层
// 允许的同预算重采样次数上限：首败后只允许 1 次换采样（温度随机性仍可能恢复），
// 再失败就停止重放。
//
// 依据（2026-09-27）：同一参数哈希的连续 attempt 完全相同，max_tokens
// 32000→64000 而 completion_tokens 仅 98/259；继续重放只烧预算，真正的恢复杠杆
// 是 agent 层把「参数非法 + schema」回注给模型（prompt 改变 → 请求 hash 改变）。
// 真截断（有截断证据）走预算扩容路径，不计入该上限，也不受其约束。
const malformedSyntaxResampleLimit = 1

// trackMalformedSyntaxResample 统计同一调用内语法类 malformed 参数的重采样次数，
// 达到上限时报告 true（重试循环必须停止并交回上层）。
func trackMalformedSyntaxResample(count *int, err error) bool {
	if count == nil || err == nil {
		return false
	}
	if classifyRetryableLLMError(err).Reason != "invalid_tool_arguments" {
		return false
	}
	if hasToolCallTruncationEvidence(err) {
		return false
	}
	*count++
	return *count > malformedSyntaxResampleLimit
}

// isHandoffEligibleError reports whether an inner-loop exhaustion should hand
// the transient failure to the enclosing runtime retry loop instead of
// becoming terminal. Transport-class failures (connection drops, SSE stream
// EOF, header timeouts) and transient stream/server failures benefit from a
// fresh outer-layer request: the inner loop fast-fails on the tighter
// transport budget, and the outer loop provides the total attempt guarantee
// with its own finite budget.
func isHandoffEligibleError(err error) bool {
	if err == nil {
		return false
	}
	decision := classifyRetryableLLMError(err)
	if !decision.Retryable {
		return false
	}
	// 任意 5xx 都是上游不可用：除列出的 500/502/503/504 外，529（Anthropic
	// overloaded）、520-524（Cloudflare 源站故障）、598 等也应交给外层重试
	// 循环做 provider/key 轮换，而不是在内层预算耗尽后直接终态。
	if strings.HasPrefix(decision.Reason, "http_5") {
		return true
	}
	switch decision.Reason {
	case "transport", "transient_stream_or_server", "stream_interrupted",
		"empty_reply", "reasoning_only_empty_reply", "insufficient_system_resource",
		"rate_limit", "http_429", "http_408", "http_409",
		"http_500", "http_502", "http_503", "http_504":
		return true
	}
	return false
}

func providerCallHTTPStatus(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var coder httpStatusCoder
	if stderrs.As(err, &coder) {
		if statusCode := coder.HTTPStatusCode(); statusCode > 0 {
			return statusCode, true
		}
	}

	lower := strings.ToLower(err.Error())
	const marker = "http "
	for start := 0; start < len(lower); {
		offset := strings.Index(lower[start:], marker)
		if offset == -1 {
			return 0, false
		}

		index := start + offset + len(marker)
		if index+3 <= len(lower) {
			if code, convErr := strconv.Atoi(lower[index : index+3]); convErr == nil {
				return code, true
			}
		}
		start = index
	}

	return 0, false
}

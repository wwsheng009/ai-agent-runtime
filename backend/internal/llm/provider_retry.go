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

const (
	// outputBudgetEscalationMaxCount bounds how many times a single request may
	// widen its output budget after a completion-budget-bound degenerate reply
	// (reasoning-only empty reply, truncated tool call, malformed tool-call
	// arguments).
	outputBudgetEscalationMaxCount = 2

	// outputBudgetEscalationCeiling caps the widened output budget.
	outputBudgetEscalationCeiling = 32768

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
// tool call mid-markup (typically finish_reason=length). invalid_tool_arguments
// joins them because the aggregated arguments were cut off mid-JSON often enough
// (finish_reason=length) that a wider budget changes the next sample. Other
// degenerate reasons (empty_reply) are not budget-bound and must not widen the
// request.
func isOutputBudgetEscalationReason(reason string) bool {
	switch strings.TrimSpace(reason) {
	case "reasoning_only_empty_reply", "truncated_tool_call", "invalid_tool_arguments":
		return true
	default:
		return false
	}
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
	if !isOutputBudgetEscalationReason(classifyRetryableLLMError(err).Reason) {
		return false
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

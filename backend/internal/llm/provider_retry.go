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

package llm

import (
	stderrs "errors"
	"fmt"
	"net/http"
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
		"context_length_exceeded",
		"context window exceeded",
		"maximum context length",
		"prompt is too long",
		"invalid api key",
		"incorrect api key",
	} {
		if strings.Contains(lower, needle) {
			return false
		}
	}

	return true
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

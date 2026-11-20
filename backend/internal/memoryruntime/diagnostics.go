package memoryruntime

import (
	"errors"
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/memoryredact"
)

// memoryHTTPStatusCoder is intentionally a small optional interface. The LLM
// package exposes the status code for classification, but does not expose the
// provider response body to memory persistence.
type memoryHTTPStatusCoder interface {
	HTTPStatusCode() int
}

// memoryStageError is the boundary between provider failures and durable
// memory diagnostics. Its Error method never includes the upstream response
// body, which may contain credentials or vendor-specific sensitive content.
type memoryStageError struct {
	stage             string
	requestedProvider string
	resolvedProvider  string
	requestedModel    string
	resolvedModel     string
	cause             error
}

func newMemoryStageError(stage, requestedProvider, resolvedProvider, requestedModel, resolvedModel string, cause error) error {
	if cause == nil {
		return nil
	}
	return &memoryStageError{
		stage:             stage,
		requestedProvider: requestedProvider,
		resolvedProvider:  resolvedProvider,
		requestedModel:    requestedModel,
		resolvedModel:     resolvedModel,
		cause:             cause,
	}
}

func (e *memoryStageError) Error() string {
	if e == nil {
		return ""
	}
	parts := []string{"memoryruntime:", "stage=" + safeMemoryLabel(e.stage)}
	if requested := safeMemoryLabel(e.requestedProvider); requested != "" {
		parts = append(parts, "requested_provider="+requested)
	}
	if resolved := safeMemoryLabel(e.resolvedProvider); resolved != "" {
		parts = append(parts, "resolved_provider="+resolved)
	}
	if requested := safeMemoryLabel(e.requestedModel); requested != "" {
		parts = append(parts, "requested_model="+requested)
	}
	if resolved := safeMemoryLabel(e.resolvedModel); resolved != "" {
		parts = append(parts, "resolved_model="+resolved)
	}
	if status := memoryHTTPStatus(e.cause); status > 0 {
		parts = append(parts, fmt.Sprintf("error_class=%s", memoryHTTPErrorClass(status)))
		parts = append(parts, fmt.Sprintf("http_status=%d", status))
	} else {
		parts = append(parts, "error_class=provider_failure")
		if detail := safeMemoryErrorDetail(e.cause); detail != "" {
			parts = append(parts, "detail="+detail)
		}
	}
	return strings.Join(parts, " ")
}

func (e *memoryStageError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// memorySanitizedError keeps errors.Is/errors.As working while replacing the
// persisted/displayed text with a redacted form.
type memorySanitizedError struct {
	cause error
	text  string
}

func (e *memorySanitizedError) Error() string {
	if e == nil {
		return ""
	}
	return e.text
}

func (e *memorySanitizedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func safeMemoryError(err error) error {
	if err == nil {
		return nil
	}
	// Keep an already classified stage error intact; callers can safely return
	// it and use the same representation for durable job state.
	var stageErr *memoryStageError
	if errors.As(err, &stageErr) {
		return stageErr
	}
	var sanitized *memorySanitizedError
	if errors.As(err, &sanitized) {
		return sanitized
	}
	return &memorySanitizedError{cause: err, text: safeMemoryErrorText(err)}
}

func safeMemoryErrorText(err error) string {
	if err == nil {
		return ""
	}
	var stageErr *memoryStageError
	if errors.As(err, &stageErr) {
		return stageErr.Error()
	}
	if status := memoryHTTPStatus(err); status > 0 {
		return fmt.Sprintf("memoryruntime: upstream request failed error_class=%s http_status=%d",
			memoryHTTPErrorClass(status), status)
	}
	return safeMemoryErrorDetail(err)
}

func memoryHTTPStatus(err error) int {
	var statusErr memoryHTTPStatusCoder
	if errors.As(err, &statusErr) && statusErr != nil {
		return statusErr.HTTPStatusCode()
	}
	return 0
}

func memoryHTTPErrorClass(status int) string {
	switch {
	case status == 401 || status == 403:
		return "authentication"
	case status == 408 || status == 425 || status == 429:
		return "rate_or_timeout"
	case status >= 500 && status <= 599:
		return "upstream"
	case status >= 400 && status <= 499:
		return "request"
	default:
		return "http"
	}
}

func safeMemoryErrorDetail(err error) string {
	if err == nil {
		return ""
	}
	detail, _ := memoryredact.Redact(strings.TrimSpace(err.Error()))
	detail = strings.Join(strings.Fields(detail), " ")
	if runes := []rune(detail); len(runes) > 240 {
		detail = string(runes[:237]) + "..."
	}
	return detail
}

func safeMemoryLabel(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), "_")
	if runes := []rune(value); len(runes) > 96 {
		value = string(runes[:96])
	}
	return value
}

package memoryruntime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

type diagnosticHTTPError struct {
	status int
	body   string
}

func (e diagnosticHTTPError) Error() string {
	return fmt.Sprintf("upstream returned %d: %s", e.status, e.body)
}

func (e diagnosticHTTPError) HTTPStatusCode() int {
	return e.status
}

func TestMemoryStageErrorClassifiesAndSanitizesHTTPFailures(t *testing.T) {
	const secret = "INVALID_API_KEY sk-live-1234567890"
	err := newMemoryStageError(
		"extraction",
		"configured-provider",
		"resolved-provider",
		"configured-model",
		"resolved-model",
		diagnosticHTTPError{status: 401, body: secret},
	)

	require.Error(t, err)
	text := err.Error()
	require.Contains(t, text, "stage=extraction")
	require.Contains(t, text, "error_class=authentication")
	require.Contains(t, text, "http_status=401")
	require.NotContains(t, text, "INVALID_API_KEY")
	require.NotContains(t, text, "sk-live-1234567890")
	require.NotContains(t, safeMemoryErrorText(err), "INVALID_API_KEY")
	require.NotContains(t, safeMemoryErrorText(err), "sk-live-1234567890")
}

func TestMemoryHTTPErrorClass(t *testing.T) {
	for _, tc := range []struct {
		status int
		class  string
	}{
		{status: 401, class: "authentication"},
		{status: 403, class: "authentication"},
		{status: 408, class: "rate_or_timeout"},
		{status: 425, class: "rate_or_timeout"},
		{status: 429, class: "rate_or_timeout"},
		{status: 404, class: "request"},
		{status: 422, class: "request"},
		{status: 500, class: "upstream"},
		{status: 503, class: "upstream"},
		{status: 301, class: "http"},
	} {
		t.Run(fmt.Sprintf("%d", tc.status), func(t *testing.T) {
			require.Equal(t, tc.class, memoryHTTPErrorClass(tc.status))
		})
	}
}

func TestSafeMemoryErrorPreservesCancellationChain(t *testing.T) {
	sanitized := safeMemoryError(context.Canceled)
	require.Error(t, sanitized)
	require.Equal(t, "context canceled", sanitized.Error())
	require.ErrorIs(t, sanitized, context.Canceled)
}

func TestSafeMemoryErrorPreservesWrappedCancellationChain(t *testing.T) {
	cause := fmt.Errorf("worker stopped: %w", context.Canceled)
	sanitized := safeMemoryError(cause)
	require.Error(t, sanitized)
	require.ErrorIs(t, sanitized, context.Canceled)
	require.True(t, errors.Is(sanitized, context.Canceled))
}

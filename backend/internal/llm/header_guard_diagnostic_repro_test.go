package llm

import (
	"fmt"
	"io"
	"testing"
	"time"
)

// TestDiagnoseFailureHeaderGuardExhaustionIsRetryable reproduces the exact
// production error shape reported by the user:
//
//	model error [UPSTREAM_UNAVAILABLE, retryable=false] provider transport
//	stream failed after retries: failed to send request: timeout awaiting
//	response headers (response-header guard after 20s)
//	[action] Automatic retries are exhausted; switch providers or report the
//	upstream blocker.
//
// The cause chain built here mirrors provider.go callStreamingAggregate:
// responseHeaderTimeoutError (doRequest guard) -> fmt.Errorf("failed to send
// request: %w") -> markRetryExhausted("provider transport stream failed after
// retries", transportBudget, cause).
func TestDiagnoseFailureHeaderGuardExhaustionIsRetryable(t *testing.T) {
	guardErr := &responseHeaderTimeoutError{timeout: 20 * time.Second}
	cause := fmt.Errorf("failed to send request: %w", guardErr)
	exhausted := markRetryExhausted("provider transport stream failed after retries", 3, cause)

	if exhausted.Error() != "provider transport stream failed after retries: failed to send request: timeout awaiting response headers (response-header guard after 20s)" {
		t.Fatalf("unexpected message: %q", exhausted.Error())
	}

	diag := DiagnoseFailure(exhausted)
	if diag.ErrorCode != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("ErrorCode = %q, want UPSTREAM_UNAVAILABLE", diag.ErrorCode)
	}
	if !diag.Retryable {
		t.Fatalf("Retryable = false, want true; next_action=%q", diag.NextAction)
	}
	if diag.NextAction != "Retry with bounded backoff, then switch providers or report the blocker after repeated failure." {
		t.Fatalf("NextAction = %q", diag.NextAction)
	}
}

// TestDiagnoseFailureRequestPhaseEOFExhaustionIsRetryable reproduces the exact
// request-phase EOF production shape reported by the user:
//
//	model error [UPSTREAM_UNAVAILABLE, retryable=false] provider transport
//	stream failed after retries: failed to send request: Post
//	"https://api.b.ai/v1/chat/completions": EOF
//	[action] Automatic retries are exhausted; switch providers or report the
//	upstream blocker.
//
// Unlike the response-header guard case, this is an io.EOF while the request
// bytes are still being sent (the connection is dropped before any response
// headers). The chain mirrors provider.go callStreamingAggregate:
// io.EOF -> fmt.Errorf("failed to send request: %w") ->
// markRetryExhaustedForNextLayer("provider transport stream failed after
// retries", transportBudget, cause). DiagnoseFailure must still surface the
// transient cause as retryable so session-level recovery keeps trying.
func TestDiagnoseFailureRequestPhaseEOFExhaustionIsRetryable(t *testing.T) {
	cause := fmt.Errorf("failed to send request: Post \"https://api.b.ai/v1/chat/completions\": %w", io.EOF)
	exhausted := markRetryExhaustedForNextLayer("provider transport stream failed after retries", 2, cause)

	if exhausted.Error() != "provider transport stream failed after retries: failed to send request: Post \"https://api.b.ai/v1/chat/completions\": EOF" {
		t.Fatalf("unexpected message: %q", exhausted.Error())
	}

	diag := DiagnoseFailure(exhausted)
	if diag.ErrorCode != "UPSTREAM_UNAVAILABLE" {
		t.Fatalf("ErrorCode = %q, want UPSTREAM_UNAVAILABLE", diag.ErrorCode)
	}
	if !diag.Retryable {
		t.Fatalf("Retryable = false, want true; next_action=%q", diag.NextAction)
	}
	if diag.NextAction != "Retry with bounded backoff, then switch providers or report the blocker after repeated failure." {
		t.Fatalf("NextAction = %q", diag.NextAction)
	}
}

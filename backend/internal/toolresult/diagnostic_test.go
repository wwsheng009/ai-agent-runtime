package toolresult

import (
	"strings"
	"testing"

	runtimeerrors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
)

func TestDiagnoseFailureUsesStructuredMetadataAndAction(t *testing.T) {
	diagnostic := Diagnose("task_output", "call-1", "background job failed", map[string]interface{}{
		"tool_metadata": map[string]interface{}{
			"error_code": string(runtimeerrors.ErrJobNotFound),
		},
	})
	if diagnostic.OK {
		t.Fatal("expected failed tool invocation")
	}
	if diagnostic.ErrorCode != string(runtimeerrors.ErrJobNotFound) {
		t.Fatalf("expected job-not-found code, got %#v", diagnostic)
	}
	if diagnostic.Retryable {
		t.Fatal("job-not-found must not be blindly retried")
	}
	if !strings.HasPrefix(diagnostic.NextAction, "Use the") {
		t.Fatalf("expected precise id recovery action, got %q", diagnostic.NextAction)
	}
}

func TestDiagnoseSuccessDoesNotPromoteUnderlyingJobError(t *testing.T) {
	diagnostic := Diagnose("task_output", "call-2", "", map[string]interface{}{
		"error_code": string(runtimeerrors.ErrToolTimeout),
	})
	if !diagnostic.OK || diagnostic.ErrorCode != "" || diagnostic.Retryable {
		t.Fatalf("expected successful query contract, got %#v", diagnostic)
	}
}

func TestDiagnoseTimeoutIsRetryableWithSideEffectWarning(t *testing.T) {
	diagnostic := Diagnose("bash", "call-3", "[TOOL_TIMEOUT] command timed out", nil)
	if diagnostic.ErrorCode != string(runtimeerrors.ErrToolTimeout) || !diagnostic.Retryable {
		t.Fatalf("expected retryable timeout, got %#v", diagnostic)
	}
	if diagnostic.NextAction == "" {
		t.Fatal("expected timeout next action")
	}
}

func TestApplyDiagnosticMetadata(t *testing.T) {
	metadata := map[string]interface{}{}
	diagnostic := Diagnose("view", "call-4", "permission denied", nil)
	ApplyDiagnosticMetadata(metadata, diagnostic)
	if metadata[MetadataOKKey] != false {
		t.Fatalf("expected ok=false, got %#v", metadata)
	}
	if metadata[MetadataErrorCodeKey] != string(runtimeerrors.ErrAgentPermission) {
		t.Fatalf("expected permission code, got %#v", metadata)
	}
	if metadata[MetadataRetryableKey] != false || metadata[MetadataNextActionKey] == "" {
		t.Fatalf("expected non-retryable action metadata, got %#v", metadata)
	}
}

func TestDiagnoseClassifiesCommonRecoveryModes(t *testing.T) {
	testCases := []struct {
		name      string
		message   string
		code      runtimeerrors.ErrorCode
		retryable bool
	}{
		{name: "json arguments", message: "json: cannot unmarshal number into Go value", code: runtimeerrors.ErrToolInvalidArgs},
		{name: "windows path", message: "The system cannot find the path specified", code: runtimeerrors.ErrToolPathNotFound},
		{name: "network", message: "dial tcp: connection refused", code: runtimeerrors.ErrNetworkUnavailable, retryable: true},
		{name: "rate limit", message: "HTTP 429 rate limit exceeded", code: runtimeerrors.ErrAPIRateLimit, retryable: true},
		{name: "quota auth", message: "HTTP 403 insufficient user quota", code: runtimeerrors.ErrAPIUnauthorized},
		{name: "server", message: "HTTP 503 service unavailable", code: runtimeerrors.ErrAPIServerError, retryable: true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			diagnostic := Diagnose("test_tool", "call-test", tc.message, nil)
			if diagnostic.ErrorCode != string(tc.code) || diagnostic.Retryable != tc.retryable {
				t.Fatalf("unexpected diagnostic for %q: %#v", tc.message, diagnostic)
			}
			if diagnostic.NextAction == "" {
				t.Fatalf("expected next action for %q", tc.message)
			}
		})
	}
}

func TestDiagnoseHonorsExplicitRetryDisposition(t *testing.T) {
	diagnostic := Diagnose("remote_call", "call-explicit", "connection refused", map[string]interface{}{
		"error_code":  string(runtimeerrors.ErrNetworkUnavailable),
		"retryable":   false,
		"next_action": "Switch to the local fallback.",
	})
	if diagnostic.Retryable || diagnostic.NextAction != "Switch to the local fallback." {
		t.Fatalf("expected explicit runtime disposition, got %#v", diagnostic)
	}
}

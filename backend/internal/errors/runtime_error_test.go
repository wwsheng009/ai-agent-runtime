package errors

import (
	"errors"
	"testing"
)

func TestRuntimeError_Error(t *testing.T) {
	tests := []struct {
		name    string
		err     *RuntimeError
		wantSub string
	}{
		{
			name: "error without cause",
			err: &RuntimeError{
				Code:    ErrNetworkTimeout,
				Message: "connection timed out",
				Cause:   nil,
			},
			wantSub: "[NETWORK_TIMEOUT] connection timed out",
		},
		{
			name: "error with cause",
			err: &RuntimeError{
				Code:    ErrAPIServerError,
				Message: "server error",
				Cause:   errors.New("500 Internal Server Error"),
			},
			wantSub: "[API_SERVER_ERROR] server error: 500 Internal Server Error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if !contains(got, tt.wantSub) {
				t.Errorf("RuntimeError.Error() = %v, want substring %v", got, tt.wantSub)
			}
		})
	}
}

func TestWrap(t *testing.T) {
	baseErr := errors.New("base error")
	err := Wrap(ErrAPINotFound, "resource not found", baseErr)

	if err.Code != ErrAPINotFound {
		t.Errorf("Wrap() code = %v, want %v", err.Code, ErrAPINotFound)
	}

	if err.Message != "resource not found" {
		t.Errorf("Wrap() message = %v, want %v", err.Message, "resource not found")
	}

	if err.Cause != baseErr {
		t.Errorf("Wrap() cause = %v, want %v", err.Cause, baseErr)
	}
}

func TestWrapWithContext(t *testing.T) {
	baseErr := errors.New("base error")
	ctx := map[string]interface{}{
		"user_id": "12345",
		"action":  "test",
	}

	err := WrapWithContext(ErrAPIRateLimit, "rate limit exceeded", baseErr, ctx)

	if err.Code != ErrAPIRateLimit {
		t.Errorf("WrapWithContext() code = %v, want %v", err.Code, ErrAPIRateLimit)
	}

	gotCtx := err.GetContext()
	if gotCtx["user_id"] != "12345" {
		t.Errorf("WrapWithContext() context['user_id'] = %v, want %v", gotCtx["user_id"], "12345")
	}
}

func TestUnwrap(t *testing.T) {
	baseErr := errors.New("base error")
	err := Wrap(ErrNetworkUnavailable, "network down", baseErr)

	unwrapped := err.Unwrap()
	if unwrapped != baseErr {
		t.Errorf("Unwrap() = %v, want %v", unwrapped, baseErr)
	}
}

func TestIs(t *testing.T) {
	err := Wrap(ErrToolExecution, "tool failed", nil)

	if !Is(err, ErrToolExecution) {
		t.Errorf("Is() should return true for matching error code")
	}

	if Is(err, ErrNetworkTimeout) {
		t.Errorf("Is() should return false for non-matching error code")
	}
}

func TestRuntimeError_ContextMethods(t *testing.T) {
	ctx := map[string]interface{}{
		"key1": "value1",
		"key2": 123,
	}

	err := &RuntimeError{
		Code:    ErrValidationFailed,
		Message: "validation error",
		Context: ctx,
	}

	// Test HasContext
	if !err.HasContext("key1") {
		t.Errorf("HasContext() should return true for existing key")
	}

	if err.HasContext("key3") {
		t.Errorf("HasContext() should return false for non-existing key")
	}

	// Test GetContextValue
	val, ok := err.GetContextValue("key1")
	if !ok {
		t.Errorf("GetContextValue() should return true for existing key")
	}
	if val != "value1" {
		t.Errorf("GetContextValue() = %v, want %v", val, "value1")
	}

	_, ok = err.GetContextValue("key3")
	if ok {
		t.Errorf("GetContextValue() should return false for non-existing key")
	}

	// Test WithContext
	newErr := err.WithContext("key3", "value3")
	val, ok = newErr.GetContextValue("key3")
	if !ok || val != "value3" {
		t.Errorf("WithContext() failed to add new context")
	}

	// Original context should not be modified
	_, ok = err.GetContextValue("key3")
	if ok {
		t.Errorf("WithContext() should not modify original error")
	}
}

func TestNew(t *testing.T) {
	err := New(ErrConfigInvalid, "configuration is invalid")

	if err.Code != ErrConfigInvalid {
		t.Errorf("New() code = %v, want %v", err.Code, ErrConfigInvalid)
	}

	if err.Message != "configuration is invalid" {
		t.Errorf("New() message = %v, want 'configuration is invalid'", err.Message)
	}

	if err.Cause != nil {
		t.Errorf("New() cause should be nil")
	}
}

func TestNewf(t *testing.T) {
	err := Newf(ErrConfigNotFound, "file %s not found", "/path/to/config")

	if err.Code != ErrConfigNotFound {
		t.Errorf("Newf() code = %v, want %v", err.Code, ErrConfigNotFound)
	}

	wantMessage := "file /path/to/config not found"
	if err.Message != wantMessage {
		t.Errorf("Newf() message = %v, want %v", err.Message, wantMessage)
	}
}

func TestPrepend(t *testing.T) {
	err := New(ErrAPIBadRequest, "bad request")
	prepended := err.Prepend("Validation: ")

	wantPart := "[API_BAD_REQUEST] Validation: bad request"
	if !contains(prepended.Error(), wantPart) {
		t.Errorf("Prepend() = %v, want substring %v", prepended.Error(), wantPart)
	}
}

func TestGetContext(t *testing.T) {
	ctx := map[string]interface{}{
		"user": "test",
	}

	err := &RuntimeError{
		Code:    ErrAgentMaxSteps,
		Message: "max steps reached",
		Context: ctx,
	}

	gotCtx := err.GetContext()

	// Should be a copy, not reference
	gotCtx["user"] = "modified"

	original, ok := err.GetContextValue("user")
	if !ok || original != "test" {
		t.Errorf("GetContext() should return a copy, got %v", original)
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(substr) <= len(s) && 
		(s == substr || (len(s) > len(substr) && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

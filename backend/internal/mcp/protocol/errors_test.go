package protocol

import (
	"encoding/json"
	"testing"
)

func TestErrorValues(t *testing.T) {
	expectedCodes := map[string]int{
		"ErrorCodeInvalidRequest":  -32600,
		"ErrorCodeMethodNotFound":  -32601,
		"ErrorCodeInvalidParams":   -32602,
		"ErrorCodeInternalError":   -32603,
		"ErrorCodeResourceNotFound": -32001,
		"ErrorCodeToolNotFound":    -32002,
		"ErrorCodeServerError":     -32003,
	}

	if ErrorCodeInvalidRequest != expectedCodes["ErrorCodeInvalidRequest"] {
		t.Errorf("ErrorCodeInvalidRequest = %d, want %d", ErrorCodeInvalidRequest, expectedCodes["ErrorCodeInvalidRequest"])
	}

	if ErrorCodeMethodNotFound != expectedCodes["ErrorCodeMethodNotFound"] {
		t.Errorf("ErrorCodeMethodNotFound = %d, want %d", ErrorCodeMethodNotFound, expectedCodes["ErrorCodeMethodNotFound"])
	}

	if ErrorCodeInvalidParams != expectedCodes["ErrorCodeInvalidParams"] {
		t.Errorf("ErrorCodeInvalidParams = %d, want %d", ErrorCodeInvalidParams, expectedCodes["ErrorCodeInvalidParams"])
	}

	if ErrorCodeInternalError != expectedCodes["ErrorCodeInternalError"] {
		t.Errorf("ErrorCodeInternalError = %d, want %d", ErrorCodeInternalError, expectedCodes["ErrorCodeInternalError"])
	}

	if ErrorCodeResourceNotFound != expectedCodes["ErrorCodeResourceNotFound"] {
		t.Errorf("ErrorCodeResourceNotFound = %d, want %d", ErrorCodeResourceNotFound, expectedCodes["ErrorCodeResourceNotFound"])
	}

	if ErrorCodeToolNotFound != expectedCodes["ErrorCodeToolNotFound"] {
		t.Errorf("ErrorCodeToolNotFound = %d, want %d", ErrorCodeToolNotFound, expectedCodes["ErrorCodeToolNotFound"])
	}

	if ErrorCodeServerError != expectedCodes["ErrorCodeServerError"] {
		t.Errorf("ErrorCodeServerError = %d, want %d", ErrorCodeServerError, expectedCodes["ErrorCodeServerError"])
	}
}

func TestPredefinedErrors(t *testing.T) {
	tests := []struct {
		name    string
		err     *Error
		code    int
		message string
	}{
		{
			name:    "ErrInvalidRequest",
			err:     ErrInvalidRequest,
			code:    ErrorCodeInvalidRequest,
			message: "Invalid Request",
		},
		{
			name:    "ErrMethodNotFound",
			err:     ErrMethodNotFound,
			code:    ErrorCodeMethodNotFound,
			message: "Method not found",
		},
		{
			name:    "ErrInvalidParams",
			err:     ErrInvalidParams,
			code:    ErrorCodeInvalidParams,
			message: "Invalid params",
		},
		{
			name:    "ErrInternalError",
			err:     ErrInternalError,
			code:    ErrorCodeInternalError,
			message: "Internal error",
		},
		{
			name:    "ErrResourceNotFound",
			err:     ErrResourceNotFound,
			code:    ErrorCodeResourceNotFound,
			message: "Resource not found",
		},
		{
			name:    "ErrToolNotFound",
			err:     ErrToolNotFound,
			code:    ErrorCodeToolNotFound,
			message: "Tool not found",
		},
		{
			name:    "ErrServerError",
			err:     ErrServerError,
			code:    ErrorCodeServerError,
			message: "Server error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err.Code != tt.code {
				t.Errorf("Error code = %d, want %d", tt.err.Code, tt.code)
			}

			if tt.err.Message != tt.message {
				t.Errorf("Error message = %s, want %s", tt.err.Message, tt.message)
			}
		})
	}
}

func TestNewError(t *testing.T) {
	err := NewError(ErrorCodeInvalidParams, "Invalid parameter", map[string]interface{}{
		"param": "test_param",
		"value": "invalid",
	})

	if err.Code != ErrorCodeInvalidParams {
		t.Errorf("Error code = %d, want %d", err.Code, ErrorCodeInvalidParams)
	}

	if err.Message != "Invalid parameter" {
		t.Errorf("Error message = %s, want 'Invalid parameter'", err.Message)
	}

	if err.Data == nil {
		t.Fatal("Expected error data, got nil")
	}

	data, ok := err.Data.(map[string]interface{})
	if !ok {
		t.Fatal("Expected error data to be a map")
	}

	if data["param"] != "test_param" {
		t.Errorf("Expected param 'test_param', got %v", data["param"])
	}
}

func TestNewError_WithoutData(t *testing.T) {
	err := NewError(ErrorCodeInternalError, "Something went wrong", nil)

	if err.Code != ErrorCodeInternalError {
		t.Errorf("Error code = %d, want %d", err.Code, ErrorCodeInternalError)
	}

	if err.Message != "Something went wrong" {
		t.Errorf("Error message = %s, want 'Something went wrong'", err.Message)
	}

	if err.Data != nil {
		t.Error("Expected no error data, got value")
	}
}

func TestNewInternalError(t *testing.T) {
	err := NewInternalError("Database connection failed")

	if err.Code != ErrorCodeInternalError {
		t.Errorf("Error code = %d, want %d", err.Code, ErrorCodeInternalError)
	}

	if err.Message != "Database connection failed" {
		t.Errorf("Error message = %s, want 'Database connection failed'", err.Message)
	}

	if err.Data != nil {
		t.Error("Expected no error data, got value")
	}
}

func TestNewMethodNotFoundError(t *testing.T) {
	err := NewMethodNotFoundError("tools/list")

	if err.Code != ErrorCodeMethodNotFound {
		t.Errorf("Error code = %d, want %d", err.Code, ErrorCodeMethodNotFound)
	}

	if err.Message != "Method not found" {
		t.Errorf("Error message = %s, want 'Method not found'", err.Message)
	}

	if err.Data != "tools/list" {
		t.Errorf("Expected error data 'tools/list', got %v", err.Data)
	}
}

func TestNewInvalidParamsError(t *testing.T) {
	err := NewInvalidParamsError("Missing required parameter 'name'")

	if err.Code != ErrorCodeInvalidParams {
		t.Errorf("Error code = %d, want %d", err.Code, ErrorCodeInvalidParams)
	}

	if err.Message != "Missing required parameter 'name'" {
		t.Errorf("Error message = %s, want 'Missing required parameter 'name''", err.Message)
	}

	if err.Data != nil {
		t.Error("Expected no error data, got value")
	}
}

func TestError_JSON(t *testing.T) {
	err := Error{
		Code:    ErrorCodeInvalidParams,
		Message: "Invalid parameter",
		Data:    "Additional details",
	}

	data, errJSON := json.Marshal(err)
	if errJSON != nil {
		t.Fatalf("Failed to marshal error: %v", errJSON)
	}

	var decoded Error
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal error: %v", err)
	}

	if decoded.Code != ErrorCodeInvalidParams {
		t.Errorf("Error code = %d, want %d", decoded.Code, ErrorCodeInvalidParams)
	}

	if decoded.Message != "Invalid parameter" {
		t.Errorf("Error message = %s, want 'Invalid parameter'", decoded.Message)
	}

	if decoded.Data != "Additional details" {
		t.Errorf("Expected error data 'Additional details', got %v", decoded.Data)
	}
}

func TestError_JSONWithoutData(t *testing.T) {
	err := Error{
		Code:    ErrorCodeInternalError,
		Message: "Internal error",
	}

	data, errJSON := json.Marshal(err)
	if errJSON != nil {
		t.Fatalf("Failed to marshal error without data: %v", errJSON)
	}

	var decoded Error
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal error without data: %v", err)
	}

	if decoded.Code != ErrorCodeInternalError {
		t.Errorf("Error code = %d, want %d", decoded.Code, ErrorCodeInternalError)
	}

	if decoded.Data != nil {
		t.Error("Expected decoded error data to be nil")
	}
}

func TestError_InResponse(t *testing.T) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      42,
		Error: &Error{
			Code:    ErrorCodeToolNotFound,
			Message: "Tool not found",
			Data:    "my_tool",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal response with error: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal response with error: %v", err)
	}

	if decoded.Error == nil {
		t.Fatal("Expected error in response, got nil")
	}

	if decoded.Result != nil {
		t.Error("Expected no result in error response")
	}

	if decoded.Error.Code != ErrorCodeToolNotFound {
		t.Errorf("Error code = %d, want %d", decoded.Error.Code, ErrorCodeToolNotFound)
	}
}

func TestError_ErrorInterface(t *testing.T) {
	err := NewError(ErrorCodeInvalidParams, "Test error", nil)

	// Error struct doesn't implement error interface, but we can test string representation
	if err.Message != "Test error" {
		t.Errorf("Error message = %s, want 'Test error'", err.Message)
	}

	// Test with data
	err2 := NewError(ErrorCodeInternalError, "Internal error", map[string]string{
		"key": "value",
	})

	if err2.Data == nil {
		t.Fatal("Expected error data, got nil")
	}
}

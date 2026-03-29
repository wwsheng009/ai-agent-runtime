package protocol

import (
	"encoding/json"
	"testing"
)

func TestRequest_JSON(t *testing.T) {
	req := Request{
		JSONRPC: JSONRPCVersion,
		ID:      1,
		Method:  MethodListTools,
		Params: map[string]interface{}{
			"cursor": "test_cursor",
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("Failed to marshal request: %v", err)
	}

	var decoded Request
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal request: %v", err)
	}

	if decoded.JSONRPC != JSONRPCVersion {
		t.Errorf("Expected JSONRPC version %s, got %s", JSONRPCVersion, decoded.JSONRPC)
	}

	if decoded.Method != MethodListTools {
		t.Errorf("Expected method %s, got %s", MethodListTools, decoded.Method)
	}

	// JSON 解析数字为 float64，需要转换
	idFloat, ok := decoded.ID.(float64)
	if !ok {
		t.Fatalf("Expected ID to be float64, got %T", decoded.ID)
	}
	if idFloat != 1 {
		t.Errorf("Expected ID 1, got %v", idFloat)
	}
}

func TestResponse_JSON(t *testing.T) {
	result := struct {
		Message string `json:"message"`
	}{
		Message: "success",
	}

	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      1,
		Result:  result,
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Failed to marshal response: %v", err)
	}

	var decoded Response
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal response: %v", err)
	}

	if decoded.Error != nil {
		t.Errorf("Expected no error, got %v", decoded.Error)
	}

	if decoded.JSONRPC != JSONRPCVersion {
		t.Errorf("Expected JSONRPC version %s, got %s", JSONRPCVersion, decoded.JSONRPC)
	}
}

func TestResponse_WithError(t *testing.T) {
	resp := Response{
		JSONRPC: JSONRPCVersion,
		ID:      1,
		Error: &Error{
			Code:    -32600,
			Message: "Invalid Request",
			Data:    "details",
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
		t.Fatal("Expected error, got nil")
	}

	if decoded.Error.Code != -32600 {
		t.Errorf("Expected error code -32600, got %d", decoded.Error.Code)
	}

	if decoded.Error.Message != "Invalid Request" {
		t.Errorf("Expected error message 'Invalid Request', got %s", decoded.Error.Message)
	}
}

func TestNotification_JSON(t *testing.T) {
	notif := Notification{
		JSONRPC: JSONRPCVersion,
		Method:  MethodInitialized,
	}

	data, err := json.Marshal(notif)
	if err != nil {
		t.Fatalf("Failed to marshal notification: %v", err)
	}

	var decoded Notification
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal notification: %v", err)
	}

	if decoded.Method != MethodInitialized {
		t.Errorf("Expected method %s, got %s", MethodInitialized, decoded.Method)
	}
}

func TestInitializeParams_JSON(t *testing.T) {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		Capabilities: ClientCapabilities{
			Experimental: map[string]interface{}{
				"test": true,
			},
		},
		ClientInfo: ClientInfo{
			Name:    "aicli",
			Version: "1.0.0",
		},
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal initialize params: %v", err)
	}

	var decoded InitializeParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal initialize params: %v", err)
	}

	if decoded.ProtocolVersion != "2024-11-05" {
		t.Errorf("Expected protocol version 2024-11-05, got %s", decoded.ProtocolVersion)
	}

	if decoded.ClientInfo.Name != "aicli" {
		t.Errorf("Expected client name 'aicli', got %s", decoded.ClientInfo.Name)
	}
}

func TestInitializeResult_JSON(t *testing.T) {
	result := InitializeResult{
		ProtocolVersion: "2024-11-05",
		Capabilities: ServerCapabilities{
			Tools: &struct{}{},
		},
		ServerInfo: ServerInfo{
			Name:    "test-server",
			Version: "1.0.0",
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal initialize result: %v", err)
	}

	var decoded InitializeResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal initialize result: %v", err)
	}

	if decoded.ProtocolVersion != "2024-11-05" {
		t.Errorf("Expected protocol version 2024-11-05, got %s", decoded.ProtocolVersion)
	}

	if decoded.Capabilities.Tools == nil {
		t.Error("Expected Tools capability")
	}
}

func TestListToolsParams_JSON(t *testing.T) {
	cursor := "test_cursor"
	params := ListToolsParams{
		Cursor: &cursor,
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal list tools params: %v", err)
	}

	var decoded ListToolsParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal list tools params: %v", err)
	}

	if decoded.Cursor == nil {
		t.Fatal("Expected cursor, got nil")
	}

	if *decoded.Cursor != "test_cursor" {
		t.Errorf("Expected cursor 'test_cursor', got %s", *decoded.Cursor)
	}
}

func TestListToolsResult_JSON(t *testing.T) {
	tool := &Tool{
		Name:        "test_tool",
		Description: "A test tool",
	}

	result := ListToolsResult{
		Tools: []*Tool{tool},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal list tools result: %v", err)
	}

	var decoded ListToolsResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal list tools result: %v", err)
	}

	if len(decoded.Tools) != 1 {
		t.Fatalf("Expected 1 tool, got %d", len(decoded.Tools))
	}

	if decoded.Tools[0].Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", decoded.Tools[0].Name)
	}

	if decoded.Tools[0].Description != "A test tool" {
		t.Errorf("Expected tool description 'A test tool', got %s", decoded.Tools[0].Description)
	}
}

func TestCallToolParams_JSON(t *testing.T) {
	params := CallToolParams{
		Name: "test_tool",
		Arguments: map[string]interface{}{
			"param1": "value1",
			"param2": 123,
		},
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal call tool params: %v", err)
	}

	var decoded CallToolParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal call tool params: %v", err)
	}

	if decoded.Name != "test_tool" {
		t.Errorf("Expected tool name 'test_tool', got %s", decoded.Name)
	}

	if decoded.Arguments["param1"] != "value1" {
		t.Errorf("Expected param1 'value1', got %v", decoded.Arguments["param1"])
	}

	if decoded.Arguments["param2"].(float64) != 123 {
		t.Errorf("Expected param2 123, got %v", decoded.Arguments["param2"])
	}
}

func TestCallToolResult_JSON(t *testing.T) {
	result := CallToolResult{
		Content: []Content{
			{
				Type: "text",
				Text: "Test output",
			},
		},
		IsError: false,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal call tool result: %v", err)
	}

	var decoded CallToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal call tool result: %v", err)
	}

	if len(decoded.Content) != 1 {
		t.Fatalf("Expected 1 content item, got %d", len(decoded.Content))
	}

	if decoded.Content[0].Type != "text" {
		t.Errorf("Expected content type 'text', got %s", decoded.Content[0].Type)
	}

	if decoded.Content[0].Text != "Test output" {
		t.Errorf("Expected content text 'Test output', got %s", decoded.Content[0].Text)
	}

	if decoded.IsError {
		t.Error("Expected no error, got isError=true")
	}
}

func TestCallToolResult_WithMultipleContent(t *testing.T) {
	result := CallToolResult{
		Content: []Content{
			{
				Type: "text",
				Text: "First line",
			},
			{
				Type: "text",
				Text: "Second line",
			},
		},
		IsError: false,
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal call tool result with multiple content: %v", err)
	}

	var decoded CallToolResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal call tool result with multiple content: %v", err)
	}

	if len(decoded.Content) != 2 {
		t.Fatalf("Expected 2 content items, got %d", len(decoded.Content))
	}

	if decoded.Content[0].Text != "First line" {
		t.Errorf("Expected first content 'First line', got %s", decoded.Content[0].Text)
	}

	if decoded.Content[1].Text != "Second line" {
		t.Errorf("Expected second content 'Second line', got %s", decoded.Content[1].Text)
	}
}

func TestContent_JSON(t *testing.T) {
	tests := []struct {
		name string
		content Content
	}{
		{
			name: "Text content",
			content: Content{
				Type: "text",
				Text: "Hello, world!",
			},
		},
		{
			name: "Image content",
			content: Content{
				Type:     "image",
				Data:     "base64data",
				MIMEType: "image/png",
			},
		},
		{
			name: "Resource content",
			content: Content{
				Type: "resource",
				URI:  "file:///path/to/resource",
				Text: "Resource text",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.content)
			if err != nil {
				t.Fatalf("Failed to marshal content: %v", err)
			}

			var decoded Content
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal content: %v", err)
			}

			if decoded.Type != tt.content.Type {
				t.Errorf("Expected type %s, got %s", tt.content.Type, decoded.Type)
			}
		})
	}
}

func TestListResourcesParams_JSON(t *testing.T) {
	params := ListResourcesParams{
		// Cursor is optional, test without it
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal list resources params: %v", err)
	}

	var decoded ListResourcesParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal list resources params: %v", err)
	}

	if decoded.Cursor != nil {
		t.Error("Expected nil cursor, got value")
	}
}

func TestReadResourceParams_JSON(t *testing.T) {
	params := ReadResourceParams{
		URI: "file:///path/to/file.txt",
	}

	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Failed to marshal read resource params: %v", err)
	}

	var decoded ReadResourceParams
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal read resource params: %v", err)
	}

	if decoded.URI != "file:///path/to/file.txt" {
		t.Errorf("Expected URI 'file:///path/to/file.txt', got %s", decoded.URI)
	}
}

func TestReadResourceResult_JSON(t *testing.T) {
	result := ReadResourceResult{
		Contents: []ResourceContents{
			{
				URI:  "file:///path/to/file.txt",
				Text: "File contents",
			},
		},
	}

	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal read resource result: %v", err)
	}

	var decoded ReadResourceResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal read resource result: %v", err)
	}

	if len(decoded.Contents) != 1 {
		t.Fatalf("Expected 1 content item, got %d", len(decoded.Contents))
	}

	if decoded.Contents[0].URI != "file:///path/to/file.txt" {
		t.Errorf("Expected URI 'file:///path/to/file.txt', got %s", decoded.Contents[0].URI)
	}

	if decoded.Contents[0].Text != "File contents" {
		t.Errorf("Expected text 'File contents', got %s", decoded.Contents[0].Text)
	}
}

func TestResourceContents_JSON(t *testing.T) {
	tests := []struct {
		name string
		content ResourceContents
	}{
		{
			name: "Text resource",
			content: ResourceContents{
				URI:      "file:///path/to/file.txt",
				MIMEType: "text/plain",
				Text:     "File content",
			},
		},
		{
			name: "Blob resource",
			content: ResourceContents{
				URI:      "file:///path/to/file.bin",
				MIMEType: "application/octet-stream",
				Blob:     "YWJjZGVmZ2g=", // base64 encoded
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.content)
			if err != nil {
				t.Fatalf("Failed to marshal resource content: %v", err)
			}

			var decoded ResourceContents
			if err := json.Unmarshal(data, &decoded); err != nil {
				t.Fatalf("Failed to unmarshal resource content: %v", err)
			}

			if decoded.URI != tt.content.URI {
				t.Errorf("Expected URI %s, got %s", tt.content.URI, decoded.URI)
			}
		})
	}
}

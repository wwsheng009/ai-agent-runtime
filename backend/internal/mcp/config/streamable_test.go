package config

import (
	"testing"
	"time"
)

func TestValidate_StreamableHTTPTransport(t *testing.T) {
	loader := &Loader{}
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"chrome-mcp": {
			Name:     "chrome-mcp",
			Type:     "streamable",
			URL:      "http://127.0.0.1:12306/mcp",
			Timeout:  Duration{Duration: 30 * time.Second},
			MaxRetry: 3,
		},
		"alias-mcp": {
			Name:     "alias-mcp",
			Type:     "streamableHttp",
			URL:      "https://example.com/mcp",
			Timeout:  Duration{Duration: 30 * time.Second},
			MaxRetry: 3,
		},
	}}

	if err := loader.validate(cfg); err != nil {
		t.Fatalf("validate streamable config: %v", err)
	}
}

func TestValidate_StreamableRequiresURL(t *testing.T) {
	loader := &Loader{}
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"broken": {
			Name:     "broken",
			Type:     "streamable",
			Timeout:  Duration{Duration: 30 * time.Second},
			MaxRetry: 3,
		},
	}}

	if err := loader.validate(cfg); err == nil {
		t.Fatal("expected validation error for streamable MCP without url")
	}
}

func TestIsStreamableHTTPTransport(t *testing.T) {
	streamable := []string{"streamable", "streamableHttp", "streamable-http", "streamable_http", "http", " HTTP "}
	for _, value := range streamable {
		if !IsStreamableHTTPTransport(value) {
			t.Fatalf("expected %q to be a streamable HTTP transport", value)
		}
	}
	for _, value := range []string{"stdio", "sse", "websocket", "ws", ""} {
		if IsStreamableHTTPTransport(value) {
			t.Fatalf("expected %q NOT to be a streamable HTTP transport", value)
		}
	}
}

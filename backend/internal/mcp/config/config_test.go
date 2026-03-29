package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfig_SetDefaults(t *testing.T) {
	loader := &Loader{}
	config := &Config{}

	loader.setDefaults(config)

	// 检查全局默认值
	if config.Global.HealthCheckInterval.Duration != time.Minute {
		t.Errorf("Expected HealthCheckInterval to be 1m, got %v", config.Global.HealthCheckInterval)
	}

	if config.Global.ConnectTimeout.Duration != 10*time.Second {
		t.Errorf("Expected ConnectTimeout to be 10s, got %v", config.Global.ConnectTimeout)
	}
}

func TestConfig_SetDefaultsWithMCPServers(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Type: "stdio",
			},
			"test_mcp_2": {
				Type:    "sse",
				Timeout: Duration{Duration: 15 * time.Second},
			},
		},
	}

	loader.setDefaults(config)

	testMCP := config.MCPServers["test_mcp"]
	if testMCP.Name != "test_mcp" {
		t.Errorf("Expected name 'test_mcp', got %s", testMCP.Name)
	}

	if testMCP.Timeout.Duration != 30*time.Second {
		t.Errorf("Expected default timeout 30s, got %v", testMCP.Timeout)
	}

	if testMCP.MaxRetry != 3 {
		t.Errorf("Expected default maxRetry 3, got %d", testMCP.MaxRetry)
	}
	if testMCP.TrustLevel != MCPTrustLevelLocal {
		t.Errorf("Expected stdio MCP default trust level local, got %s", testMCP.TrustLevel)
	}

	testMCP2 := config.MCPServers["test_mcp_2"]
	if testMCP2.Timeout.Duration != 15*time.Second {
		t.Errorf("Expected timeout 15s, got %v", testMCP2.Timeout)
	}

	if testMCP2.MaxRetry != 3 {
		t.Errorf("Expected default maxRetry 3, got %d", testMCP2.MaxRetry)
	}
	if testMCP2.TrustLevel != MCPTrustLevelUntrusted {
		t.Errorf("Expected remote MCP default trust level untrusted_remote, got %s", testMCP2.TrustLevel)
	}
}

func TestConfig_Validate_Valid(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"stdio_mcp": {
				Name:     "stdio_mcp",
				Type:     "stdio",
				Command:  "node server.js",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
			"sse_mcp": {
				Name:     "sse_mcp",
				Type:     "sse",
				URL:      "http://localhost:3000/sse",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
			"ws_mcp": {
				Name:     "ws_mcp",
				Type:     "websocket",
				URL:      "ws://localhost:3000/ws",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}
}

func TestConfig_Validate_InvalidType(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"invalid_mcp": {
				Name:     "invalid_mcp",
				Type:     "invalid_type",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for invalid transport type, got nil")
	}
}

func TestConfig_Validate_StdioWithoutCommand(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"stdio_mcp": {
				Name:     "stdio_mcp",
				Type:     "stdio",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for stdio type without command, got nil")
	}
}

func TestConfig_Validate_SSEWithoutURL(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"sse_mcp": {
				Name:     "sse_mcp",
				Type:     "sse",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for sse type without URL, got nil")
	}
}

func TestConfig_Validate_WebsocketWithoutURL(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"ws_mcp": {
				Name:     "ws_mcp",
				Type:     "websocket",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for websocket type without URL, got nil")
	}
}

func TestConfig_Validate_InvalidTimeout(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Name:     "test_mcp",
				Type:     "stdio",
				Command:  "node server.js",
				Timeout:  Duration{Duration: 500 * time.Millisecond}, // 小于 1 秒
				MaxRetry: 3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for timeout less than 1 second, got nil")
	}
}

func TestConfig_Validate_InvalidMaxRetry(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Name:     "test_mcp",
				Type:     "stdio",
				Command:  "node server.js",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: -1, // 负数
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for negative maxRetry, got nil")
	}
}

func TestConfig_Validate_InvalidTrustLevel(t *testing.T) {
	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Name:       "test_mcp",
				Type:       "sse",
				URL:        "http://localhost:3000/sse",
				TrustLevel: MCPTrustLevel("unknown"),
				Timeout:    Duration{Duration: 30 * time.Second},
				MaxRetry:   3,
			},
		},
	}

	err := loader.validate(config)
	if err == nil {
		t.Error("Expected error for invalid trust level, got nil")
	}
}

func TestExpandEnvVars(t *testing.T) {
	// 设置测试环境变量
	os.Setenv("TEST_VAR", "test_value")
	defer os.Unsetenv("TEST_VAR")

	os.Setenv("TEST_URL", "http://example.com")
	defer os.Unsetenv("TEST_URL")

	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Name:     "test_mcp",
				Type:     "sse",
				URL:      "$TEST_URL/sse",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
				Env: map[string]string{
					"VAR1": "$TEST_VAR",
					"VAR2": "direct_value",
				},
			},
		},
	}

	err := loader.expandEnvVars(config)
	if err != nil {
		t.Fatalf("Failed to expand env vars: %v", err)
	}

	testMCP := config.MCPServers["test_mcp"]
	expectedURL := "http://example.com/sse"
	if testMCP.URL != expectedURL {
		t.Errorf("Expected URL %s, got %s", expectedURL, testMCP.URL)
	}

	expectedVAR1 := "test_value"
	if testMCP.Env["VAR1"] != expectedVAR1 {
		t.Errorf("Expected VAR1 %s, got %s", expectedVAR1, testMCP.Env["VAR1"])
	}

	expectedVAR2 := "direct_value"
	if testMCP.Env["VAR2"] != expectedVAR2 {
		t.Errorf("Expected VAR2 %s, got %s", expectedVAR2, testMCP.Env["VAR2"])
	}
}

func TestExpandEnvVars_WithCommand(t *testing.T) {
	// 设置测试环境变量
	os.Setenv("NODE_PATH", "/usr/local/bin")
	defer os.Unsetenv("NODE_PATH")

	loader := &Loader{}
	config := &Config{
		MCPServers: map[string]MCPConfig{
			"test_mcp": {
				Name:     "test_mcp",
				Type:     "stdio",
				Command:  "$NODE_PATH/node server.js",
				Timeout:  Duration{Duration: 30 * time.Second},
				MaxRetry: 3,
			},
		},
	}

	err := loader.expandEnvVars(config)
	if err != nil {
		t.Fatalf("Failed to expand env vars: %v", err)
	}

	testMCP := config.MCPServers["test_mcp"]
	expectedCommand := "/usr/local/bin/node server.js"
	if testMCP.Command != expectedCommand {
		t.Errorf("Expected command %s, got %s", expectedCommand, testMCP.Command)
	}
}

func TestLoadConfig(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_mcp.yaml")

	configContent := `
mcpServers:
  test_stdio:
    type: stdio
    command: "node server.js"
    enabled: true
    timeout: 30s

  test_sse:
    type: sse
    url: "http://localhost:3000/sse"
    enabled: true
    timeout: 30s

global:
  autoConnect: true
  healthCheckInterval: 1m
  connectTimeout: 10s
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	loader := NewLoader(configPath)
	config, err := loader.Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// 验证 MCP 配置数量
	if len(config.MCPServers) != 2 {
		t.Errorf("Expected 2 MCP servers, got %d", len(config.MCPServers))
	}

	// 验证 stdio 配置
	stdioMCP := config.MCPServers["test_stdio"]
	if stdioMCP.Type != "stdio" {
		t.Errorf("Expected type 'stdio', got %s", stdioMCP.Type)
	}
	if stdioMCP.Command != "node server.js" {
		t.Errorf("Expected command 'node server.js', got %s", stdioMCP.Command)
	}
	if !stdioMCP.Enabled {
		t.Error("Expected enabled to be true")
	}

	// 验证 sse 配置
	sseMCP := config.MCPServers["test_sse"]
	if sseMCP.Type != "sse" {
		t.Errorf("Expected type 'sse', got %s", sseMCP.Type)
	}
	if sseMCP.URL != "http://localhost:3000/sse" {
		t.Errorf("Expected URL 'http://localhost:3000/sse', got %s", sseMCP.URL)
	}

	// 验证全局配置
	if !config.Global.AutoConnect {
		t.Error("Expected global autoConnect to be true")
	}

	if config.Global.HealthCheckInterval.Duration != time.Minute {
		t.Errorf("Expected healthCheckInterval 1m, got %v", config.Global.HealthCheckInterval)
	}

	if config.Global.ConnectTimeout.Duration != 10*time.Second {
		t.Errorf("Expected connectTimeout 10s, got %v", config.Global.ConnectTimeout)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid_mcp.yaml")

	configContent := `
mcpServers:
  test:
    type: stdio
    command: node server.js
] invalid yaml
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	loader := NewLoader(configPath)
	_, err := loader.Load()
	if err == nil {
		t.Error("Expected error for invalid YAML, got nil")
	}
}

func TestLoadConfig_WithEnvVars(t *testing.T) {
	// 设置测试环境变量
	os.Setenv("MCP_URL", "http://test.example.com")
	os.Setenv("MCP_CMD", "/usr/bin/python3")
	defer os.Unsetenv("MCP_URL")
	defer os.Unsetenv("MCP_CMD")

	// 创建临时配置文件
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_mcp_env.yaml")

	configContent := `
mcpServers:
  test_mcp:
    type: sse
    url: "$MCP_URL/mcp"
    env:
      CMD: "$MCP_CMD"
    enabled: true
    timeout: 30s
`

	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("Failed to write test config: %v", err)
	}

	loader := NewLoader(configPath)
	config, err := loader.Load()
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	testMCP := config.MCPServers["test_mcp"]
	expectedURL := "http://test.example.com/mcp"
	if testMCP.URL != expectedURL {
		t.Errorf("Expected URL %s, got %s", expectedURL, testMCP.URL)
	}

	expectedCMD := "/usr/bin/python3"
	if testMCP.Env["CMD"] != expectedCMD {
		t.Errorf("Expected CMD %s, got %s", expectedCMD, testMCP.Env["CMD"])
	}
}

func TestLoader_GetConfig(t *testing.T) {
	loader := &Loader{
		config: &Config{
			Global: GlobalConfig{
				AutoConnect: true,
			},
		},
	}

	config := loader.GetConfig()
	if config == nil {
		t.Error("Expected config, got nil")
	}

	if !config.Global.AutoConnect {
		t.Error("Expected AutoConnect to be true")
	}
}

func TestLoader_GetConfig_NotLoaded(t *testing.T) {
	loader := &Loader{}

	config := loader.GetConfig()
	if config != nil {
		t.Error("Expected nil config when not loaded, got value")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	loader := NewLoader("/nonexistent/path/to/config.yaml")
	_, err := loader.Load()
	if err == nil {
		t.Error("Expected error for nonexistent file, got nil")
	}
}

func TestConfig_MCPStatus(t *testing.T) {
	status := MCPStatus{
		Name:          "test_mcp",
		Type:          "stdio",
		TrustLevel:    MCPTrustLevelLocal,
		ExecutionMode: "local_mcp",
		Enabled:       true,
		Connected:     true,
		ToolCount:     5,
		LastError:     "",
		LastConnect:   time.Now(),
		HealthCheck:   time.Now(),
	}

	if status.Name != "test_mcp" {
		t.Errorf("Expected name 'test_mcp', got %s", status.Name)
	}

	if !status.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if !status.Connected {
		t.Error("Expected Connected to be true")
	}

	if status.ToolCount != 5 {
		t.Errorf("Expected ToolCount 5, got %d", status.ToolCount)
	}
	if status.TrustLevel != MCPTrustLevelLocal {
		t.Errorf("Expected trust level local, got %s", status.TrustLevel)
	}
	if status.ExecutionMode != "local_mcp" {
		t.Errorf("Expected execution mode local_mcp, got %s", status.ExecutionMode)
	}
}

func TestConfig_ToolInfo(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"param1": map[string]interface{}{
				"type": "string",
			},
		},
	}

	info := ToolInfo{
		Name:        "test_tool",
		Description: "A test tool",
		MCPName:     "test_mcp",
		Enabled:     true,
		InputSchema: schema,
	}

	if info.Name != "test_tool" {
		t.Errorf("Expected name 'test_tool', got %s", info.Name)
	}

	if info.Description != "A test tool" {
		t.Errorf("Expected description 'A test tool', got %s", info.Description)
	}

	if info.MCPName != "test_mcp" {
		t.Errorf("Expected MCPName 'test_mcp', got %s", info.MCPName)
	}

	if !info.Enabled {
		t.Error("Expected Enabled to be true")
	}

	if info.InputSchema == nil {
		t.Error("Expected InputSchema to be set")
	}
}

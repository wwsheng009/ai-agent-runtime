package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMCPAuthConfigUnmarshalScalarAndMapping(t *testing.T) {
	var scalar struct {
		Server MCPConfig `yaml:"server"`
	}
	if err := yaml.Unmarshal([]byte("server:\n  type: streamable\n  url: https://example.com/mcp\n  auth: oauth\n"), &scalar); err != nil {
		t.Fatalf("scalar unmarshal: %v", err)
	}
	if !scalar.Server.IsOAuth() {
		t.Fatalf("auth: oauth 简写应被识别: %#v", scalar.Server.Auth)
	}

	var mapping struct {
		Server MCPConfig `yaml:"server"`
	}
	raw := `server:
  type: streamable
  url: https://example.com/mcp
  auth:
    type: oauth
    clientId: cid-1
    clientSecret: csecret-1
    scopes: [read, write]
    callbackPort: 3344
    authorizationServer: https://auth.example.com
    resource: https://example.com/mcp
`
	if err := yaml.Unmarshal([]byte(raw), &mapping); err != nil {
		t.Fatalf("mapping unmarshal: %v", err)
	}
	auth := mapping.Server.Auth
	if auth == nil || auth.Type != "oauth" || auth.ClientID != "cid-1" || auth.ClientSecret != "csecret-1" ||
		auth.CallbackPort != 3344 || auth.AuthorizationServer != "https://auth.example.com" ||
		auth.Resource != "https://example.com/mcp" || len(auth.Scopes) != 2 {
		t.Fatalf("结构化 auth 解析错误: %#v", auth)
	}
}

func TestLoaderValidatesAuthConfig(t *testing.T) {
	cases := []struct {
		name    string
		content string
		wantErr string
	}{
		{
			name: "oauth on stdio rejected",
			content: `mcpServers:
  local:
    type: stdio
    command: npx
    args: ["-y", "pkg"]
    auth: oauth
    timeout: 30s
`,
			wantErr: "streamable/sse",
		},
		{
			name: "unknown auth type rejected",
			content: `mcpServers:
  remote:
    type: streamable
    url: https://example.com/mcp
    auth:
      type: bearer
    timeout: 30s
`,
			wantErr: "不支持的 auth 类型",
		},
		{
			name: "callback port out of range",
			content: `mcpServers:
  remote:
    type: streamable
    url: https://example.com/mcp
    auth:
      type: oauth
      callbackPort: 70000
    timeout: 30s
`,
			wantErr: "callbackPort",
		},
		{
			name: "valid oauth on streamable",
			content: `mcpServers:
  remote:
    type: streamable
    url: https://example.com/mcp
    auth: oauth
    timeout: 30s
`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mcp.yaml")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatalf("write: %v", err)
			}
			_, err := NewLoader(path).Load()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("不应报错: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误 = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}

// auth 配置必须能随管理操作安全落盘（TokenSource 等内存字段不得写入）。
func TestMCPConfigAuthSerializationExcludesRuntimeFields(t *testing.T) {
	cfg := MCPConfig{
		Name: "remote",
		Type: "streamable",
		URL:  "https://example.com/mcp",
		Auth: &MCPAuthConfig{Type: "oauth", ClientID: "cid"},
	}
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	if !strings.Contains(text, "auth:") || !strings.Contains(text, "cid") {
		t.Fatalf("auth 配置应落盘:\n%s", text)
	}
	if strings.Contains(text, "tokensource") || strings.Contains(text, "envError") || strings.Contains(text, "enverror") {
		t.Fatalf("运行时字段不应落盘:\n%s", text)
	}
}

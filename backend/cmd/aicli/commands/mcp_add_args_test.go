package commands

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/auth"
)

func TestParseMCPAddArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		dash    int
		want    mcpAddArgs
		wantErr bool
	}{
		{
			name: "legacy name and url",
			args: []string{"docs", "https://example.com/mcp"},
			dash: -1,
			want: mcpAddArgs{Name: "docs", Target: "https://example.com/mcp"},
		},
		{
			name: "legacy name command and extra args",
			args: []string{"local", "npx", "-y", "pkg"},
			dash: -1,
			want: mcpAddArgs{Name: "local", Target: "npx", ExtraArgs: []string{"-y", "pkg"}},
		},
		{
			name: "dash form name before command after",
			args: []string{"chrome-devtools", "npx", "-y", "pkg"},
			dash: 1,
			want: mcpAddArgs{Name: "chrome-devtools", Target: "npx", ExtraArgs: []string{"-y", "pkg"}},
		},
		{
			name: "dash form with explicit target before dash",
			args: []string{"local", "node", "--flag"},
			dash: 2,
			want: mcpAddArgs{Name: "local", Target: "node", ExtraArgs: []string{"--flag"}},
		},
		{
			name:    "dash without name",
			args:    []string{"npx"},
			dash:    0,
			wantErr: true,
		},
		{
			name:    "dash without command",
			args:    []string{"docs"},
			dash:    1,
			wantErr: true,
		},
		{name: "empty args", args: nil, dash: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseMCPAddArgs(tt.args, tt.dash)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseMCPAddArgs: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestInferMCPTransport(t *testing.T) {
	tests := []struct {
		target  string
		command string
		want    string
	}{
		{target: "https://mcp.example.com/mcp", want: "streamable"},
		{target: "http://127.0.0.1:12306/mcp", want: "streamable"},
		{target: "wss://example.com/mcp", want: "websocket"},
		{target: "ws://example.com/mcp", want: "websocket"},
		{target: "npx", want: "stdio"},
		{target: "npx", command: "node", want: "stdio"},
		{target: "", command: "npx", want: "stdio"},
	}
	for _, tt := range tests {
		if got := inferMCPTransport(tt.target, tt.command); got != tt.want {
			t.Fatalf("inferMCPTransport(%q, %q) = %q, want %q", tt.target, tt.command, got, tt.want)
		}
	}
}

func TestParseMCPEnvOptions(t *testing.T) {
	env, err := parseMCPEnvOptions([]string{"FS_ROOT=/data", "MODE=ro", "EMPTY="})
	if err != nil {
		t.Fatalf("parseMCPEnvOptions: %v", err)
	}
	if env["FS_ROOT"] != "/data" || env["MODE"] != "ro" || env["EMPTY"] != "" {
		t.Fatalf("unexpected env: %#v", env)
	}
	if env, err := parseMCPEnvOptions(nil); err != nil || env != nil {
		t.Fatalf("nil input should return nil,nil; got %#v, %v", env, err)
	}
	for _, bad := range [][]string{{"NO_EQUALS"}, {"=value"}} {
		if _, err := parseMCPEnvOptions(bad); err == nil {
			t.Fatalf("expected error for %v", bad)
		}
	}
}

func TestBuildMCPAuthConfig(t *testing.T) {
	if cfg, err := buildMCPAuthConfig(""); err != nil || cfg != nil {
		t.Fatalf("empty auth should mean 不改变: cfg=%v err=%v", cfg, err)
	}
	cleared, err := buildMCPAuthConfig("none")
	if err != nil || cleared == nil || cleared.Type != "" {
		t.Fatalf("none should clear auth: cfg=%v err=%v", cleared, err)
	}

	prevID, prevSecret, prevScopes, prevPort, prevServer := addOAuthClientID, addOAuthClientSecret, addOAuthScopes, addOAuthCallbackPort, addOAuthAuthServer
	t.Cleanup(func() {
		addOAuthClientID, addOAuthClientSecret, addOAuthScopes, addOAuthCallbackPort, addOAuthAuthServer = prevID, prevSecret, prevScopes, prevPort, prevServer
	})
	addOAuthClientID = "client-1"
	addOAuthClientSecret = "secret-1"
	addOAuthScopes = []string{"read", "write"}
	addOAuthCallbackPort = 3344
	addOAuthAuthServer = "https://auth.example.com"

	oauth, err := buildMCPAuthConfig("OAuth")
	if err != nil {
		t.Fatalf("oauth should be accepted: %v", err)
	}
	if oauth.Type != "oauth" || oauth.ClientID != "client-1" || oauth.ClientSecret != "secret-1" ||
		oauth.CallbackPort != 3344 || oauth.AuthorizationServer != "https://auth.example.com" ||
		len(oauth.Scopes) != 2 {
		t.Fatalf("unexpected oauth config: %#v", oauth)
	}

	if _, err := buildMCPAuthConfig("bearer"); err == nil || !strings.Contains(err.Error(), "不支持的 --auth") {
		t.Fatalf("unsupported auth must fail with actionable error, got %v", err)
	}
}

func TestRunMCPAddCommandPersistsOAuthConfig(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")

	prev := mcpConfigFile
	mcpConfigFile = configPath
	t.Cleanup(func() { mcpConfigFile = prev })

	prevScopes, prevPort := addOAuthScopes, addOAuthCallbackPort
	t.Cleanup(func() { addOAuthScopes, addOAuthCallbackPort = prevScopes, prevPort })
	addOAuthScopes = []string{"read"}
	addOAuthCallbackPort = 0

	result, err := runMCPAddCommand(mcpAddCommandOptions{
		Name:      "remote",
		Target:    "http://127.0.0.1:9/mcp",
		Transport: "streamable",
		AuthType:  "oauth",
	})
	if err != nil {
		t.Fatalf("runMCPAddCommand: %v", err)
	}
	if result.Config == nil || result.Config.Auth == nil || result.Config.Auth.Type != "oauth" {
		t.Fatalf("oauth config not persisted: %#v", result.Config)
	}

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"auth:", "type: oauth", "scopes:", "- read"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}

	// --auth oauth 仅适用于远程传输：stdio 必须被拒绝且给出可行动错误。
	if _, err := runMCPAddCommand(mcpAddCommandOptions{
		Name: "local", Target: "npx", Transport: "stdio", AuthType: "oauth",
	}); err == nil || !strings.Contains(err.Error(), "streamable/sse") {
		t.Fatalf("stdio + oauth must fail with actionable error, got %v", err)
	}
}

func TestRunMCPAddCommandPersistsEnvAndInfersFlag(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")

	prev := mcpConfigFile
	mcpConfigFile = configPath
	t.Cleanup(func() { mcpConfigFile = prev })

	result, err := runMCPAddCommand(mcpAddCommandOptions{
		Name:              "docs",
		Target:            "http://127.0.0.1:9/mcp",
		Transport:         "streamable",
		TransportInferred: true,
		Headers:           []string{"Authorization: Bearer ${MCP_TEST_TOKEN}"},
		Env:               []string{"FS_ROOT=/data", "MODE=ro"},
	})
	if err != nil {
		t.Fatalf("runMCPAddCommand: %v", err)
	}
	if !result.TransportInferred {
		t.Fatal("TransportInferred should be preserved for the renderer")
	}
	cfg := result.Config
	if cfg == nil || cfg.Type != "streamable" {
		t.Fatalf("unexpected config: %#v", cfg)
	}
	if cfg.Env["FS_ROOT"] != "/data" || cfg.Env["MODE"] != "ro" {
		t.Fatalf("env not persisted: %#v", cfg.Env)
	}
	// header 仍以 ${VAR} 字面量落盘，展开发生在加载配置时。
	if cfg.Env["HEADER_Authorization"] != "Bearer ${MCP_TEST_TOKEN}" {
		t.Fatalf("header mapping = %q", cfg.Env["HEADER_Authorization"])
	}
	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	for _, want := range []string{"FS_ROOT: /data", "MODE: ro"} {
		if !strings.Contains(string(content), want) {
			t.Fatalf("config missing %q:\n%s", want, content)
		}
	}
}

func TestRunMCPAddCommandRejectsUnsupportedAuth(t *testing.T) {
	if _, err := runMCPAddCommand(mcpAddCommandOptions{
		Name: "docs", Target: "npx", Transport: "stdio", AuthType: "oauth",
	}); err == nil {
		t.Fatal("expected --auth oauth to fail fast")
	}
}

func TestAuthListPayloadOmitsSecrets(t *testing.T) {
	payload := authListPayload([]*auth.Token{
		{
			ServerName:   "remote",
			ServerURL:    "https://example.com/mcp",
			AccessToken:  "super-secret-access",
			RefreshToken: "super-secret-refresh",
			ClientID:     "cid",
			Scope:        "read",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
		nil,
	})
	if len(payload) != 1 {
		t.Fatalf("payload 长度 = %d, want 1（nil 应被跳过）", len(payload))
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	text := string(raw)
	for _, secret := range []string{"super-secret-access", "super-secret-refresh"} {
		if strings.Contains(text, secret) {
			t.Fatalf("令牌明文泄漏到 --list 输出: %s", text)
		}
	}
	if payload[0]["hasRefreshToken"] != true || payload[0]["clientId"] != "cid" {
		t.Fatalf("元数据缺失: %#v", payload[0])
	}
}

func TestLoadMCPAuthServer(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "mcp.yaml")
	content := `mcpServers:
  remote:
    type: streamable
    url: https://example.com/mcp
    auth: oauth
    enabled: true
  local:
    type: stdio
    command: npx
    args: ["-y", "pkg"]
    enabled: true
`
	if err := os.WriteFile(configPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	prev := mcpConfigFile
	mcpConfigFile = configPath
	t.Cleanup(func() { mcpConfigFile = prev })

	server, path, err := loadMCPAuthServer("remote", true)
	if err != nil {
		t.Fatalf("loadMCPAuthServer: %v", err)
	}
	if server.URL != "https://example.com/mcp" || !server.IsOAuth() || path != configPath {
		t.Fatalf("server = %#v, path = %q", server, path)
	}

	if _, _, err := loadMCPAuthServer("missing", true); err == nil || !strings.Contains(err.Error(), "未找到") {
		t.Fatalf("不存在的名称应报错，got %v", err)
	}
	if _, _, err := loadMCPAuthServer("local", true); err == nil || !strings.Contains(err.Error(), "auth: oauth") {
		t.Fatalf("未启用 oauth 的 server 应给出可行动错误，got %v", err)
	}
	// requireOAuth=false 时只做存在性检查。
	if _, _, err := loadMCPAuthServer("local", false); err != nil {
		t.Fatalf("非 oauth 校验应通过: %v", err)
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandEnvBracedAndLegacy(t *testing.T) {
	t.Setenv("MCP_EXPAND_TOKEN", "secret-token")
	t.Setenv("MCP_EXPAND_EMPTY", "")
	t.Setenv("MCP_EXPAND_HOST", "api")
	t.Setenv("MCP_EXPAND_PATH", "mcp")
	t.Setenv("MCP_EXPAND_BIN", "/usr/local/bin/server")
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {
			Type:    "streamable",
			URL:     "https://$MCP_EXPAND_HOST.example/${MCP_EXPAND_PATH}",
			Command: "$MCP_EXPAND_BIN --serve",
			Env: map[string]string{
				"TOKEN":     "${MCP_EXPAND_TOKEN}",
				"WITH_DEF":  "${MCP_EXPAND_MISSING:-fallback}",
				"EMPTY_DEF": "${MCP_EXPAND_EMPTY:-fallback}",
			},
		},
	}}

	ExpandEnv(cfg)
	got := cfg.MCPServers["docs"]
	if got.EnvError != "" {
		t.Fatalf("unexpected EnvError: %s", got.EnvError)
	}
	if got.URL != "https://api.example/mcp" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Command != "/usr/local/bin/server --serve" {
		t.Fatalf("Command = %q", got.Command)
	}
	if got.Env["TOKEN"] != "secret-token" {
		t.Fatalf("Env[TOKEN] = %q", got.Env["TOKEN"])
	}
	if got.Env["WITH_DEF"] != "fallback" || got.Env["EMPTY_DEF"] != "fallback" {
		t.Fatalf("default expansion failed: %#v", got.Env)
	}
}

func TestExpandEnvLegacyUnsetIsLenient(t *testing.T) {
	// 历史 `$VAR` 语法保持 os.ExpandEnv 行为：未设置展开为空串且不记录错误。
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"legacy": {Type: "sse", URL: "https://example.com/$MCP_EXPAND_NOT_SET/sse"},
	}}
	ExpandEnv(cfg)
	got := cfg.MCPServers["legacy"]
	if got.URL != "https://example.com//sse" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.EnvError != "" {
		t.Fatalf("legacy unset must not record EnvError, got %q", got.EnvError)
	}
}

func TestExpandEnvMissingBracedVarRecordsPerServerError(t *testing.T) {
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {
			Type:    "streamable",
			URL:     "https://example.com/mcp",
			Headers: map[string]string{"Authorization": "Bearer ${MCP_EXPAND_ABSENT_TOKEN}"},
		},
		"ok": {Type: "streamable", URL: "https://example.com/mcp"},
	}}
	ExpandEnv(cfg)

	docs := cfg.MCPServers["docs"]
	if !strings.Contains(docs.EnvError, "MCP_EXPAND_ABSENT_TOKEN") {
		t.Fatalf("EnvError must name the variable: %q", docs.EnvError)
	}
	if !strings.Contains(docs.EnvError, ":-") {
		t.Fatalf("EnvError should hint at ${VAR:-default}: %q", docs.EnvError)
	}
	if cfg.MCPServers["ok"].EnvError != "" {
		t.Fatalf("unrelated server must stay clean: %q", cfg.MCPServers["ok"].EnvError)
	}
}

func TestExpandEnvClearsPreviousErrors(t *testing.T) {
	t.Setenv("MCP_EXPAND_LATE", "now-set")
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {Type: "streamable", URL: "https://example.com/${MCP_EXPAND_LATE}", EnvError: "stale"},
	}}
	ExpandEnv(cfg)
	got := cfg.MCPServers["docs"]
	if got.EnvError != "" {
		t.Fatalf("EnvError must be recomputed each load, got %q", got.EnvError)
	}
	if got.URL != "https://example.com/now-set" {
		t.Fatalf("URL = %q", got.URL)
	}
}

func TestExpandEnvHeadersAndArgsOnlyUseExplicitSyntax(t *testing.T) {
	t.Setenv("MCP_EXPAND_HDR", "hdr-value")
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {
			Type:    "stdio",
			Command: "npx",
			Args:    []string{"-y", "pkg", "$1", "${MCP_EXPAND_ARG:-arg-default}"},
			Headers: map[string]string{
				"Authorization": "Bearer ${MCP_EXPAND_HDR}",
				"X-Literal":     "cost$5",
			},
		},
	}}
	ExpandEnv(cfg)
	got := cfg.MCPServers["docs"]
	if got.Headers["Authorization"] != "Bearer hdr-value" {
		t.Fatalf("Authorization = %q", got.Headers["Authorization"])
	}
	if got.Headers["X-Literal"] != "cost$5" {
		t.Fatalf("bare $ must stay literal in headers, got %q", got.Headers["X-Literal"])
	}
	if got.Args[2] != "$1" {
		t.Fatalf("bare $1 must stay literal in args, got %q", got.Args[2])
	}
	if got.Args[3] != "arg-default" {
		t.Fatalf("braced default in args = %q", got.Args[3])
	}
}

func TestExpandEnvEscapeProducesLiteral(t *testing.T) {
	t.Setenv("MCP_EXPAND_ESC", "should-not-appear")
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {
			Type: "streamable",
			URL:  "https://example.com/$${MCP_EXPAND_ESC}/mcp",
			Env:  map[string]string{"LITERAL": "$${MCP_EXPAND_ESC}"},
		},
	}}
	ExpandEnv(cfg)
	got := cfg.MCPServers["docs"]
	if got.URL != "https://example.com/${MCP_EXPAND_ESC}/mcp" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.Env["LITERAL"] != "${MCP_EXPAND_ESC}" {
		t.Fatalf("Env[LITERAL] = %q", got.Env["LITERAL"])
	}
}

func TestExpandEnvMalformedBracedStaysLiteral(t *testing.T) {
	cfg := &Config{MCPServers: map[string]MCPConfig{
		"docs": {Type: "streamable", URL: "https://example.com/${1bad}/mcp"},
	}}
	ExpandEnv(cfg)
	got := cfg.MCPServers["docs"]
	if got.URL != "https://example.com/${1bad}/mcp" {
		t.Fatalf("URL = %q", got.URL)
	}
	if got.EnvError != "" {
		t.Fatalf("malformed braces must not record EnvError, got %q", got.EnvError)
	}
}

func TestLoaderKeepsRawEnvRefsUntilRuntimeExpansion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.yaml")
	content := `mcpServers:
  docs:
    type: streamable
    url: https://example.com/mcp
    headers:
      Authorization: "Bearer ${MCP_LOADER_ABSENT}"
    enabled: true
  ok:
    type: streamable
    url: https://example.com/mcp
    enabled: true
`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := NewLoader(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 加载期保持原始字面量：管理操作（add/enable/remove）读改写回时不能丢引用，
	// 更不能把展开后的真实值写进配置文件。
	if got := cfg.MCPServers["docs"].Headers["Authorization"]; got != "Bearer ${MCP_LOADER_ABSENT}" {
		t.Fatalf("load must keep raw literal, got %q", got)
	}
	if cfg.MCPServers["docs"].EnvError != "" {
		t.Fatalf("load must not flag env errors, got %q", cfg.MCPServers["docs"].EnvError)
	}

	// 运行时入口显式展开后才记录缺失项。
	ExpandEnv(cfg)
	if !strings.Contains(cfg.MCPServers["docs"].EnvError, "MCP_LOADER_ABSENT") {
		t.Fatalf("EnvError = %q", cfg.MCPServers["docs"].EnvError)
	}
	if cfg.MCPServers["ok"].EnvError != "" {
		t.Fatalf("unrelated server must stay clean: %q", cfg.MCPServers["ok"].EnvError)
	}
}

func TestCloneWithDefaultsExpandsInMemoryConfig(t *testing.T) {
	t.Setenv("MCP_CLONE_TOKEN", "clone-token")
	src := &Config{MCPServers: map[string]MCPConfig{
		"docs": {
			Type:    "streamable",
			URL:     "https://example.com/${MCP_CLONE_PATH:-mcp}",
			Enabled: true,
			Env:     map[string]string{"TOKEN": "${MCP_CLONE_TOKEN}"},
		},
	}}
	out := CloneWithDefaults(src)
	if out.MCPServers["docs"].Env["TOKEN"] != "clone-token" {
		t.Fatalf("clone did not expand env: %#v", out.MCPServers["docs"].Env)
	}
	if out.MCPServers["docs"].URL != "https://example.com/mcp" {
		t.Fatalf("URL = %q", out.MCPServers["docs"].URL)
	}
	// 源配置不被修改（CloneWithDefaults 的既有契约）。
	if src.MCPServers["docs"].Env["TOKEN"] != "${MCP_CLONE_TOKEN}" {
		t.Fatalf("source must stay untouched: %#v", src.MCPServers["docs"].Env)
	}
}

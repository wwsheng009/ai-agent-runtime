package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// mcp.json 与 mcp.yaml 同层自动发现：两者都参与合并，同名 server 以 mcp.yaml 为准。
func TestLoadEffectiveFromMergesJSONSiblingWithYAMLOverride(t *testing.T) {
	_ = isolateLayeredHome(t)
	base := t.TempDir()

	jsonPath := writeConfigFile(t, base, ".aicli/mcp.json", `{
  "mcpServers": {
    "json-only-probe": {"command": "json-only-cmd"},
    "shared-probe": {"command": "json-shared-cmd"}
  }
}`)
	yamlPath := writeConfigFile(t, base, ".aicli/mcp.yaml", `mcpServers:
  shared-probe:
    type: stdio
    command: yaml-shared-cmd
  yaml-only-probe:
    type: stdio
    command: yaml-only-cmd
`)

	result, err := LoadEffectiveFrom(base, "")
	if err != nil {
		t.Fatalf("LoadEffectiveFrom: %v", err)
	}
	if got := result.Config.MCPServers["shared-probe"].Command; got != "yaml-shared-cmd" {
		t.Fatalf("同名 server 应以 mcp.yaml 为准, got %q", got)
	}
	if got := result.Config.MCPServers["json-only-probe"].Command; got != "json-only-cmd" {
		t.Fatalf("json 独有 server 应生效, got %q", got)
	}
	if got := result.Config.MCPServers["yaml-only-probe"].Command; got != "yaml-only-cmd" {
		t.Fatalf("yaml 独有 server 应生效, got %q", got)
	}
	if filepath.Clean(result.Primary.Path) != filepath.Clean(yamlPath) || result.Primary.Source != "project" {
		t.Fatalf("primary = %#v, want project/%s", result.Primary, yamlPath)
	}

	origin, ok := result.Origins["shared-probe"]
	if !ok || filepath.Clean(origin.Path) != filepath.Clean(yamlPath) || origin.Source != "project" {
		t.Fatalf("shared-probe origin = %#v, want project/%s", origin, yamlPath)
	}
	if len(origin.Shadowed) != 1 || filepath.Clean(origin.Shadowed[0].Path) != filepath.Clean(jsonPath) {
		t.Fatalf("shared-probe 应记录被覆盖的 json 来源, got %#v", origin.Shadowed)
	}

	jsonOrigin, ok := result.Origins["json-only-probe"]
	if !ok || filepath.Clean(jsonOrigin.Path) != filepath.Clean(jsonPath) || jsonOrigin.Source != "project" {
		t.Fatalf("json-only-probe origin = %#v, want project/%s", jsonOrigin, jsonPath)
	}
}

// 只有 mcp.json 时按同一发现链自动生效（用户给的 Claude 风格示例）。
func TestLoadEffectiveFromDiscoversJSONOnlyLayer(t *testing.T) {
	_ = isolateLayeredHome(t)
	base := t.TempDir()
	jsonPath := writeConfigFile(t, base, ".aicli/mcp.json",
		`{"mcpServers":{"json-only-probe":{"command":"npx","args":["-y","@example/mcp-server"]}}}`)

	result, err := LoadEffectiveFrom(base, "")
	if err != nil {
		t.Fatalf("LoadEffectiveFrom: %v", err)
	}
	server, ok := result.Config.MCPServers["json-only-probe"]
	if !ok {
		t.Fatalf("json-only server 未加载: %#v", result.Config.MCPServers)
	}
	if server.Type != "stdio" {
		t.Fatalf("缺省类型应补齐为 stdio, got %q", server.Type)
	}
	if server.Command != "npx" || len(server.Args) != 2 {
		t.Fatalf("command/args 解析错误: %#v", server)
	}
	if filepath.Clean(result.Primary.Path) != filepath.Clean(jsonPath) || result.Primary.Source != "project" {
		t.Fatalf("primary = %#v, want project/%s", result.Primary, jsonPath)
	}
}

// 同层 mcp.json 损坏时按「低优先级文件损坏 → 跳过 + 告警」处理，yaml 仍生效。
func TestLoadEffectiveFromMalformedJSONSiblingIsSkippedWithWarning(t *testing.T) {
	_ = isolateLayeredHome(t)
	base := t.TempDir()
	writeConfigFile(t, base, ".aicli/mcp.json", `{"mcpServers": {`)
	writeConfigFile(t, base, ".aicli/mcp.yaml", `mcpServers:
  yaml-survivor-probe:
    type: stdio
    command: yaml-survivor-cmd
`)

	result, err := LoadEffectiveFrom(base, "")
	if err != nil {
		t.Fatalf("LoadEffectiveFrom: %v", err)
	}
	if _, ok := result.Config.MCPServers["yaml-survivor-probe"]; !ok {
		t.Fatalf("yaml 层应仍然生效: %#v", result.Config.MCPServers)
	}
	found := false
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "mcp.json") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("损坏的低优先级 mcp.json 应记入告警: %#v", result.Warnings)
	}
}

// 只有一份损坏的 mcp.json 时它就是 primary：与 yaml 单文件语义一致，直接报错。
func TestLoadEffectiveFromMalformedJSONOnlyFails(t *testing.T) {
	_ = isolateLayeredHome(t)
	base := t.TempDir()
	writeConfigFile(t, base, ".aicli/mcp.json", `{"mcpServers": {`)

	if _, err := LoadEffectiveFrom(base, ""); err == nil {
		t.Fatal("唯一的 mcp.json 损坏时应直接报错")
	}
}

// Claude/Cursor 风格 mcpServers JSON（含 type: http 别名）按原生 schema 直接可用。
func TestLoadConfigFileAcceptsClaudeStyleJSON(t *testing.T) {
	dir := t.TempDir()
	path := writeConfigFile(t, dir, "mcp.json", `{
  "mcpServers": {
    "local-fs": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-filesystem", "/data"]},
    "remote-http": {"type": "http", "url": "https://example.com/mcp"}
  }
}`)

	cfg, err := NewLoader(path).Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.MCPServers["local-fs"].Type; got != "stdio" {
		t.Fatalf("缺省类型应为 stdio, got %q", got)
	}
	// http 是 streamable 的别名：配置层保留原值，传输层归一化，校验必须通过。
	if got := cfg.MCPServers["remote-http"].Type; got != "http" {
		t.Fatalf("type 原值应保留, got %q", got)
	}
}

// 外部工具容器键（VS Code 的 servers / Claude ~/.claude.json 的 projects）
// 不能静默解析成 0 个 server，必须给出可行动错误。
func TestLoadConfigFileRejectsExternalJSONContainers(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
	}{
		{"servers.json", `{"servers":{"vscode-probe":{"command":"npx"}}}`},
		{"projects.json", `{"projects":{"E:\\demo":{"mcpServers":{}}}}`},
	}
	for _, tc := range cases {
		path := writeConfigFile(t, dir, tc.name, tc.body)
		_, err := NewLoader(path).Load()
		if err == nil || !strings.Contains(err.Error(), "aicli mcp import --from json") {
			t.Fatalf("%s: 外部容器键应报错并指向导入命令, got %v", tc.name, err)
		}
	}
}

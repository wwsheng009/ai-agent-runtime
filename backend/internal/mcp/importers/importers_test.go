package importers

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func serverByName(t *testing.T, result Result, name string) Server {
	t.Helper()
	for _, server := range result.Servers {
		if server.Name == name {
			return server
		}
	}
	t.Fatalf("result %s 缺少 server %q：%#v", result.Vendor, name, result.Servers)
	return Server{}
}

func TestImportClaudeMapsTransportsAndSecrets(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{
  "mcpServers": {
    "files": {"type": "stdio", "command": "npx", "args": ["-y", "server-filesystem", "/data"],
              "env": {"FS_ROOT": "/data"}},
    "remote": {"type": "http", "url": "https://mcp.example.com/mcp",
               "headers": {"Authorization": "Bearer ${TEAM_TOKEN}"}},
    "legacy-sse": {"type": "sse", "url": "https://legacy.example.com/sse"},
    "odd": {"type": "http", "url": "https://odd.example.com/mcp", "mysteryField": true}
  },
  "projects": {
    "/other/project": {"mcpServers": {"other-only": {"command": "other-cmd"}}}
  }
}`)

	results, err := Import(VendorClaude, Options{Home: home, Root: root})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %#v", results)
	}
	result := results[0]
	if len(result.Servers) != 5 {
		t.Fatalf("servers = %#v", result.Servers)
	}

	remote := serverByName(t, result, "remote")
	if remote.Config.Type != "streamable" {
		t.Fatalf("http 应映射为 streamable: %#v", remote.Config.Type)
	}
	if remote.OriginalType != "http" {
		t.Fatalf("应保留原始类型: %#v", remote.OriginalType)
	}
	if remote.Config.Enabled != true {
		t.Fatalf("默认应启用: %#v", remote.Config)
	}
	if got := remote.Config.Headers["Authorization"]; got != "Bearer ${TEAM_TOKEN}" {
		t.Fatalf("header 引用应原样保留: %#v", remote.Config.Headers)
	}

	if got := serverByName(t, result, "legacy-sse").Config.Type; got != "sse" {
		t.Fatalf("sse 映射错误: %q", got)
	}
	files := serverByName(t, result, "files")
	if files.Config.Command != "npx" || len(files.Config.Args) != 3 || files.Config.Env["FS_ROOT"] != "/data" {
		t.Fatalf("stdio 映射错误: %#v", files.Config)
	}
	if got := serverByName(t, result, "other-only").Config.Type; got != "stdio" {
		t.Fatalf("projects 段回退推断应为 stdio: %q", got)
	}

	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "mysteryField") {
		t.Fatalf("未知字段应产生告警:\n%s", warnings)
	}
}

// 项目级来源覆盖用户级同名 server，并记录覆盖关系。
func TestImportPrefersProjectLayerOverUserLayer(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	writeFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {
	  "shared": {"url": "https://user.example.com/mcp"},
	  "user-only": {"url": "https://user-only.example.com/mcp"}
	}}`)
	writeFile(t, filepath.Join(root, ".cursor", "mcp.json"), `{"mcpServers": {
	  "shared": {"url": "https://project.example.com/mcp"}
	}}`)

	results, err := Import(VendorCursor, Options{Home: home, Root: root})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	result := results[0]
	shared := serverByName(t, result, "shared")
	if shared.Config.URL != "https://project.example.com/mcp" {
		t.Fatalf("项目级应覆盖用户级: %#v", shared)
	}
	if filepath.Clean(shared.Source) != filepath.Clean(filepath.Join(root, ".cursor", "mcp.json")) {
		t.Fatalf("来源应指向项目文件: %#v", shared.Source)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "被更高优先级来源覆盖") {
		t.Fatalf("应记录覆盖告警: %#v", result.Warnings)
	}
	if len(result.Scanned) != 2 || !result.Scanned[0].Exists || !result.Scanned[1].Exists {
		t.Fatalf("scanned = %#v", result.Scanned)
	}
}

func TestImportCodexTOML(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), `model = "gpt-5"

[mcp_servers.context7]
command = "npx"
args = [
  "-y",
  "@upstash/context7-mcp",
]
startup_timeout_ms = 20000
env = { CONTEXT7_TOKEN = "${CONTEXT7_TOKEN}" }

[mcp_servers.remote]
url = "https://mcp.example.com/mcp"
http_headers = { "X-Api-Key" = "plain-key" }

[mcp_servers."odd.name"]
command = "odd-cmd"
enabled = false

[mcp_servers.compat]
command = "compat-cmd"
disabled = true

[other_section]
foo = "bar"
`)

	results, err := Import(VendorCodex, Options{Home: home, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	result := results[0]
	if len(result.Servers) != 4 {
		t.Fatalf("servers = %#v", result.Servers)
	}

	context7 := serverByName(t, result, "context7")
	if context7.Config.Type != "stdio" || context7.Config.Command != "npx" {
		t.Fatalf("context7 映射错误: %#v", context7.Config)
	}
	if len(context7.Config.Args) != 2 {
		t.Fatalf("跨行数组应解析: %#v", context7.Config.Args)
	}
	if context7.Config.Timeout.Duration != 20*time.Second {
		t.Fatalf("startup_timeout_ms 应换算为 20s: %v", context7.Config.Timeout.Duration)
	}
	if context7.Config.Env["CONTEXT7_TOKEN"] != "${CONTEXT7_TOKEN}" {
		t.Fatalf("env 引用应保留: %#v", context7.Config.Env)
	}

	remote := serverByName(t, result, "remote")
	if remote.Config.Type != "streamable" || remote.Config.Headers["X-Api-Key"] != "plain-key" {
		t.Fatalf("remote 映射错误: %#v", remote.Config)
	}

	odd := serverByName(t, result, "odd.name")
	if odd.Config.Enabled {
		t.Fatalf("enabled=false 应映射为禁用: %#v", odd.Config)
	}
	// `disabled = true` 是 MCP 官方兼容写法：保留该形态（IsEnabled 同样为 false）。
	compat := serverByName(t, result, "compat")
	if compat.Config.Enabled || !compat.Config.Disabled {
		t.Fatalf("disabled=true 应保留兼容形态: %#v", compat.Config)
	}
}

func TestImportCodexTOMLReportsUnsupportedConstructs(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".codex", "config.toml"), `[[mcp_servers.list]]
command = "x"

[mcp_servers.deep.nested]
command = "y"

[mcp_servers.ok]
command = "ok-cmd"
`)

	result := mustImport(t, VendorCodex, home)[0]
	warnings := strings.Join(result.Warnings, "\n")
	if !strings.Contains(warnings, "不支持数组表") || !strings.Contains(warnings, "不支持嵌套子表") {
		t.Fatalf("应报告不支持的构造:\n%s", warnings)
	}
	if server := serverByName(t, result, "ok"); server.Config.Command != "ok-cmd" {
		t.Fatalf("合法表仍应导入: %#v", server)
	}
}

func TestImportUnknownVendorAndMissingFiles(t *testing.T) {
	if _, err := Import("nope", Options{Home: t.TempDir(), Root: t.TempDir()}); err == nil ||
		!strings.Contains(err.Error(), "claude") {
		t.Fatalf("非法来源应给出可选值: %v", err)
	}

	// 文件不存在不报错：Scanned 全部 Exists=false，Servers 为空。
	results, err := Import(VendorGemini, Options{Home: t.TempDir(), Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(results[0].Servers) != 0 {
		t.Fatalf("缺失文件不应产出 server: %#v", results[0])
	}
	for _, scanned := range results[0].Scanned {
		if scanned.Exists {
			t.Fatalf("不应存在: %#v", scanned)
		}
	}
}

func TestImportAllAndNameFilter(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), `{"mcpServers": {"keep": {"url": "https://a.example.com/mcp"}, "drop": {"url": "https://b.example.com/mcp"}}}`)
	writeFile(t, filepath.Join(root, ".cursor", "mcp.json"), `{"mcpServers": {"cursor-one": {"command": "c-cmd"}}}`)

	all, err := Import(VendorAll, Options{Home: home, Root: root})
	if err != nil {
		t.Fatalf("Import all: %v", err)
	}
	if len(all) != len(SupportedVendors()) {
		t.Fatalf("应扫描全部来源: %#v", all)
	}

	filtered, err := Import(VendorClaude, Options{Home: home, Root: root, Names: []string{"KEEP"}})
	if err != nil {
		t.Fatalf("Import filtered: %v", err)
	}
	if len(filtered[0].Servers) != 1 || filtered[0].Servers[0].Name != "keep" {
		t.Fatalf("名称过滤（大小写不敏感）失败: %#v", filtered[0].Servers)
	}
}

// 坏文件只影响自己：其它来源照常导入，错误记在该文件上。
func TestImportIsolatesBrokenFile(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, ".claude.json"), "{ not json")
	writeFile(t, filepath.Join(home, ".cursor", "mcp.json"), `{"mcpServers": {"ok": {"command": "ok-cmd"}}}`)

	results, err := Import(VendorAll, Options{Home: home, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	byVendor := map[string]Result{}
	for _, result := range results {
		byVendor[result.Vendor] = result
	}
	claude := byVendor[VendorClaude]
	if len(claude.Scanned) == 0 || claude.Scanned[0].Err == "" {
		t.Fatalf("坏文件应记录错误: %#v", claude.Scanned)
	}
	if len(byVendor[VendorCursor].Servers) != 1 {
		t.Fatalf("其它来源不应受影响: %#v", byVendor[VendorCursor])
	}
}

func mustImport(t *testing.T, vendor, home string) []Result {
	t.Helper()
	results, err := Import(vendor, Options{Home: home, Root: t.TempDir()})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return results
}

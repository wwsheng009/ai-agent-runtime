package commands

import (
	"path/filepath"
	"strings"
	"testing"
)

// chat_mcp_add_json_test.go 覆盖 chat 内 `/mcp add-json`：
// 内联 JSON（含空格与 `&`）、@文件、多 server 容器提示、未映射字段告警、错误路径。

func TestChatMCPAddJSONInlineParsesVerbatim(t *testing.T) {
	service := newFakeChatMCPService()
	mutations := 0

	// JSON 里带空格与 `&`：命令 tokenizer 会拆散，必须按原文还原。
	command := `/mcp add-json notion {"type":"http","url":"https://mcp.notion.com/mcp",` +
		`"headers":{"X-Team":"a&b"},"env":{"NOTE":"hello world"},"timeoutSeconds":45}`
	text := chatMCPCommandTextWithService(command, service, func() { mutations++ })

	for _, want := range []string{
		`✓ 已新增 MCP "notion"（streamable）`,
		service.ConfigPath(),
		"用 /mcp status notion 查看连接状态与工具数。",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("输出缺少 %q:\n%s", want, text)
		}
	}
	if mutations != 1 {
		t.Fatalf("成功后应回调 onMutate: %d", mutations)
	}
	if len(service.added) != 1 {
		t.Fatalf("应调用一次 Add: %#v", service.added)
	}
	request := service.added[0]
	if request.Name != "notion" || request.URL != "https://mcp.notion.com/mcp" || request.Type != "streamable" {
		t.Fatalf("请求不符: %#v", request)
	}
	if request.Headers["X-Team"] != "a&b" || request.Env["NOTE"] != "hello world" {
		t.Fatalf("headers/env 应完整保留: %#v %#v", request.Headers, request.Env)
	}
	if request.TimeoutSeconds == nil || *request.TimeoutSeconds != 45 {
		t.Fatalf("timeoutSeconds 未生效: %#v", request.TimeoutSeconds)
	}
}

func TestChatMCPAddJSONFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "local-fs.json")
	writeMCPImportFile(t, path, `{"command":"npx","args":["-y","@modelcontextprotocol/server-filesystem","/data"]}`)

	service := newFakeChatMCPService()
	text := chatMCPCommandTextWithService("/mcp add-json local-fs @"+path, service, nil)
	if !strings.Contains(text, `✓ 已新增 MCP "local-fs"（stdio）`) {
		t.Fatalf("输出不符:\n%s", text)
	}
	if len(service.added) != 1 || service.added[0].Command != "npx" || len(service.added[0].Args) != 3 {
		t.Fatalf("请求不符: %#v", service.added)
	}
}

// 多 server 容器要走 CLI 的 import --from json：错误必须给出这条命令。
func TestChatMCPAddJSONRejectsMultiServerContainer(t *testing.T) {
	service := newFakeChatMCPService()
	text := chatMCPCommandTextWithService(
		`/mcp add-json notion {"mcpServers":{"notion":{"url":"https://mcp.notion.com/mcp"}}}`, service, nil)
	if !strings.Contains(text, "多 server 容器") || !strings.Contains(text, "aicli mcp import --from json") {
		t.Fatalf("容器输入应给出 import 提示:\n%s", text)
	}
	if len(service.added) != 0 {
		t.Fatalf("失败路径不得写入: %#v", service.added)
	}
}

func TestChatMCPAddJSONWarnsUnknownFields(t *testing.T) {
	service := newFakeChatMCPService()
	text := chatMCPCommandTextWithService(
		`/mcp add-json notion {"url":"https://mcp.notion.com/mcp","header":{"A":"B"}}`, service, nil)
	if !strings.Contains(text, `提示: 忽略未映射字段 "header"`) {
		t.Fatalf("未映射字段应告警:\n%s", text)
	}
	if len(service.added) != 1 || service.added[0].Type != "streamable" {
		t.Fatalf("请求不符: %#v", service.added)
	}
}

func TestChatMCPAddJSONErrorPaths(t *testing.T) {
	service := newFakeChatMCPService()
	cases := []struct {
		name    string
		command string
		want    string
	}{
		{"缺名称", "/mcp add-json", "需要指定 MCP 名称"},
		{"缺 JSON", "/mcp add-json notion", "需要 JSON 内容"},
		{"非法 JSON", `/mcp add-json notion {"url":`, "解析 JSON 失败"},
		{"缺 url/command", `/mcp add-json notion {"type":"http"}`, "command"},
		{"空对象", `/mcp add-json notion {}`, "command"},
		{"stdin 不支持", "/mcp add-json notion -", "stdin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := chatMCPCommandTextWithService(tc.command, service, nil)
			if !strings.Contains(text, tc.want) {
				t.Fatalf("输出缺少 %q:\n%s", tc.want, text)
			}
			if !strings.Contains(text, "/mcp add-json") {
				t.Fatalf("错误路径应带用法:\n%s", text)
			}
		})
	}
	if len(service.added) != 0 {
		t.Fatalf("错误路径不得写入: %#v", service.added)
	}
}

// `mcp get --json` 的导出片段可直接喂回 chat：与 CLI 同一条解析路径。
func TestChatMCPAddJSONAcceptsExportedConfig(t *testing.T) {
	service := newFakeChatMCPService()
	text := chatMCPCommandTextWithService(
		`/mcp add-json files {"name":"files","config":{"type":"stdio","command":"npx","args":["-y","pkg"]}}`, service, nil)
	if !strings.Contains(text, `✓ 已新增 MCP "files"（stdio）`) {
		t.Fatalf("导出片段应可回填:\n%s", text)
	}
	if len(service.added) != 1 || service.added[0].Command != "npx" {
		t.Fatalf("请求不符: %#v", service.added)
	}
}

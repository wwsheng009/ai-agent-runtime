package commands

import (
	"context"
	"fmt"
	"strings"
)

// chat_mcp_add_json.go 实现 chat 内的 `/mcp add-json <name> <JSON|@文件>`：
// 与 CLI 的 `aicli mcp add-json` 共用同一套解析（parseMCPAddJSONRequest），
// 因此 type 别名、url/command 推断、`mcp get --json` 的 .config 片段、
// 未映射字段告警与多 server 容器提示在两边一致。

const chatMCPAddJSONUsage = "用法: /mcp add-json <name> <JSON|@文件>\n" +
	"示例: /mcp add-json notion '{\"type\":\"http\",\"url\":\"https://mcp.notion.com/mcp\",\"headers\":{\"Authorization\":\"Bearer ${NOTION_TOKEN}\"}}'\n" +
	"     /mcp add-json notion @notion.json\n" +
	"整份多 server 文件（{\"mcpServers\":{...}}）请用 CLI: aicli mcp import --from json <文件>"

// chatMCPAddJSONText 执行 add-json：写配置 + 热重载由 service.Add 承担，成功后回调
// onMutate 刷新会话工具面（与 /mcp add 同一路径）。
//
// rawJSON 是命令里「子命令 + server 名」之后的原文：JSON 含空格、引号与 `&`，
// 会被命令 tokenizer 拆散，必须按原文还原而不是按 token 拼接。
func chatMCPAddJSONText(service chatMCPService, args []string, rawJSON string, onMutate func()) string {
	if service == nil {
		return "错误: MCP 管理服务不可用"
	}
	if len(args) == 0 {
		return "错误: 需要指定 MCP 名称\n" + chatMCPAddJSONUsage
	}
	name := strings.TrimSpace(args[0])
	if name == "" {
		return "错误: MCP 名称不能为空\n" + chatMCPAddJSONUsage
	}
	input := strings.TrimSpace(rawJSON)
	if input == "" {
		input = strings.Join(args[1:], " ")
	}
	if input == "" {
		return "错误: 需要 JSON 内容\n" + chatMCPAddJSONUsage
	}
	// chat 不能读 stdin（终端被 TUI 占用），只支持内联 JSON 与 @文件。
	input, err := resolveMCPAddJSONInput(input, false)
	if err != nil {
		return "错误: " + err.Error() + "\n" + chatMCPAddJSONUsage
	}
	request, warnings, err := parseMCPAddJSONRequest(name, input)
	if err != nil {
		return "错误: " + err.Error() + "\n" + chatMCPAddJSONUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCommandTimeout)
	defer cancel()
	configValue, err := service.Add(ctx, request)
	if err != nil {
		return fmt.Sprintf("错误: 新增 MCP %q 失败: %v", name, err)
	}
	if onMutate != nil {
		onMutate()
	}

	transport := strings.TrimSpace(request.Type)
	if configValue != nil && strings.TrimSpace(configValue.Type) != "" {
		transport = configValue.Type
	}
	if transport == "" {
		transport = "未知"
	}
	lines := []string{fmt.Sprintf("✓ 已新增 MCP %q（%s）", name, transport)}
	if path := strings.TrimSpace(service.ConfigPath()); path != "" {
		lines = append(lines, "  配置: "+path)
	}
	for _, warning := range warnings {
		lines = append(lines, "  提示: "+warning)
	}
	lines = append(lines, "  用 /mcp status "+name+" 查看连接状态与工具数。")
	return strings.Join(lines, "\n")
}

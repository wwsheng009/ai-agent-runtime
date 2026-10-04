package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	mcpadmin "github.com/wwsheng009/ai-agent-runtime/internal/mcp/admin"
)

// chat_mcp_completion.go 为 `/mcp` 提供逐位参数补全：
//
//   - 首位：二级子命令（list/enable/disable/status/remove/auth/...）
//   - status|enable|disable|remove|auth 等“作用于已有 server”的子命令
//     的 server 名位：从当前 MCP 配置读取名称（与 /mcp list 同一份数据源）
//   - auth <name> 之后：--status / --clear / --no-browser 旗标
//
// 补全必须是即时的：读取失败或超时一律静默降级为空候选，不弹错误、不阻塞输入。

// chatMCPCompletionTimeout 是补全读取 MCP 列表的短超时。
const chatMCPCompletionTimeout = 2 * time.Second

// completeMCPLineSlashArgs 是 `/mcp` 参数补全入口（使用进程内 MCP 管理服务）。
func completeMCPLineSlashArgs(argsText string, cursor int) []chatSlashCompletionCandidate {
	return completeMCPLineSlashArgsWithService(newChatMCPService(), argsText, cursor)
}

// completeMCPLineSlashArgsWithService 与 completeMCPLineSlashArgs 同逻辑，
// 显式接收服务以便单测注入替身。
func completeMCPLineSlashArgsWithService(service chatMCPService, argsText string, cursor int) []chatSlashCompletionCandidate {
	ctx := parseSlashArgumentContext(argsText, cursor)
	query := activeSlashArgumentQuery(ctx)
	first := strings.ToLower(slashArgumentTokenText(ctx, 0))

	// 首位（子命令本身）或还没输入子命令：给出二级命令候选。
	if first == "" || slashArgumentCursorInToken(ctx, 0) {
		return matchSlashArgumentCandidates(mcpSubcommandArgumentCandidates(), query)
	}

	switch first {
	case "status", "show", "info", "enable", "disable", "remove", "rm", "delete", "auth", "login":
		// server 名位（空位或正在输入）：补全已配置的 MCP 名称。
		if slashArgumentTokenText(ctx, 1) == "" || slashArgumentCursorInToken(ctx, 1) {
			return matchSlashArgumentCandidates(mcpServerNameArgumentCandidates(service), query)
		}
		if first == "auth" || first == "login" {
			// /mcp auth <name> [--status|--clear|--no-browser]
			if slashArgumentTokenText(ctx, 2) == "" || slashArgumentCursorInToken(ctx, 2) {
				return matchSlashArgumentCandidates(mcpAuthFlagArgumentCandidates(), query)
			}
		}
		if first == "enable" || first == "disable" {
			// /mcp enable|disable <name> [--session]
			if slashArgumentTokenText(ctx, 2) == "" || slashArgumentCursorInToken(ctx, 2) {
				return matchSlashArgumentCandidates(mcpSessionFlagArgumentCandidates(), query)
			}
		}
		return nil
	default:
		// list/reload/help/select/add 等不需要名称位补全；多余的参数位也不猜。
		return nil
	}
}

// mcpSubcommandArgumentCandidates 声明 /mcp 的二级命令，与
// chatMCPCommandTextWithService 的 switch 分派保持同源（含别名）。
// 无别名的主命令排在前面，保证弹窗首屏看到的是常用命令而不是别名。
func mcpSubcommandArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupFunctions)
	primary := []chatSlashCompletionCandidate{
		{Command: "list", Summary: "列出全部 MCP 与连接状态", Group: group},
		{Command: "select", Summary: "交互菜单：选择 server → 查看/启停/移除/热重载", Group: group},
		{Command: "status", Summary: "查看单个 MCP 的配置与运行状态", Group: group, AcceptsArgs: true},
		{Command: "add", Summary: "新增 MCP（<name> <url> 或 --command <cmd>）", Group: group, AcceptsArgs: true},
		{Command: "add-json", Summary: "用 JSON 片段新增 MCP（<name> <JSON|@文件>）", Group: group, AcceptsArgs: true},
		{Command: "enable", Summary: "启用；--session 仅本会话（不写配置）", Group: group, AcceptsArgs: true},
		{Command: "disable", Summary: "停用；--session 仅本会话（不写配置）", Group: group, AcceptsArgs: true},
		{Command: "remove", Summary: "删除并热重载", Group: group, AcceptsArgs: true},
		{Command: "reload", Summary: "重新加载配置并重连", Group: group},
		{Command: "auth", Summary: "OAuth 授权：auth <name> [--status|--clear]", Group: group, AcceptsArgs: true},
		{Command: "help", Summary: "显示 /mcp 用法", Group: group},
	}
	aliases := []chatSlashCompletionCandidate{
		{Command: "ls", Summary: "list 的别名", Group: group, AliasOf: "list"},
		{Command: "pick", Summary: "select 的别名", Group: group, AliasOf: "select"},
		{Command: "menu", Summary: "select 的别名", Group: group, AliasOf: "select"},
		{Command: "choose", Summary: "select 的别名", Group: group, AliasOf: "select"},
		{Command: "show", Summary: "status 的别名", Group: group, AliasOf: "status", AcceptsArgs: true},
		{Command: "info", Summary: "status 的别名", Group: group, AliasOf: "status", AcceptsArgs: true},
		{Command: "rm", Summary: "remove 的别名", Group: group, AliasOf: "remove", AcceptsArgs: true},
		{Command: "delete", Summary: "remove 的别名", Group: group, AliasOf: "remove", AcceptsArgs: true},
		{Command: "login", Summary: "auth 的别名", Group: group, AliasOf: "auth", AcceptsArgs: true},
	}
	return append(primary, aliases...)
}

// mcpAuthFlagArgumentCandidates 是 `/mcp auth <name> ` 之后的旗标候选。
func mcpAuthFlagArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupFunctions)
	return []chatSlashCompletionCandidate{
		{Command: "--status", Summary: "查看该 server 的 OAuth 授权状态", Group: group},
		{Command: "--clear", Summary: "清除该 server 的 OAuth 令牌", Group: group},
		{Command: "--no-browser", Summary: "发起授权时不自动打开浏览器", Group: group},
	}
}

// mcpSessionFlagArgumentCandidates 是 `/mcp enable|disable <name> ` 之后的旗标候选。
func mcpSessionFlagArgumentCandidates() []chatSlashCompletionCandidate {
	group := string(chatSlashCommandGroupFunctions)
	return []chatSlashCompletionCandidate{
		{Command: "--session", Summary: "仅本会话生效，不写配置；全局停用时临时连接", Group: group},
	}
}

// mcpServerNameArgumentCandidates 读取当前 MCP 配置中的 server 名作为名称位候选。
// 读取失败或超时返回 nil（补全静默降级）。
func mcpServerNameArgumentCandidates(service chatMCPService) []chatSlashCompletionCandidate {
	if service == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatMCPCompletionTimeout)
	defer cancel()
	items, err := service.List(ctx)
	if err != nil || len(items) == 0 {
		return nil
	}

	names := make([]string, 0, len(items))
	byName := make(map[string]mcpadmin.Item, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Config.Name)
		if name == "" {
			continue
		}
		if _, exists := byName[name]; exists {
			continue
		}
		byName[name] = item
		names = append(names, name)
	}
	sort.Strings(names)

	candidates := make([]chatSlashCompletionCandidate, 0, len(names))
	for _, name := range names {
		candidates = append(candidates, chatSlashCompletionCandidate{
			Command: name,
			Summary: chatMCPCompletionSummary(byName[name]),
			Group:   string(chatSlashCommandGroupFunctions),
		})
	}
	return candidates
}

// chatMCPCompletionSummary 把 /mcp list 的状态口径压缩成一行补全摘要。
func chatMCPCompletionSummary(item mcpadmin.Item) string {
	enabled := item.Config.IsEnabled()
	status := "已停用"
	if enabled {
		status = "已启用，未连接"
	}
	if item.Status == nil {
		return status
	}
	switch {
	case item.Status.RequiresAuth:
		return "需认证"
	case item.Status.Connected:
		status = "已连接"
		if item.Status.ToolCount > 0 {
			status += fmt.Sprintf(" · %d tools", item.Status.ToolCount)
		}
	case !enabled:
		status = "已停用"
	default:
		status = "已启用，未连接"
	}
	return status
}

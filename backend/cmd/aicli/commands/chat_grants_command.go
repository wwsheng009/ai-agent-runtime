package commands

import (
	"fmt"
	"strings"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// chatGrantsUsage is the single usage line shared by the structured cell and
// the legacy stdout projection.
const chatGrantsUsage = "/grants [list|status|revoke <tool> [pattern]]"

// runChatGrantsCommand is the single source of truth for the /grants command:
// it reads the durable remembered grants under <project>/.aicli/grants.json
// (policy.FileGrantStore, the same file as the Web harness GET/POST
// /harness/grants API) and optionally revokes one grant.
//
// Write surface boundary: this CLI face is deliberately read+revoke only. New
// grants are still created through the approval "记住" choices or the Web
// settings page, so the CLI cannot widen the permission set.
func runChatGrantsCommand(session *ChatSession, command string) (string, error) {
	root := strings.TrimSpace(folderTrustProjectRoot(session))
	if root == "" {
		return "", fmt.Errorf("项目根目录未就绪，无法访问 durable 授权存储")
	}
	store, err := runtimepolicy.OpenProjectGrantStore(root)
	if err != nil {
		return "", fmt.Errorf("打开 durable 授权存储失败: %w", err)
	}

	value := strings.TrimSpace(extractCommandArgument(command))
	subcommand, rest := splitGrantsFirstToken(value)
	switch strings.ToLower(subcommand) {
	case "", "list", "status":
		if rest != "" {
			return "", fmt.Errorf("/grants %s 不接受额外参数: %s", subcommand, rest)
		}
		return formatChatGrantsList(store), nil
	case "revoke":
		if rest == "" {
			return "", fmt.Errorf("/grants revoke 需要指定 tool")
		}
		return formatChatGrantsRevoke(store, rest)
	default:
		return "", fmt.Errorf("未知的 /grants 子命令: %s", subcommand)
	}
}

// formatChatGrantsList renders the durable grants with their specifier form.
// An empty list is a successful read, not an error: the store treats a missing
// file as empty, and /grants must not turn that into a failure.
func formatChatGrantsList(store *runtimepolicy.FileGrantStore) string {
	grants := store.List()
	if len(grants) == 0 {
		return strings.Join([]string{
			"当前没有 durable 授权",
			"durable 授权来自审批弹窗中的「记住」选择或 Web 设置页的授权管理",
			"使用 /grants revoke <tool> [pattern] 撤销已有授权",
		}, "\n")
	}
	lines := []string{fmt.Sprintf("durable 授权: %d 条（文件: %s）", len(grants), store.Path())}
	for _, grant := range grants {
		pattern := strings.TrimSpace(grant.Pattern)
		if pattern == "" {
			pattern = "全部"
		}
		scope := strings.TrimSpace(grant.Scope)
		if scope == "" {
			scope = "project"
		}
		lines = append(lines, fmt.Sprintf("- %s · %s · %s", strings.TrimSpace(grant.Tool), pattern, scope))
	}
	lines = append(lines, "使用 /grants revoke <tool> [pattern] 撤销授权（省略 pattern 仅撤销该 tool 的 tool-wide 授权）")
	return strings.Join(lines, "\n")
}

// formatChatGrantsRevoke removes one durable grant. Revoking only narrows the
// permission set, so it intentionally has no second confirmation: an accidental
// revoke can never widen what the runtime allows, it only asks again later.
//
// pattern == "" keeps the existing GrantRevoker contract (matchEmptyPattern
// true), i.e. it revokes that tool's tool-wide grants only; a named pattern is
// matched exactly against the stored specifier.
func formatChatGrantsRevoke(store *runtimepolicy.FileGrantStore, rest string) (string, error) {
	tool, pattern := splitGrantsFirstToken(rest)
	if tool == "" {
		return "", fmt.Errorf("/grants revoke 需要指定 tool")
	}
	removed := store.Revoke(tool, pattern, pattern == "")
	target := fmt.Sprintf("tool=%s pattern=全部(tool-wide)", tool)
	if pattern != "" {
		target = fmt.Sprintf("tool=%s pattern=%s", tool, pattern)
	}
	return fmt.Sprintf("已撤销 durable 授权: %d 条（%s）", removed, target), nil
}

// splitGrantsFirstToken splits "first rest" while preserving the remainder
// verbatim (grant patterns such as "exact:echo a && echo b" may contain
// spaces, so the revoke pattern must not be re-joined from fields).
func splitGrantsFirstToken(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if idx := strings.IndexAny(value, " \t"); idx >= 0 {
		return value[:idx], strings.TrimSpace(value[idx+1:])
	}
	return value, ""
}

// executeStructuredGrantsCommand is the unified-renderer projection.
func executeStructuredGrantsCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	text, err := runChatGrantsCommand(session, command)
	if err != nil {
		return commandTextResult(fmt.Sprintf("错误: %v\n用法: %s", err, chatGrantsUsage))
	}
	return commandTextResult(text)
}

// handleGrantsCommand is the legacy compatibility projection. Normal
// interactive dispatch claims /grants in tryExecuteStructuredChatCommand before
// the broad legacy gate, so this only serves direct handleCommand callers.
func handleGrantsCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	text, err := runChatGrantsCommand(session, command)
	if err != nil {
		printfChatCommandOutput(session, "错误: %v\n用法: %s", err, chatGrantsUsage)
		return false
	}
	printChatCommandOutput(session, text)
	return false
}

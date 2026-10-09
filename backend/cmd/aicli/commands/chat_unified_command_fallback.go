package commands

import (
	"fmt"
	"strings"
)

// dispatchUnifiedUnknownChatCommand 是统一渲染会话的最终回落：未被结构化
// 认领的命令一律提交 typed "未知命令" 结果（fail-closed），绝不进入 legacy
// 直写处理器。目录命令的认领完整性由
// TestUnifiedCatalogCommandsNeverFallToUnknown 机械守卫；/exit、/help 等
// 有限命令在结构化分派中有各自认领。
func dispatchUnifiedUnknownChatCommand(session *ChatSession, command string) bool {
	name := unifiedChatCommandName(command)
	if name == "" {
		name = strings.TrimSpace(command)
	}
	if name == "" {
		name = "该命令"
	}
	if _, known := chatSlashCommandCatalogMap()[name]; known {
		// 目录内命令的某个参数/状态形式未被结构化认领：给出明确的不支持说明，
		// 而不是误导性的"未知命令"（裸形式认领由覆盖测试机械守卫；精确到分支的
		// typed 文案列为后续小项）。
		_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("错误: %s 的当前参数或状态形式不受支持\n输入 /help 查看用法", name)), false)
		return false
	}
	_ = renderChatCommandResult(session, commandTextResult(fmt.Sprintf("错误: 未知命令: %s\n输入 /help 查看可用命令", name)), false)
	return false
}

func unifiedChatCommandName(command string) string {
	name, _ := splitFirstToken(strings.TrimSpace(command))
	return strings.ToLower(strings.TrimSpace(name))
}

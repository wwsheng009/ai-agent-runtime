package commands

// §4.4 自动修订回合（CLI 半程）：带意见的 request_changes 在裁决落地之后立刻起一轮
// 修订，与 Web 面板的 trigger_revision 同语义。两种投影各走既有机制：
//
//   - 统一 TTY（结构化 /plan）：把合成指令挂到 CommandResult.SendMessageAfterCommit，
//     由 command.go 在裁决单元落盘之后经正常 send 管线提交（与 /shell、/cmd 同一条
//     post-commit 边界）；
//   - 纯文本 REPL：裁决行打印之后直接调用 sendChatMessageAfterCommit。
//
// 两条路径共用下面这一个判定，避免出现第二套「要不要触发」的口径。
//
// 输出一律走 printfDirectInteractiveOutput：本文件属于交互式命令面，直接 fmt.Print*
// 会绕过 TerminalSession 的所有权（P0 迁移清单 TestChatInteractiveDirectWriterInventory
// 也明令新特性不得新增直接写入）。

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/planmode"
)

// planRevisionAfterCommitEffect 返回这次裁决要不要带一轮修订：仅 request_changes +
// 非空意见 + 交互输出。脚本/JSON（NoInteractive / JSONOutput）保持旧语义——意见留在
// pending_review_notes，由下一次用户输入交付，不给自动化流程塞一轮意外的模型回合。
func planRevisionAfterCommitEffect(session *ChatSession, decisionToken, notes string) string {
	if session == nil || session.NoInteractive || session.JSONOutput {
		return ""
	}
	if planmode.ExitDecision(decisionToken) != planmode.ExitRequestChanges {
		return ""
	}
	if strings.TrimSpace(notes) == "" {
		return ""
	}
	// 正文刻意不带评审意见：意见仍由该轮通过 planmode 的一次性提醒通道（plan_review）
	// 交付并清除，两条通道因此不可能重复（与 HTTP 侧 trigger_revision 同一约定）。
	return agent.PlanRevisionPrompt()
}

// startChatPlanRevisionRound 是纯文本 REPL 的触发点：裁决行已经打印，这里再提交修订
// 轮。提交失败只提示、不回滚裁决——意见仍在 durable 状态，下一次输入照旧能交付。
func startChatPlanRevisionRound(session *ChatSession, decisionToken, notes string) {
	prompt := planRevisionAfterCommitEffect(session, decisionToken, notes)
	if prompt == "" {
		return
	}
	if err := sendChatMessageAfterCommit(session, prompt); err != nil {
		printfDirectInteractiveOutput(session, "错误: 自动修订回合提交失败: %v\n", err)
		printfDirectInteractiveOutput(session, "提示: 评审意见仍在待交付状态，下一次输入时送达模型\n")
	}
}

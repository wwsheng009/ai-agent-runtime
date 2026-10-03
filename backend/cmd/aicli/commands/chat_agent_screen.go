package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// chatAgentScreenListID 是 /agents 副屏列表的 ScreenID。
const chatAgentScreenListID = "agents.list"

// chatScreenAgentsListSpec 构建 /agents 副屏列表 Spec（unified 交互出口）：
//   - Title "Agent 列表"、Subtitle 指引选择、ConfirmLabel "查看输出"；
//   - 每行 Title 为 agent path，Detail 为紧凑状态摘要，SearchText 聚合可检索字段；
//   - 无子 agent（或列表获取失败）时放入占位行，保证列表 Spec 合法、副屏照常打开，
//     而不是退回主屏内联输出；
//   - DegradeDoc 保留既有 "Agent Graph:" 主屏文本：能力不足/租约忙/嵌套时按
//     框架降级契约内联显示（与迁移前逐行一致）；
//   - AfterClose：用户确认选择后解析目标 agent，设置会话选中 target，并在
//     租约释放后打开只读 transcript 副屏；占位行（下标越界）不会触发任何变更。
func chatScreenAgentsListSpec(session *ChatSession) chatScreenSpec {
	agents, err := chatAgentGraphItems(session)
	rows := make([]chatScreenRow, 0, len(agents)+1)
	for _, agent := range agents {
		path := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
		rows = append(rows, chatScreenRow{
			Title:      path,
			Detail:     chatAgentListRowDetail(agent),
			SearchText: chatAgentListRowSearchText(agent),
		})
	}
	if err != nil {
		rows = append(rows, chatScreenRow{
			Title:  "（agent 列表不可用）",
			Detail: strings.TrimSpace(err.Error()),
		})
	} else if len(agents) == 0 {
		rows = append(rows, chatScreenRow{
			Title:  "（暂无子 agent）",
			Detail: "spawn_agent 后在此查看输出",
		})
	}
	return chatScreenSpec{
		ID:           chatAgentScreenListID,
		Title:        "Agent 列表",
		Kind:         chatScreenList,
		Subtitle:     "↑↓ 选择 agent，Enter 查看输出，Esc 返回",
		ConfirmLabel: "查看输出",
		Rows:         rows,
		Trigger:      "command",
		DegradeDoc:   chatScreenTextDoc(chatAgentGraphText(session)),
		AfterClose:   chatAgentListAfterCloseHandler(agents),
	}
}

// chatAgentGraphText 是 /agents 既有主屏文本投影（与迁移前逐行一致），
// 同时作为列表 Spec 的降级内联文档来源。
func chatAgentGraphText(session *ChatSession) string {
	lines := []string{"Agent Graph:"}
	lines = append(lines, chatAgentGraphLines(session)...)
	return strings.Join(lines, "\n")
}

// chatAgentListRowDetail 构建 agent 列表行的紧凑 Detail 字符串。
func chatAgentListRowDetail(agent toolbroker.AgentStatusResult) string {
	parts := make([]string, 0, 8)
	status := firstNonEmptyChatValue(agent.Status, "unknown")
	parts = append(parts, "status="+status)
	if agent.SessionState != "" {
		parts = append(parts, "state="+agent.SessionState)
	}
	if agent.Depth > 0 {
		parts = append(parts, fmt.Sprintf("depth=%d", agent.Depth))
	}
	if agent.AgentType != "" {
		parts = append(parts, "type="+agent.AgentType)
	}
	if agent.RunStatus != "" {
		parts = append(parts, "run="+agent.RunStatus)
	}
	if agent.PendingApproval {
		parts = append(parts, "approval=pending")
	}
	if agent.PendingQuestion {
		parts = append(parts, "question=pending")
	}
	if agent.PendingToolName != "" {
		parts = append(parts, "tool="+agent.PendingToolName)
	}
	return strings.Join(parts, " ")
}

// chatAgentListRowSearchText 聚合所有可检索字段，供副屏列表即时搜索。
func chatAgentListRowSearchText(agent toolbroker.AgentStatusResult) string {
	fields := []string{
		agent.Path,
		agent.SessionID,
		agent.ID,
		agent.Status,
		agent.SessionState,
		agent.AgentType,
		agent.TeamID,
		agent.TeammateID,
		agent.RunID,
		agent.RunStatus,
		agent.CurrentTaskID,
		agent.CurrentTaskStatus,
		agent.PendingToolName,
	}
	nonEmpty := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			nonEmpty = append(nonEmpty, strings.ToLower(f))
		}
	}
	return strings.Join(nonEmpty, " ")
}

// chatAgentListAfterCloseHandler 返回 AfterClose 回调：在 agent 列表副屏关闭后，
// 若用户确认选择，则解析目标 agent，设置会话选中 target，并打开 transcript 副屏。
// 该回调在 openChatScreen 返回之后执行（I3）：chatScreenOpenActive 已归零，
// 因此从 AfterClose 打开另一个副屏是安全的。占位行无法映射到 agents 下标，
// 由下标检查直接忽略（不写入选中 target、不打开 transcript）。
func chatAgentListAfterCloseHandler(agents []toolbroker.AgentStatusResult) func(*ChatSession, chatScreenOutcome) {
	return func(session *ChatSession, outcome chatScreenOutcome) {
		if outcome.Degraded {
			return
		}
		if outcome.Result != chatScreenClosedConfirm || outcome.Index < 0 {
			return
		}
		if outcome.Index >= len(agents) {
			return
		}
		selected := agents[outcome.Index]
		target := firstNonEmptyChatValue(selected.Path, selected.SessionID, selected.ID)
		if target == "" {
			return
		}
		setChatSelectedAgentTarget(session, target)
		warnIfChatSessionSyncFails(session, "set selected agent target", syncRuntimeSessionFromChat(session))

		// 构建 transcript 视图并打开只读副屏。
		opts := chatAgentTranscriptOptions{
			Target:  target,
			Limit:   chatAgentTranscriptDefaultLimit,
			Timeout: chatAgentTranscriptFollowDefaultTimeout,
		}
		view, err := buildChatAgentTranscriptView(session, opts)
		if err != nil {
			printChatCommandOutput(session, fmt.Sprintf("错误: %v", err))
			return
		}
		lines := view.Lines()
		transcriptSpec := chatScreenAgentTranscriptSpec(lines)
		chatScreenOpenAndApply(session, transcriptSpec)
	}
}

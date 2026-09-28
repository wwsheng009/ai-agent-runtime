package commands

import (
	"fmt"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render"
)

// /todos — 任务列表查看命令（v1.3.1，设计见 docs/plan/
// aicli-tui-busy-slash-command-execution-plan-20260927.md 附录 F.7）。
//
// 统一运行时交互机制（方案 §3.8）中的注册声明（宿主落地前的单一事实源）：
//
//	业务域: C12 任务与待办
//	模式:   screen（副屏任务面板，可切换视图）；无副屏能力时降级 inline
//	生效域: read（只读快照；不写会话、不发 turn、零确认）
//
// 在 runtimeCommandHost 落地前，忙时行为由 chatSlashCommandQueueSafe 承接
// （先排队，回合结束后执行，不丢输入）；宿主落地后本命令应迁入注册表并
// 支持运行时 inline/screen 两种模式。
const (
	chatTodosCommandName = "/todos"
)

// chatTodosFilter 是 /todos 的"切换查看"过滤视图。
type chatTodosFilter string

const (
	chatTodosFilterAll    chatTodosFilter = "all"
	chatTodosFilterActive chatTodosFilter = "active" // pending + in_progress
	chatTodosFilterDone   chatTodosFilter = "done"
	chatTodosFilterBrief  chatTodosFilter = "brief"
)

// executeStructuredTodosCommand 渲染当前会话最近一次 todos 工具快照。
//
// 数据面与 web「任务列表」面板同源（web_todo_snapshot.go）：
// 反向扫描 transcript 中最近一条带 todo 快照的 todos 工具结果。
// 快照缺失/为空即视为无待办，不渲染过期数据。
func executeStructuredTodosCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	filter, err := parseChatTodosFilter(command)
	if err != nil {
		return commandTextResult(err.Error())
	}
	return CommandResult{
		Blocks: []RenderBlock{{Document: buildChatTodosDocument(session, filter)}},
		Action: CommandContinue,
	}
}

// parseChatTodosFilter 解析 /todos 的第一个参数；未知参数返回用法错误。
func parseChatTodosFilter(command string) (chatTodosFilter, error) {
	arg := strings.ToLower(strings.TrimSpace(extractCommandArgument(command)))
	switch arg {
	case "", "all", "todo", "todos":
		return chatTodosFilterAll, nil
	case "active", "open", "doing", "pending":
		return chatTodosFilterActive, nil
	case "done", "completed", "complete":
		return chatTodosFilterDone, nil
	case "brief", "summary":
		return chatTodosFilterBrief, nil
	default:
		return "", fmt.Errorf("错误: 未知的 /todos 参数 %q\n用法: /todos [all|active|done|brief]", arg)
	}
}

// buildChatTodosDocument 构建只读任务列表文档；供命令渲染与测试直接复用。
func buildChatTodosDocument(session *ChatSession, filter chatTodosFilter) render.Document {
	snapshot := chatTodosSnapshotForSession(session)
	return buildChatPlainTextCommandDocument(strings.Join(buildChatTodosLines(snapshot, filter), "\n"))
}

// handleTodosCommand 是 /todos 的 legacy/plain 出口（无统一渲染会话时使用），
// 与结构化处理器共享同一份快照与视图逻辑。
func handleTodosCommand(session *ChatSession, command string) bool {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return false
	}
	filter, err := parseChatTodosFilter(command)
	if err != nil {
		printChatCommandOutput(session, err.Error())
		return false
	}
	printChatCommandOutput(session, strings.Join(buildChatTodosLines(chatTodosSnapshotForSession(session), filter), "\n"))
	return false
}

// chatTodosSnapshotForSession 取当前会话最近一次待办快照（只读）。
//
// 注意（方案 V12）：transcript 扫描与运行中 turn 的 messages 写入并发，
// 需在 -race 下验证；宿主落地时应改为「tool_end 缓存快照（加锁）+ 无缓存回退扫描」。
func chatTodosSnapshotForSession(session *ChatSession) *chatWebTodoSnapshot {
	if session == nil {
		return nil
	}
	return chatWebTodoSnapshotFromMessages(sessionTranscriptMessages(session), currentRuntimeSessionID(session))
}

func buildChatTodosLines(snapshot *chatWebTodoSnapshot, filter chatTodosFilter) []string {
	if snapshot == nil || len(snapshot.Items) == 0 {
		return []string{"当前会话暂无待办", "提示: 模型可通过 todos 工具创建/更新任务列表"}
	}

	pending, inProgress, completed := chatTodosStatusCounts(snapshot.Items)
	if filter == chatTodosFilterBrief {
		return []string{fmt.Sprintf(
			"任务列表: %d 项（进行中 %d / 待办 %d / 已完成 %d）",
			len(snapshot.Items), inProgress, pending, completed,
		)}
	}

	lines := []string{fmt.Sprintf(
		"任务列表（%d 项：进行中 %d / 待办 %d / 已完成 %d）",
		len(snapshot.Items), inProgress, pending, completed,
	)}
	for _, item := range snapshot.Items {
		if !chatTodosFilterMatches(item.Status, filter) {
			continue
		}
		lines = append(lines, formatChatTodoLine(item))
	}
	if len(lines) == 1 {
		lines = append(lines, "（当前视图无匹配项）")
	}
	return lines
}

func chatTodosStatusCounts(items []chatWebTodoItem) (pending, inProgress, completed int) {
	for _, item := range items {
		switch item.Status {
		case "pending":
			pending++
		case "in_progress":
			inProgress++
		case "completed":
			completed++
		}
	}
	return pending, inProgress, completed
}

func chatTodosFilterMatches(status string, filter chatTodosFilter) bool {
	switch filter {
	case chatTodosFilterActive:
		return status == "pending" || status == "in_progress"
	case chatTodosFilterDone:
		return status == "completed"
	default:
		return true
	}
}

// formatChatTodoLine 输出紧凑的单项行：`[x] 已完成`、`[>] 进行中 — 执行态文案`、`[ ] 待办`。
func formatChatTodoLine(item chatWebTodoItem) string {
	label := strings.TrimSpace(item.Content)
	switch item.Status {
	case "completed":
		return "[x] " + label
	case "in_progress":
		if active := strings.TrimSpace(item.ActiveForm); active != "" {
			return "[>] " + label + " — " + active
		}
		return "[>] " + label
	default:
		return "[ ] " + label
	}
}

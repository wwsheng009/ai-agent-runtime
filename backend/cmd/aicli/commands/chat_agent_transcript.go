package commands

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// 子会话 transcript 视图（P0，只读快照）：
//
// 设计约束（见 docs/plan/subagent-transcript-separation-analysis-20260919.md §4.4）：
//   - 主界面只显示主 agent 的 transcript；子 agent 内容通过本视图单独查看，
//     绝不并入父 Scene / 父 timeline（父侧隔离由 chat_runtime_events.go 的
//     isForeignSessionContentEvent 守卫保证）。
//   - 数据源以 EventStore 事件流为主（与前端 SubagentSessionDialog 同源），
//     不使用 /load <child-session-id>（那会把子会话切成主会话，破坏隔离）。
//   - 只读：视图内不提供发送入口，写操作仍走 /agents send|followup 控制面。
const (
	// chatAgentTranscriptPopupOwner 与 chatAgentPanelPopupOwner 分离：子会话
	// transcript 视图与协作面板可以同时存在、互不覆盖。
	chatAgentTranscriptPopupOwner = "agent_transcript"

	chatAgentTranscriptDefaultLimit = 200
	chatAgentTranscriptMaxLimit     = 2000
	// chatAgentTranscriptMaxVisibleRows 对齐前端下钻对话框
	// （frontend/src/components/workspace/trajectory/subagent-session-dialog.tsx）
	// 的 MAX_VISIBLE_ROWS=400：视图只保留最近 400 行正文，超出部分以
	// truncated 标记省略（尾部窗口语义与前端 items.slice(-400) 一致）。
	chatAgentTranscriptMaxVisibleRows = 400
	// chatAgentTranscriptFollowDefaultTimeout 是 legacy（无 popup）follow 的
	// 单次等待时长，与 /collab follow 的默认口径一致。
	chatAgentTranscriptFollowDefaultTimeout = 10 * time.Second
	// chatAgentTranscriptSource* 标记视图正文的数据源（Q2 裁定）：events 是
	// EventStore 事件流（实时/近期子会话主口径）；messages 是 SessionStore
	// canonical messages（G9 历史回放兜底，仅当事件窗口为空时启用）。
	chatAgentTranscriptSourceEvents   = "events"
	chatAgentTranscriptSourceMessages = "messages"
)

// chatAgentTranscriptTailEventStore 是 EventStore 的尾部读取扩展（内置的
// InMemoryRuntimeStore 与 SQLiteRuntimeStore 都已实现）：transcript 视图要的是
// "最近一页"事件，而不是会话开头的事件。
type chatAgentTranscriptTailEventStore interface {
	ListEventsBefore(ctx context.Context, sessionID string, beforeSeq int64, limit int) ([]runtimeevents.Event, error)
}

// handleChatAgentTranscriptCommand 是 /agent [target] 与 /agents view [target]
// 的 legacy（非 unified）输出路径：优先固定 popup，无 surface 时经统一输出边界
// （printChatCommandOutput）投影。命令处理器不得直接 fmt.Print*：该边界负责
// TerminalSession 所有权转移后的输出记账（见 chat_surface_output.go），
// chat_command_result_test.go 的 chatDirectWriterInventory 债务账本也不接受
// 新增原始 writer。
func handleChatAgentTranscriptCommand(session *ChatSession, command string) {
	if session == nil {
		printChatCommandOutput(session, "错误: 当前没有活动会话")
		return
	}
	opts := parseChatAgentTranscriptArgs(strings.TrimSpace(extractCommandArgument(command)))
	if isChatAgentTranscriptCloseTarget(opts.Target) {
		clearChatAgentTranscriptPopup(session)
		printChatCommandOutput(session, "Agent Transcript 已关闭")
		return
	}
	view, err := buildChatAgentTranscriptView(session, opts)
	if err != nil {
		printfChatCommandOutput(session, "错误: %v", err)
		return
	}
	if useRuntimeSelectionPopup(session) {
		if opts.Follow {
			if view.Source == chatAgentTranscriptSourceMessages {
				// G9：messages 回放没有 live 事件源，不建立订阅（follow 语义
				// 只在事件流上成立）。
				stopChatAgentTranscriptFollow(session)
			} else {
				// G4：fixed-bottom popup 模式使用非阻塞订阅，事件到达即重绘；
				// 终态收口在 startChatAgentTranscriptFollow 内部处理。
				view.FollowActive = startChatAgentTranscriptFollow(session, view)
			}
		} else {
			stopChatAgentTranscriptFollow(session)
		}
		showChatAgentTranscriptPopup(session, view.Lines())
		if session.Interaction != nil {
			session.Interaction.SetNotice("Agent Transcript")
		}
		return
	}
	lines := view.Lines()
	if opts.Follow {
		// legacy console 无 popup：与 /collab follow 同款"等待下一次更新后
		// 刷新一次"语义（不逐条流式，保持一次性命令输出边界）。
		lines = append(lines, chatAgentTranscriptFollowOnceLines(session, view, opts.Timeout)...)
	}
	printChatCommandOutput(session, strings.Join(lines, "\n"))
}

// executeStructuredAgentTranscriptCommand 把子会话 transcript 快照接入统一命令
// 管线（unified/TUI 输出），提交一个有限文本单元格。follow 在此路径只做
// 说明：unified 单元格是一次性提交，不承载持续刷新（与 /agents panel 的
// "live modal loop is not revived" 口径一致）；实时刷新在交互式 popup 路径。
func executeStructuredAgentTranscriptCommand(session *ChatSession, command string) CommandResult {
	if session == nil {
		return commandErrorResult(fmt.Errorf("当前没有活动会话"))
	}
	opts := parseChatAgentTranscriptArgs(strings.TrimSpace(extractCommandArgument(command)))
	if isChatAgentTranscriptCloseTarget(opts.Target) {
		clearChatAgentTranscriptPopup(session)
		return commandTextResult("Agent Transcript 已关闭")
	}
	view, err := buildChatAgentTranscriptView(session, opts)
	if err != nil {
		return commandErrorResult(err)
	}
	lines := view.Lines()
	if opts.Follow {
		lines = append(lines, "  follow=unavailable（一次性快照输出；交互式 popup 模式支持实时刷新）")
	}
	return commandTextResult(strings.Join(lines, "\n"))
}

// parseChatAgentTranscriptArgs 解析两种入口共用的参数：
//
//	/agent [target] [limit=N] [follow] [timeout=10s]
//	/agents view|open|transcript [target] [limit=N] [follow] [timeout=10s]
type chatAgentTranscriptOptions struct {
	Target  string
	Limit   int
	Follow  bool
	Timeout time.Duration
}

func parseChatAgentTranscriptArgs(argument string) chatAgentTranscriptOptions {
	opts := chatAgentTranscriptOptions{
		Limit:   chatAgentTranscriptDefaultLimit,
		Timeout: chatAgentTranscriptFollowDefaultTimeout,
	}
	for _, field := range splitChatCommandFields(argument) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		switch strings.ToLower(field) {
		case "view", "open", "transcript", "show":
			continue
		case "follow", "watch":
			opts.Follow = true
			continue
		}
		if value, ok := chatAgentTranscriptLimitToken(field); ok {
			opts.Limit = value
			continue
		}
		if duration, ok := chatCollabTimeoutToken(field); ok {
			if duration > 0 {
				opts.Timeout = duration
			}
			continue
		}
		if opts.Target == "" {
			opts.Target = field
		}
	}
	return opts
}

func chatAgentTranscriptLimitToken(field string) (int, bool) {
	for _, prefix := range []string{"limit=", "--limit="} {
		if !strings.HasPrefix(strings.ToLower(field), prefix) {
			continue
		}
		value := strings.TrimSpace(field[len(prefix):])
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			return 0, false
		}
		return parsed, true
	}
	return 0, false
}

func isChatAgentTranscriptCloseTarget(target string) bool {
	switch strings.ToLower(strings.TrimSpace(target)) {
	case "close", "off", "clear":
		return true
	default:
		return false
	}
}

// chatAgentTranscriptView 是一次视图渲染的状态：解析后的 agent 身份、尾部
// 事件窗口与渲染正文（未裁剪）。follow 订阅按事件增量合并到该状态，再统一
// 经 Lines() 输出，保证快照与实时刷新共用同一渲染口径。
type chatAgentTranscriptView struct {
	Target       string
	Agent        toolbroker.AgentStatusResult
	SessionID    string
	Source       string
	Events       []runtimeevents.Event
	Body         []string
	Ended        bool
	FollowActive bool
}

// buildChatAgentTranscriptView 组装只读视图：解析 target → 读子会话事件 →
// 渲染正文（最多保留最近 chatAgentTranscriptMaxVisibleRows 行，口径与前端
// SubagentSessionDialog 的 items.slice(-MAX_VISIBLE_ROWS) 一致）。
func buildChatAgentTranscriptView(session *ChatSession, opts chatAgentTranscriptOptions) (*chatAgentTranscriptView, error) {
	if session == nil {
		return nil, fmt.Errorf("当前没有活动会话")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventStore == nil {
		return nil, fmt.Errorf("当前会话没有可用的 runtime event store")
	}
	target := strings.TrimSpace(opts.Target)
	if target == "" {
		target = strings.TrimSpace(chatSessionSelectedAgentTarget(session))
	}
	if target == "" {
		return nil, fmt.Errorf("未指定 agent target（用法: /agent <target> 或 /agents view <target>；可用 /agents 查看 target）")
	}
	agent, err := resolveChatAgentTarget(session, target)
	if err != nil {
		return nil, err
	}
	sessionID := firstNonEmptyChatValue(agent.SessionID, agent.ID)
	if sessionID == "" {
		return nil, fmt.Errorf("agent %s 没有可用的 session id", target)
	}
	// G7：target→SessionID 收敛到 registry 的单一映射函数（与 /agents send /
	// approve / answer 同源）；registry 不可用时回退 agent 快照身份（P0 口径，
	// 保证只读视图在轻量 harness 下仍可构建）。
	if host := session.LocalRuntimeHost; host != nil && host.ActorRegistry != nil {
		if resolved, resolveErr := host.ActorRegistry.resolveLocalAgentTargetSessionID(context.Background(), target); resolveErr == nil {
			if resolved = strings.TrimSpace(resolved); resolved != "" {
				sessionID = resolved
			}
		}
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = chatAgentTranscriptDefaultLimit
	}
	if limit > chatAgentTranscriptMaxLimit {
		limit = chatAgentTranscriptMaxLimit
	}
	events, err := listChatAgentTranscriptEvents(session, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("读取 agent transcript 失败: %w", err)
	}
	view := &chatAgentTranscriptView{
		Target:    target,
		Agent:     *agent,
		SessionID: sessionID,
		Source:    chatAgentTranscriptSourceEvents,
		Events:    events,
	}
	body := make([]string, 0, len(events))
	for _, event := range events {
		body = append(body, chatAgentTranscriptEventLines(event)...)
	}
	view.Ended = chatAgentTranscriptLastTerminalEvent(events, sessionID) != nil
	// G9 历史回放：事件流没有覆盖该子会话（已结束且事件被清理，或由其他进程
	// 写入）时回退到 SessionStore 的 canonical messages。Q2 裁定事件流为主、
	// messages 仅作历史补充，因此只在事件窗口为空时启用。
	if len(events) == 0 {
		if stored := loadChatAgentTranscriptSession(session, sessionID); stored != nil {
			if replay := chatAgentTranscriptMessageLines(stored); len(replay) > 0 {
				body = append(body, replay...)
				view.Source = chatAgentTranscriptSourceMessages
				view.Ended = true
			}
		}
	}
	body = append(body, chatAgentTranscriptActionLines(*agent, events)...)
	view.Body = body
	return view, nil
}

// capChatAgentTranscriptRows 只保留最近 maxRows 行正文；被省略时在正文头部
// 加一行 truncated 标记（标记不计入 400 行正文口径）。
func capChatAgentTranscriptRows(rows []string, maxRows int) ([]string, int) {
	if maxRows <= 0 || len(rows) <= maxRows {
		return rows, 0
	}
	omitted := len(rows) - maxRows
	capped := make([]string, 0, maxRows+1)
	capped = append(capped, fmt.Sprintf("  <truncated: 已省略较早的 %d 行（仅显示最近 %d 行）>", omitted, maxRows))
	capped = append(capped, rows[len(rows)-maxRows:]...)
	return capped, omitted
}

// Lines 返回 header + 正文（正文按 400 行上限裁剪，含截断标记与可操作提示）。
func (v *chatAgentTranscriptView) Lines() []string {
	if v == nil {
		return nil
	}
	body, omitted := capChatAgentTranscriptRows(v.Body, chatAgentTranscriptMaxVisibleRows)
	visibleRows := len(v.Body)
	if visibleRows > chatAgentTranscriptMaxVisibleRows {
		visibleRows = chatAgentTranscriptMaxVisibleRows
	}
	source := strings.TrimSpace(v.Source)
	if source == "" {
		source = chatAgentTranscriptSourceEvents
	}
	lines := chatAgentTranscriptHeaderLines(v.Agent, v.SessionID, len(v.Events), visibleRows, omitted, source, v.FollowActive, v.Ended)
	if len(v.Events) == 0 && len(v.Body) == 0 {
		lines = append(lines, "  <empty: 该子会话暂无持久化事件或消息>")
		return lines
	}
	return append(lines, body...)
}

// chatAgentTranscriptActionLines 为仍处于 pending 的审批/问题生成可操作提示
// （G5）：动作本身仍走控制面命令（/agents approve|deny|answer），视图不私建
// 通道（§4.4 纪律 3）。pending 状态取自 agent 快照，id 取自事件窗口内最近
// 一条同类型事件。
func chatAgentTranscriptActionLines(agent toolbroker.AgentStatusResult, events []runtimeevents.Event) []string {
	target := firstNonEmptyChatValue(agent.Path, agent.SessionID, agent.ID)
	var lines []string
	if agent.PendingApproval {
		if requestID := chatAgentTranscriptLastEventField(events, runtimechat.EventApprovalRequested, "request_id"); requestID != "" {
			lines = append(lines,
				fmt.Sprintf("  [action] approve: /agents approve %s %s", target, requestID),
				fmt.Sprintf("  [action] deny:    /agents deny %s %s", target, requestID),
			)
		}
	}
	if agent.PendingQuestion {
		if questionID := chatAgentTranscriptLastEventField(events, runtimechat.EventQuestionAsked, "question_id"); questionID != "" {
			lines = append(lines, fmt.Sprintf("  [action] answer: /agents answer %s %s <text>", target, questionID))
		}
	}
	return lines
}

// chatAgentTranscriptLastEventField 反向扫描事件窗口，取最近一条指定类型
// 事件中的字符串字段（request_id / question_id）。
func chatAgentTranscriptLastEventField(events []runtimeevents.Event, eventType, field string) string {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].Type != eventType {
			continue
		}
		if value := strings.TrimSpace(payloadStringValue(events[index].Payload[field])); value != "" {
			return value
		}
	}
	return ""
}

// chatAgentTranscriptLastTerminalEvent 返回窗口内最后一条属于目标会话的终态
// 事件；身份口径与 chat_runtime_events.go 的 isForeignSessionTerminalEvent
// 一致：身份缺失或不属于目标会话的终态都不收口本视图。
func chatAgentTranscriptLastTerminalEvent(events []runtimeevents.Event, sessionID string) *runtimeevents.Event {
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if !chatAgentTranscriptTerminalEventFor(event, sessionID) {
			continue
		}
		return &events[index]
	}
	return nil
}

// listChatAgentTranscriptEvents 尾部优先读取最近 limit 条事件；store 未实现
// 尾部读取时退回 ListEvents 升序窗口（P2 历史回放再统一分页语义）。
func listChatAgentTranscriptEvents(session *ChatSession, sessionID string, limit int) ([]runtimeevents.Event, error) {
	store := session.LocalRuntimeHost.EventStore
	ctx := context.Background()
	if tail, ok := store.(chatAgentTranscriptTailEventStore); ok {
		return tail.ListEventsBefore(ctx, sessionID, math.MaxInt64, limit)
	}
	return store.ListEvents(ctx, sessionID, 0, limit)
}

// loadChatAgentTranscriptSession 按 session id 读取 SessionStore 中的历史会话
// （G9 回放数据源）；store 未配置或读取失败都返回 nil，由调用方退回空态。
func loadChatAgentTranscriptSession(session *ChatSession, sessionID string) *runtimechat.Session {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionStore == nil {
		return nil
	}
	stored, err := session.LocalRuntimeHost.SessionStore.Load(context.Background(), sessionID)
	if err != nil || stored == nil {
		return nil
	}
	return stored
}

// chatAgentTranscriptMessageLines 把 canonical messages 渲染成回放行（带 role
// 前缀，与事件流的正文直出区分）；只有 tool_calls 的消息渲染调用名，避免回放
// 出现空行。
func chatAgentTranscriptMessageLines(stored *runtimechat.Session) []string {
	if stored == nil {
		return nil
	}
	messages := stored.GetMessages()
	lines := make([]string, 0, len(messages))
	for _, message := range messages {
		role := strings.TrimSpace(message.Role)
		if role == "" {
			role = "unknown"
		}
		content := strings.TrimRight(message.Content, "\r\n")
		if strings.TrimSpace(content) == "" {
			for _, call := range message.ToolCalls {
				if name := strings.TrimSpace(call.Name); name != "" {
					lines = append(lines, fmt.Sprintf("  [%s] tool_call %s", role, name))
				}
			}
			continue
		}
		for index, row := range strings.Split(content, "\n") {
			row = strings.TrimRight(row, "\r")
			if index == 0 {
				lines = append(lines, fmt.Sprintf("  [%s] %s", role, row))
				continue
			}
			lines = append(lines, "    "+row)
		}
	}
	return lines
}

func chatAgentTranscriptHeaderLines(agent toolbroker.AgentStatusResult, sessionID string, eventCount, visibleRows, omittedRows int, source string, followActive, ended bool) []string {
	path := firstNonEmptyChatValue(agent.Path, sessionID, agent.ID)
	status := firstNonEmptyChatValue(agent.Status, "unknown")
	if strings.TrimSpace(source) == "" {
		source = chatAgentTranscriptSourceEvents
	}
	header := fmt.Sprintf("Agent Transcript: %s", path)
	detail := fmt.Sprintf("  session=%s status=%s source=%s events=%d rows=%d readonly=true", sessionID, status, source, eventCount, visibleRows)
	if followActive {
		detail += " follow=live"
	}
	if ended {
		detail += " ended=true"
	}
	if omittedRows > 0 {
		detail += fmt.Sprintf(" truncated_rows=%d", omittedRows)
	}
	if agent.AgentType != "" {
		detail += " type=" + agent.AgentType
	}
	if agent.ParentSessionID != "" {
		detail += " parent=" + agent.ParentSessionID
	}
	// P2 导出：复用 /export <session-id> 既有能力，给出可复制提示。
	return []string{header, detail, fmt.Sprintf("  [hint] export: /export %s", sessionID)}
}

// chatAgentTranscriptEventLines 把单条子会话事件渲染成 transcript 行：
// assistant_message 正文直出、reasoning 带 [reasoning] 前缀，工具与
// 控制面事件复用父 timeline 的紧凑渲染（保持与主视图一致的阅读习惯）。
func chatAgentTranscriptEventLines(event runtimeevents.Event) []string {
	switch event.Type {
	case runtimechat.EventAssistantMessage:
		text := strings.TrimSpace(payloadStringValue(event.Payload["content"]))
		if text == "" {
			return nil
		}
		return strings.Split(text, "\n")
	case runtimechat.EventAssistantReasoning, "assistant.reasoning":
		block := reasoningBlockFromRuntimeEvent(event)
		if block == nil {
			return nil
		}
		text := strings.TrimSpace(firstNonEmptyChatValue(block.Content, block.Summary))
		if text == "" {
			return nil
		}
		lines := []string{"[reasoning]"}
		for _, line := range strings.Split(text, "\n") {
			lines = append(lines, "  "+line)
		}
		return lines
	case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
		status := "ended"
		if !payloadBoolValue(event.Payload, "success") {
			status = "ended (failed)"
		}
		return []string{"[session] " + status}
	}
	if !isChatAgentTranscriptTimelineEvent(event.Type) {
		return nil
	}
	rendered := renderChatRuntimeTimelineEvent(event)
	if strings.TrimSpace(rendered.Line) == "" {
		return nil
	}
	return strings.Split(rendered.Line, "\n")
}

// isChatAgentTranscriptTimelineEvent 限定可复用 timeline 渲染的事件类型，
// 避免把 llm.request.*、input.queue.* 一类噪声带进子会话视图。
func isChatAgentTranscriptTimelineEvent(eventType string) bool {
	switch eventType {
	case runtimechat.EventToolStarted, "tool.requested",
		runtimechat.EventToolFinished, "tool.completed", "tool.failed",
		"tool.cancelled", "tool.canceled", "tool.denied", "tool.progress":
		return true
	}
	return isTeamLifecycleRuntimeEvent(eventType) ||
		eventType == runtimechat.EventMailboxReceived ||
		eventType == runtimechat.EventApprovalRequested ||
		eventType == runtimechat.EventQuestionAsked ||
		strings.HasPrefix(eventType, "subagent.")
}

func showChatAgentTranscriptPopup(session *ChatSession, lines []string) {
	if session == nil || session.Surface == nil || !session.Surface.Enabled() {
		return
	}
	session.Surface.ShowPopupPreserveCursorForOwner(lines, chatAgentTranscriptPopupOwner)
}

func clearChatAgentTranscriptPopup(session *ChatSession) {
	if session == nil || session.Surface == nil {
		stopChatAgentTranscriptFollow(session)
		return
	}
	stopChatAgentTranscriptFollow(session)
	session.Surface.ClearPopupForOwnerPreserveCursor(chatAgentTranscriptPopupOwner)
	if session.Interaction != nil {
		session.Interaction.RefreshStatus("")
	}
}

// chatAgentTranscriptFollowState 保存 popup 视图的 live 订阅状态（G4）。
// 作为 ChatSession 上的指针字段：互斥量随指针传递，避免 ChatSession 值拷贝
// 复制锁；订阅回调只触碰 state，不回写 ChatSession 字段。
type chatAgentTranscriptFollowState struct {
	mu        sync.Mutex
	sessionID string
	target    string
	unsub     func()
	view      *chatAgentTranscriptView
}

// startChatAgentTranscriptFollow 为 popup 视图订阅目标子会话事件（eventType=""
// 订阅全部后按会话身份过滤，与 runtime/stream 同款口径）。重复调用替换旧订阅；
// 返回是否建立了订阅。
func startChatAgentTranscriptFollow(session *ChatSession, view *chatAgentTranscriptView) bool {
	if session == nil || view == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventBus == nil {
		return false
	}
	stopChatAgentTranscriptFollow(session)
	state := &chatAgentTranscriptFollowState{sessionID: view.SessionID, target: view.Target, view: view}
	handler := func(event runtimeevents.Event) {
		terminal := chatAgentTranscriptTerminalEventFor(event, state.sessionID)
		if !terminal && !chatAgentTranscriptEventBelongsTo(event, state.sessionID) {
			return
		}
		if !terminal && !runtimeevents.IsPersistedEventType(event.Type) {
			// 视图数据源是 EventStore：未落盘事件不进入视图（与快照路径同源）。
			return
		}
		refreshChatAgentTranscriptFollowPopup(session, state, event, terminal)
		if terminal {
			// 终态收口：渲染最终快照后解除订阅；父/兄弟会话终态不会走到这里。
			stopChatAgentTranscriptFollow(session)
		}
	}
	unsub := session.LocalRuntimeHost.EventBus.SubscribeCancelable("", handler)
	if unsub == nil {
		return false
	}
	state.mu.Lock()
	state.unsub = unsub
	state.mu.Unlock()
	session.agentTranscriptFollow = state
	return true
}

// refreshChatAgentTranscriptFollowPopup 把一条事件增量合并进视图并重绘 popup。
func refreshChatAgentTranscriptFollowPopup(session *ChatSession, state *chatAgentTranscriptFollowState, event runtimeevents.Event, terminal bool) {
	if state == nil || state.view == nil {
		return
	}
	state.mu.Lock()
	state.view.Events = append(state.view.Events, event)
	state.view.Body = append(state.view.Body, chatAgentTranscriptEventLines(event)...)
	if terminal {
		state.view.Ended = true
		state.view.FollowActive = false
	}
	lines := state.view.Lines()
	state.mu.Unlock()
	showChatAgentTranscriptPopup(session, lines)
}

// stopChatAgentTranscriptFollow 解除订阅（/agent close、切换 target、终态收口）。
func stopChatAgentTranscriptFollow(session *ChatSession) {
	if session == nil {
		return
	}
	state := session.agentTranscriptFollow
	if state == nil {
		return
	}
	session.agentTranscriptFollow = nil
	state.mu.Lock()
	unsub := state.unsub
	state.unsub = nil
	state.mu.Unlock()
	if unsub != nil {
		unsub()
	}
}

// chatAgentTranscriptFollowActive 报告视图是否仍在 live 订阅（测试与调试用）。
func chatAgentTranscriptFollowActive(session *ChatSession, sessionID string) bool {
	if session == nil {
		return false
	}
	state := session.agentTranscriptFollow
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.unsub == nil {
		return false
	}
	return sessionID == "" || state.sessionID == sessionID
}

// chatAgentTranscriptEventBelongsTo 判定事件是否属于目标子会话：与
// isForeignSessionTerminalEvent 同一身份来源（SessionID 优先、payload.session_id
// 兜底）。视图按会话隔离：身份缺失的事件一律不入视图，宁可漏也不串。
func chatAgentTranscriptEventBelongsTo(event runtimeevents.Event, sessionID string) bool {
	if strings.TrimSpace(sessionID) == "" {
		return false
	}
	eventSessionID := firstNonEmptyChatValue(strings.TrimSpace(event.SessionID), payloadStringValue(event.Payload["session_id"]))
	if eventSessionID == "" {
		return false
	}
	return eventSessionID == sessionID
}

// chatAgentTranscriptTerminalEventFor 复用 isForeignSessionTerminalEvent 的
// 身份口径做反向判定：只有确认属于目标子会话的 session_end/interrupted 才
// 收口视图；父/兄弟会话终态与身份缺失事件都不影响视图。
func chatAgentTranscriptTerminalEventFor(event runtimeevents.Event, sessionID string) bool {
	switch event.Type {
	case runtimechat.EventSessionEnd, runtimechat.EventSessionInterrupted:
	default:
		return false
	}
	return chatAgentTranscriptEventBelongsTo(event, sessionID)
}

// chatAgentTranscriptFollowOnceLines 是 legacy console 的 follow：等待目标
// 子会话的下一次事件（或超时）后返回一次增量刷新，语义与 /collab follow 的
// "等待 mailbox 更新后刷新一次"一致。
func chatAgentTranscriptFollowOnceLines(session *ChatSession, view *chatAgentTranscriptView, timeout time.Duration) []string {
	if view == nil {
		return []string{"  follow=unavailable"}
	}
	if view.Ended {
		return []string{"  follow=ended（子会话已结束）"}
	}
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.EventBus == nil {
		return []string{"  follow=unavailable（当前会话没有可用的 runtime event bus）"}
	}
	if timeout <= 0 {
		timeout = chatAgentTranscriptFollowDefaultTimeout
	}
	updates := make(chan runtimeevents.Event, 1)
	unsub := session.LocalRuntimeHost.EventBus.SubscribeCancelable("", func(event runtimeevents.Event) {
		if !chatAgentTranscriptEventBelongsTo(event, view.SessionID) {
			return
		}
		select {
		case updates <- event:
		default:
		}
	})
	if unsub != nil {
		defer unsub()
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case event := <-updates:
		lines := []string{"  Follow Update:"}
		rendered := chatAgentTranscriptEventLines(event)
		if len(rendered) == 0 {
			rendered = []string{"  <event: " + event.Type + ">"}
		}
		lines = append(lines, rendered...)
		if chatAgentTranscriptTerminalEventFor(event, view.SessionID) {
			lines = append(lines, "  follow=ended")
		}
		return lines
	case <-timer.C:
		return []string{"  follow=timeout"}
	}
}

// handleChatAgentApprovalCommand 实现 /agents approve|deny <target> <request_id>：
// 直接调用 localActorRegistry.ResolveApproval —— 与 resolve_agent_approval 工具
// 同一条 supervision 审批链（§5-G5「复用 supervision 审批链」）。视图只在正文
// 渲染该命令提示，不私建审批通道（§4.4 纪律 3）。
func handleChatAgentApprovalCommand(session *ChatSession, argument string, allow bool) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.ActorRegistry == nil {
		return "", fmt.Errorf("当前会话没有可用的 agent registry")
	}
	target, requestID := parseChatAgentApprovalArgs(argument)
	verb := "deny"
	if allow {
		verb = "approve"
	}
	if target == "" || requestID == "" {
		return "", fmt.Errorf("用法: /agents %s <target> <request_id>（request_id 见视图 [action] 行或 /debug supervision list）", verb)
	}
	result, err := session.LocalRuntimeHost.ActorRegistry.ResolveApproval(context.Background(), toolbroker.ResolveAgentApprovalArgs{
		ID:        target,
		RequestID: requestID,
		Allow:     allow,
	})
	if err != nil {
		return "", err
	}
	decision := "denied"
	if allow {
		decision = "allowed"
	}
	lines := []string{fmt.Sprintf("Agent Approval: %s", decision)}
	lines = append(lines, fmt.Sprintf("  session=%s request_id=%s resolved=%t resumed=%t resolution=%s",
		result.SessionID, result.RequestID, result.Resolved, result.Resumed, string(result.Resolution)))
	return strings.Join(lines, "\n"), nil
}

// parseChatAgentApprovalArgs 解析 approve/deny 参数：跳过动词，首个普通 token 是
// target，支持 request_id=<id>（或 request=<id>）显式键，也接受第二个位置 token。
func parseChatAgentApprovalArgs(argument string) (string, string) {
	var target, requestID string
	for _, field := range splitChatCommandFields(argument) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		switch strings.ToLower(field) {
		case "approve", "approved", "allow", "allowed", "yes", "deny", "denied", "reject", "rejected", "no":
			continue
		}
		if value, ok := chatAgentCommandValueToken(field, "request_id", "request"); ok {
			requestID = value
			continue
		}
		if target == "" {
			target = field
			continue
		}
		if requestID == "" {
			requestID = field
		}
	}
	return target, requestID
}

// handleChatAgentAnswerCommand 实现 /agents answer <target> <question_id> [text]：
// 复用 actor.AnswerQuestion（与主会话问题回答同一条链），target 经 registry 的
// resolveLocalAgentTargetSessionID 映射（G7 单一映射）。
func handleChatAgentAnswerCommand(session *ChatSession, argument string) (string, error) {
	if session == nil {
		return "", fmt.Errorf("当前没有活动会话")
	}
	if session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return "", fmt.Errorf("当前会话没有可用的 session hub")
	}
	target, questionID, answer := parseChatAgentAnswerArgs(argument)
	if target == "" || questionID == "" {
		return "", fmt.Errorf("用法: /agents answer <target> <question_id> [text]（question_id 见视图 [action] 行）")
	}
	sessionID := target
	if registry := session.LocalRuntimeHost.ActorRegistry; registry != nil {
		resolved, err := registry.resolveLocalAgentTargetSessionID(context.Background(), target)
		if err != nil {
			return "", err
		}
		sessionID = resolved
	}
	actor, err := session.LocalRuntimeHost.SessionHub.GetOrCreate(sessionID)
	if err != nil {
		return "", err
	}
	if err := actor.AnswerQuestion(context.Background(), questionID, answer); err != nil {
		return "", err
	}
	return fmt.Sprintf("Agent Answer: question=%s session=%s answered=true", questionID, sessionID), nil
}

// parseChatAgentAnswerArgs 解析 answer 参数：跳过动词，依次取 target、question_id
// （支持 question_id=<id> 显式键），其余 token 拼为 answer 文本。
func parseChatAgentAnswerArgs(argument string) (string, string, string) {
	var target, questionID, answer string
	for _, field := range splitChatCommandFields(argument) {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		switch strings.ToLower(field) {
		case "answer", "answers", "reply":
			continue
		}
		if value, ok := chatAgentCommandValueToken(field, "question_id", "question"); ok {
			questionID = value
			continue
		}
		switch {
		case target == "":
			target = field
		case questionID == "":
			questionID = field
		case answer == "":
			answer = field
		default:
			answer += " " + field
		}
	}
	return target, questionID, answer
}

// chatAgentCommandValueToken 解析 key=value 形式的 token（键不区分大小写）。
func chatAgentCommandValueToken(field string, keys ...string) (string, bool) {
	lower := strings.ToLower(field)
	for _, key := range keys {
		prefix := key + "="
		if strings.HasPrefix(lower, prefix) {
			return strings.TrimSpace(field[len(prefix):]), true
		}
	}
	return "", false
}

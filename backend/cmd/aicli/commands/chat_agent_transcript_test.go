package commands

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agentcontrol"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// appendChatTranscriptTestEvent 把一条事件写进 harness 的 EventStore；子会话
// transcript 视图的数据源与前端 SubagentSessionDialog 同源（事件流）。
func appendChatTranscriptTestEvent(t *testing.T, host *localChatRuntimeHost, sessionID, eventType string, payload map[string]interface{}) {
	t.Helper()
	require.NotNil(t, host)
	require.NotNil(t, host.EventStore)
	_, err := host.EventStore.AppendEvent(context.Background(), runtimeevents.Event{
		Type:      eventType,
		SessionID: sessionID,
		Payload:   payload,
	})
	require.NoError(t, err)
}

// TestAgentTranscriptView_SeparatesChildTimelineFromParent pins the P0 contract
// from docs/plan/subagent-transcript-separation-analysis-20260919.md §4.4:
//   - 主界面只显示主 agent 的 transcript；子会话事件不进入父 timeline（父侧由
//     chat_runtime_events.go 的 isForeignSessionContentEvent 守卫保证）。
//   - 子 agent 内容通过只读视图 /agent <target> 与 /agents view <target> 查看，
//     两个入口必须等价（Q3 双入口裁定）。
//   - 视图只读：不把子会话载入父 Session.History，也不改写父会话事件流。
func TestAgentTranscriptView_SeparatesChildTimelineFromParent(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	appendChatTranscriptTestEvent(t, host, "held-child-session", runtimechat.EventAssistantMessage,
		map[string]interface{}{"content": "child-only answer"})
	appendChatTranscriptTestEvent(t, host, "held-child-session", runtimechat.EventAssistantReasoning,
		map[string]interface{}{"reasoning": map[string]interface{}{"summary": "child reasoning"}})
	appendChatTranscriptTestEvent(t, host, "held-child-session", runtimechat.EventToolStarted,
		map[string]interface{}{"tool_name": "ls", "tool_call_id": "call-child-1"})
	appendChatTranscriptTestEvent(t, host, "held-child-session", runtimechat.EventSessionEnd,
		map[string]interface{}{"success": true})
	appendChatTranscriptTestEvent(t, host, rootSession.ID, runtimechat.EventAssistantMessage,
		map[string]interface{}{"content": "parent-only answer"})

	result := executeStructuredAgentTranscriptCommand(session, "/agent /root/held-child")
	require.Equal(t, CommandContinue, result.Action)
	require.NotEmpty(t, result.Blocks)
	text := renderDocumentText(result.Blocks[0].Document)

	require.Contains(t, text, "Agent Transcript: /root/held-child")
	require.Contains(t, text, "session=held-child-session")
	require.Contains(t, text, "source=events")
	require.Contains(t, text, "readonly=true")
	require.Contains(t, text, "child-only answer")
	require.Contains(t, text, "child reasoning")
	require.Contains(t, text, "ls")
	require.Contains(t, text, "[session] ended")
	require.NotContains(t, text, "parent-only answer", "child view must never contain parent transcript content")

	// /agents view <target> 是 /agent <target> 的等价入口。
	viewResult := executeStructuredAgentsCommand(session, "/agents view /root/held-child")
	require.Equal(t, CommandContinue, viewResult.Action)
	require.Equal(t, text, renderDocumentText(viewResult.Blocks[0].Document))

	// limit=N 取尾部窗口：只保留最近一条事件。
	limited := executeStructuredAgentTranscriptCommand(session, "/agent /root/held-child limit=1")
	limitedText := renderDocumentText(limited.Blocks[0].Document)
	require.Contains(t, limitedText, "events=1")
	require.Contains(t, limitedText, "[session] ended")
	require.NotContains(t, limitedText, "child-only answer")

	// 只读：子会话不得被载入父 Session.History，父会话事件流保持原样。
	require.Empty(t, session.RuntimeSession.History)
	parentEvents, err := host.EventStore.ListEvents(context.Background(), rootSession.ID, 0, 32)
	require.NoError(t, err)
	require.Len(t, parentEvents, 1)
	require.Equal(t, "parent-only answer", payloadStringValue(parentEvents[0].Payload["content"]))
}

// TestAgentTranscriptView_EmptyUnknownAndClose 覆盖视图的边界：无事件的子会话
// 给出显式空态；未知 target 报错且不伪装成 transcript；/agent close 关闭视图。
func TestAgentTranscriptView_EmptyUnknownAndClose(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)

	// 无持久化事件的活跃子会话：必须显式空态而不是空白或父内容兜底。
	_, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         "quiet-child-session",
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       "quiet-child-session",
		AgentPath:       "/root/quiet-child",
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	appendChatTranscriptTestEvent(t, host, rootSession.ID, runtimechat.EventAssistantMessage,
		map[string]interface{}{"content": "parent-content-should-not-leak"})

	empty := executeStructuredAgentTranscriptCommand(session, "/agents view /root/quiet-child")
	emptyText := renderDocumentText(empty.Blocks[0].Document)
	require.Contains(t, emptyText, "session=quiet-child-session")
	require.Contains(t, emptyText, "parent="+rootSession.ID)
	require.Contains(t, emptyText, "<empty:")
	require.NotContains(t, emptyText, "parent-content-should-not-leak")

	missing := executeStructuredAgentTranscriptCommand(session, "/agent /root/does-not-exist")
	missingText := renderDocumentText(missing.Blocks[0].Document)
	require.Contains(t, missingText, "错误:")
	require.Contains(t, missingText, "/root/does-not-exist")
	require.NotContains(t, missingText, "Agent Transcript:")

	closed := executeStructuredAgentTranscriptCommand(session, "/agent close")
	require.True(t, strings.Contains(renderDocumentText(closed.Blocks[0].Document), "已关闭"))
}

// TestAgentTranscriptView_ReplaysStoredMessagesWhenEventStreamIsEmpty 覆盖 G9：
// 事件流没有覆盖的历史子会话回退到 SessionStore canonical messages（Q2 裁定：
// 事件流为主、messages 仅作历史回放补充），并给出可复制的 /export 提示。
func TestAgentTranscriptView_ReplaysStoredMessagesWhenEventStreamIsEmpty(t *testing.T) {
	host, rootSession, agentStore := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)
	require.NotNil(t, host.SessionStore)

	child := runtimechat.NewSession("user-cli-replay")
	child.ID = "replay-child-session"
	// 会话上下文与下方 durable row 必须描述同一绑定：G7 的
	// resolveLocalAgentTargetSessionID 会先 materialize 本地 agent registry，
	// 身份不一致的行会被 sweep 成 stale（与 newLocalQuotaHarness 的既有口径一致）。
	child.SetContext(toolbroker.AgentSessionContextParentSessionID, rootSession.ID)
	child.SetContext(toolbroker.AgentSessionContextRootSessionID, rootSession.ID)
	child.SetContext(toolbroker.AgentSessionContextPath, "/root/replay-child")
	child.History = []types.Message{
		{Role: "user", Content: "历史问题"},
		{Role: "assistant", Content: "历史回答\n第二行"},
		{Role: "assistant", ToolCalls: []types.ToolCall{{ID: "call-replay-1", Name: "read_file"}}},
	}
	require.NoError(t, host.SessionStore.Save(context.Background(), child))

	_, err := agentStore.UpsertAgentControlAgent(context.Background(), agentcontrol.AgentRecord{
		AgentID:         "replay-child-session",
		RootSessionID:   rootSession.ID,
		ParentAgentID:   "root:" + rootSession.ID,
		ParentSessionID: rootSession.ID,
		SessionID:       "replay-child-session",
		AgentPath:       "/root/replay-child",
		Depth:           1,
		AgentType:       agentcontrol.AgentTypeChild,
		Workflow:        agentcontrol.WorkflowSpawnAgent,
		Status:          agentcontrol.AgentStatusActive,
	})
	require.NoError(t, err)

	// 父会话事件流有内容，但不得作为子视图兜底数据源泄漏进来。
	appendChatTranscriptTestEvent(t, host, rootSession.ID, runtimechat.EventAssistantMessage,
		map[string]interface{}{"content": "parent-content-should-not-leak"})

	result := executeStructuredAgentTranscriptCommand(session, "/agents view /root/replay-child")
	require.Equal(t, CommandContinue, result.Action)
	require.NotEmpty(t, result.Blocks)
	text := renderDocumentText(result.Blocks[0].Document)

	require.Contains(t, text, "session=replay-child-session")
	require.Contains(t, text, "source=messages")
	require.Contains(t, text, "ended=true")
	require.Contains(t, text, "[user] 历史问题")
	require.Contains(t, text, "[assistant] 历史回答")
	require.Contains(t, text, "第二行")
	require.Contains(t, text, "[assistant] tool_call read_file")
	require.Contains(t, text, "[hint] export: /export replay-child-session")
	require.NotContains(t, text, "parent-content-should-not-leak")

	// 事件流一旦有覆盖就仍以事件流为准（Q2：事件流优先）。
	appendChatTranscriptTestEvent(t, host, "replay-child-session", runtimechat.EventAssistantMessage,
		map[string]interface{}{"content": "event-sourced answer"})
	eventsText := renderDocumentText(executeStructuredAgentTranscriptCommand(session, "/agents view /root/replay-child").Blocks[0].Document)
	require.Contains(t, eventsText, "source=events")
	require.Contains(t, eventsText, "event-sourced answer")
	require.NotContains(t, eventsText, "历史问题")
}

// TestChatAgentTranscriptCommand_MatchesExactTokenOnly 锁定 /agent 与 /agents 的
// 前缀歧义：单数入口只匹配精确 token，/agents 的任何子命令都不得被它吞掉。
func TestChatAgentTranscriptCommand_MatchesExactTokenOnly(t *testing.T) {
	require.True(t, commandMatches("/agent", "/agent"))
	require.True(t, commandMatches("/agent /root/held-child", "/agent"))
	require.True(t, commandMatches("/agents view /root/held-child", "/agents"))
	require.False(t, commandMatches("/agents", "/agent"))
	require.False(t, commandMatches("/agents view /root/held-child", "/agent"))
}

// TestAgentTranscriptFollow_MergesChildEventsAndStopsOnChildTerminal 锁定 §5-G4：
// popup 视图的 live 订阅按会话身份增量合并事件，子会话终态收口并解除订阅，
// 父/兄弟会话终态不影响子视图（复用 isForeignSessionTerminalEvent 语义）。
func TestAgentTranscriptFollow_MergesChildEventsAndStopsOnChildTerminal(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)
	require.NotNil(t, host.EventBus)

	view := &chatAgentTranscriptView{Target: "/root/held-child", SessionID: "held-child-session"}
	require.True(t, startChatAgentTranscriptFollow(session, view))
	require.True(t, chatAgentTranscriptFollowActive(session, "held-child-session"))

	// 父会话终态不得收口子视图。
	host.EventBus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: rootSession.ID})
	require.True(t, chatAgentTranscriptFollowActive(session, "held-child-session"))
	require.False(t, view.Ended)

	// 目标子会话增量按与快照相同的渲染口径合并进视图。
	refreshChatAgentTranscriptFollowPopup(session, session.agentTranscriptFollow, runtimeevents.Event{
		Type:      runtimechat.EventAssistantMessage,
		SessionID: "held-child-session",
		Payload:   map[string]interface{}{"content": "follow-delta"},
	}, false)
	require.Contains(t, strings.Join(view.Lines(), "\n"), "follow-delta")
	require.True(t, chatAgentTranscriptFollowActive(session, "held-child-session"))
	require.Empty(t, session.RuntimeSession.History, "follow 增量只进子视图，不得改写父会话状态")

	// 子会话终态：渲染最终快照后解除订阅，视图标记 ended。
	host.EventBus.Publish(runtimeevents.Event{Type: runtimechat.EventSessionEnd, SessionID: "held-child-session"})
	require.True(t, view.Ended)
	require.False(t, view.FollowActive)
	require.False(t, chatAgentTranscriptFollowActive(session, ""))
	require.Contains(t, strings.Join(view.Lines(), "\n"), "ended=true")
}

// TestAgentTranscriptView_CapsRowsAtFrontendLimit 锁定 P1 行数上限：正文只保留
// 最近 400 行（与前端 SubagentSessionDialog 的 MAX_VISIBLE_ROWS 一致），并给出
// truncated 标记；标记不计入 400 行正文口径。
func TestAgentTranscriptView_CapsRowsAtFrontendLimit(t *testing.T) {
	rows := make([]string, 0, chatAgentTranscriptMaxVisibleRows+25)
	for index := 0; index < chatAgentTranscriptMaxVisibleRows+25; index++ {
		rows = append(rows, fmt.Sprintf("row-%d", index))
	}
	capped, omitted := capChatAgentTranscriptRows(rows, chatAgentTranscriptMaxVisibleRows)
	require.Equal(t, 25, omitted)
	require.Len(t, capped, chatAgentTranscriptMaxVisibleRows+1) // 标记 + 400 行正文
	require.Contains(t, capped[0], "truncated")
	require.Equal(t, "row-25", capped[1])
	require.Equal(t, fmt.Sprintf("row-%d", chatAgentTranscriptMaxVisibleRows+24), capped[len(capped)-1])

	view := &chatAgentTranscriptView{
		Agent:     toolbroker.AgentStatusResult{Path: "/root/child-cap", Status: "running"},
		SessionID: "child-cap",
		Body:      rows,
	}
	lines := view.Lines()
	require.Contains(t, lines[1], "rows=400")
	require.Contains(t, lines[1], "truncated_rows=25")
}

// TestAgentTranscriptView_PendingActionHintsCloseLoop 锁定 §5-G5：pending 审批/
// 问题在视图正文渲染既有控制面命令提示（/agents approve|deny|answer），视图不
// 私建通道；命令参数解析与缺参用法提示同测。
func TestAgentTranscriptView_PendingActionHintsCloseLoop(t *testing.T) {
	agent := toolbroker.AgentStatusResult{
		Path:            "/root/child-g5",
		SessionID:       "child-g5",
		PendingApproval: true,
		PendingQuestion: true,
	}
	events := []runtimeevents.Event{
		{Type: runtimechat.EventApprovalRequested, SessionID: "child-g5", Payload: map[string]interface{}{"request_id": "req-g5"}},
		{Type: runtimechat.EventQuestionAsked, SessionID: "child-g5", Payload: map[string]interface{}{"question_id": "q-g5"}},
	}
	text := strings.Join(chatAgentTranscriptActionLines(agent, events), "\n")
	require.Contains(t, text, "/agents approve /root/child-g5 req-g5")
	require.Contains(t, text, "/agents deny /root/child-g5 req-g5")
	require.Contains(t, text, "/agents answer /root/child-g5 q-g5")

	target, requestID := parseChatAgentApprovalArgs("approve /root/child-g5 request_id=req-g5")
	require.Equal(t, "/root/child-g5", target)
	require.Equal(t, "req-g5", requestID)
	answerTarget, questionID, answer := parseChatAgentAnswerArgs("answer /root/child-g5 q-g5 use staging")
	require.Equal(t, "/root/child-g5", answerTarget)
	require.Equal(t, "q-g5", questionID)
	require.Equal(t, "use staging", answer)

	// 缺参时给出用法而不是静默成功；dispatcher 必须识别 approve/deny/answer 动词。
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)
	_, err := handleChatAgentApprovalCommand(session, "approve", true)
	require.ErrorContains(t, err, "用法")
	usage := executeStructuredAgentsCommand(session, "/agents approve")
	require.Contains(t, renderDocumentText(usage.Blocks[0].Document), "用法")
}

// TestAgentTranscriptView_TargetSessionMappingConsistent 锁定 §5-G7：panel
// picker / view / send-approve-answer（registry）三处对同一 target 解析出同一个
// SessionID。
func TestAgentTranscriptView_TargetSessionMappingConsistent(t *testing.T) {
	host, rootSession, _ := newLocalQuotaHarness(t, 1, 0)
	session := chatAgentCleanupHarnessSession(host, rootSession)
	const (
		childPath      = "/root/held-child"
		childSessionID = "held-child-session"
	)

	registrySessionID, err := host.ActorRegistry.resolveLocalAgentTargetSessionID(context.Background(), childPath)
	require.NoError(t, err)
	require.Equal(t, childSessionID, registrySessionID)

	view, err := buildChatAgentTranscriptView(session, chatAgentTranscriptOptions{Target: childPath})
	require.NoError(t, err)
	require.Equal(t, registrySessionID, view.SessionID,
		"view 必须与 send/approve/answer 共用同一 target→SessionID 映射")

	items, err := chatAgentPickerItems(session)
	require.NoError(t, err)
	found := false
	for _, item := range items {
		if firstNonEmptyChatValue(item.Path, item.SessionID, item.ID) != childPath {
			continue
		}
		found = true
		require.Equal(t, registrySessionID, firstNonEmptyChatValue(item.SessionID, item.ID),
			"panel picker 与 registry 映射必须一致")
	}
	require.True(t, found, "panel picker 必须包含目标子 agent")
}

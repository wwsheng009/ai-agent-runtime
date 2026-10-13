// runtime-server 连接器命令闭环（FR-7）与注意力出站（FR-8）的宿主接线测试：
// 用假命令面验证 /approve /deny /answer 的参数解释与回执；用真实事件总线 +
// 真实 connectors.Service（内存 Store + 记录型 Provider）验证「事件 → 去重 →
// NotifyAttention → 绑定出站」，以及无绑定不推、退订后不推。
package main

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/api/runtimeapi"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
	"github.com/wwsheng009/ai-agent-runtime/internal/connectors/api"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/runtimeobserve"
)

// ── 测试替身 ───────────────────────────────────────────────────────────────

type runtimeConnectorDecideCall struct {
	sessionID string
	requestID string
	allow     bool
}

type runtimeConnectorAnswerCall struct {
	sessionID  string
	questionID string
	answer     string
}

// fakeRuntimeConnectorHost 实现 runtimeConnectorCommandHost：命令面全部本地记录，
// 不依赖完整 runtime 栈（与真 handler 的语义由 runtimeapi 侧单测/既有用例覆盖）。
type fakeRuntimeConnectorHost struct {
	mu              sync.Mutex
	pendingApproval string
	pendingQuestion string
	decideOutcome   runtimeapi.ConnectorCommandOutcome
	decideCalls     []runtimeConnectorDecideCall
	answerCalls     []runtimeConnectorAnswerCall
	parked          bool
}

func (h *fakeRuntimeConnectorHost) SubmitConnectorPrompt(context.Context, runtimeapi.ConnectorTurnRequest) (*runtimeapi.ConnectorTurnResult, error) {
	return &runtimeapi.ConnectorTurnResult{}, nil
}

func (h *fakeRuntimeConnectorHost) ConnectorPendingApprovalID(context.Context, string) (string, error) {
	return h.pendingApproval, nil
}

func (h *fakeRuntimeConnectorHost) ConnectorPendingQuestionID(context.Context, string) (string, error) {
	return h.pendingQuestion, nil
}

func (h *fakeRuntimeConnectorHost) ConnectorDecideApproval(_ context.Context, sessionID, requestID string, allow bool) (runtimeapi.ConnectorCommandOutcome, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.decideCalls = append(h.decideCalls, runtimeConnectorDecideCall{sessionID: sessionID, requestID: requestID, allow: allow})
	outcome := h.decideOutcome
	if outcome.SessionID == "" {
		outcome.SessionID = sessionID
	}
	if outcome.RequestID == "" {
		outcome.RequestID = requestID
	}
	outcome.Allowed = allow
	return outcome, nil
}

func (h *fakeRuntimeConnectorHost) ConnectorAnswerQuestion(_ context.Context, sessionID, questionID, answer string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.answerCalls = append(h.answerCalls, runtimeConnectorAnswerCall{sessionID: sessionID, questionID: questionID, answer: answer})
	return nil
}

func (h *fakeRuntimeConnectorHost) ConnectorSessionParked(string) bool { return h.parked }

func (h *fakeRuntimeConnectorHost) decideCallsSnapshot() []runtimeConnectorDecideCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]runtimeConnectorDecideCall(nil), h.decideCalls...)
}

func (h *fakeRuntimeConnectorHost) answerCallsSnapshot() []runtimeConnectorAnswerCall {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]runtimeConnectorAnswerCall(nil), h.answerCalls...)
}

// recordingProvider 记录 Send 调用（连接器出站断言用）。
type recordingProvider struct {
	name string

	mu    sync.Mutex
	sends []recordedConnectorSend
}

type recordedConnectorSend struct {
	chat  string
	texts []string
}

func (p *recordingProvider) Name() string { return p.name }

func (p *recordingProvider) Schema() api.ProviderSchema { return api.ProviderSchema{} }

func (p *recordingProvider) Activate(context.Context, connectors.Config) (connectors.Activation, error) {
	return connectors.Activation{}, nil
}

func (p *recordingProvider) Deactivate(context.Context, connectors.Config) error { return nil }

func (p *recordingProvider) Send(_ context.Context, _ connectors.Config, chat string, msgs []connectors.OutboundMessage) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	texts := make([]string, 0, len(msgs))
	for _, msg := range msgs {
		texts = append(texts, msg.Text)
	}
	p.sends = append(p.sends, recordedConnectorSend{chat: chat, texts: texts})
	return nil
}

func (p *recordingProvider) snapshot() []recordedConnectorSend {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedConnectorSend(nil), p.sends...)
}

// stubConnectorBackend 满足 connectors.Backend（出站测试不经过入站路径）。
type stubConnectorBackend struct{}

func (stubConnectorBackend) Name() string { return "stub" }

func (stubConnectorBackend) Handle(context.Context, connectors.BackendRequest, connectors.ReplySink) (connectors.BackendResult, error) {
	return connectors.BackendResult{}, nil
}

func newRuntimeConnectorCommandBackend(host *fakeRuntimeConnectorHost) runtimeConnectorBackend {
	return runtimeConnectorBackend{handler: host, logf: func(string, ...any) {}}
}

// ── /approve /deny 参数解释 ────────────────────────────────────────────────

func TestRuntimeConnectorDecideApprovalUsesCurrentPendingTarget(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingApproval: "apr-1", decideOutcome: runtimeapi.ConnectorCommandOutcome{Resumed: true}}
	backend := newRuntimeConnectorCommandBackend(host)

	// 无参 = 当前挂起项。
	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Len(t, result.Reply, 1)
	require.Contains(t, result.Reply[0].Text, "已批准：apr-1")
	require.Contains(t, result.Reply[0].Text, "已恢复运行")
	calls := host.decideCallsSnapshot()
	require.Equal(t, []runtimeConnectorDecideCall{{sessionID: "sess-1", requestID: "apr-1", allow: true}}, calls)

	// 带 request_id 且与当前挂起项一致：同样命中。
	result, err = backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"apr-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Equal(t, 2, len(host.decideCallsSnapshot()))
}

func TestRuntimeConnectorDecideApprovalDenyPassesAllowFalse(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingApproval: "apr-1"}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "deny",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"apr-1"},
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "已拒绝：apr-1")
	calls := host.decideCallsSnapshot()
	require.Len(t, calls, 1)
	require.False(t, calls[0].allow)
}

func TestRuntimeConnectorDecideApprovalWithoutPendingReturnsNotHandled(t *testing.T) {
	host := &fakeRuntimeConnectorHost{}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Len(t, result.Reply, 1)
	require.Contains(t, result.Reply[0].Text, "没有待审批项")
	require.Empty(t, host.decideCallsSnapshot(), "无挂起项不得调用 actor 审批入口")
}

func TestRuntimeConnectorDecideApprovalMismatchedRequestIDReturnsNotHandled(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingApproval: "apr-1"}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{
		Command: "approve",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"apr-2"},
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "不匹配")
	require.Contains(t, result.Reply[0].Text, "apr-1")
	require.Empty(t, host.decideCallsSnapshot(), "request_id 不匹配不得触碰 actor")
}

func TestRuntimeConnectorDecideApprovalUnboundSessionReturnsNotHandled(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingApproval: "apr-1"}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.DecideApproval(context.Background(), connectors.CommandRequest{Command: "approve"})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "尚未绑定")
	require.Empty(t, host.decideCallsSnapshot())
}

// ── /answer 序号与文本解析 ─────────────────────────────────────────────────

func TestRuntimeConnectorAnswerQuestionSequenceAndText(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingQuestion: "q-1"}
	backend := newRuntimeConnectorCommandBackend(host)

	// 序号 1 → 当前挂起 question id；回答保留内部空格。
	result, err := backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"1", "hello", "world"},
		Text:    "1 hello world",
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "已提交回答：q-1")
	require.Equal(t, []runtimeConnectorAnswerCall{{sessionID: "sess-1", questionID: "q-1", answer: "hello world"}}, host.answerCallsSnapshot())

	// question_id 直指 + 多空格原文。
	result, err = backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"q-1", "多", "空格"},
		Text:    "q-1 多  空格",
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	calls := host.answerCallsSnapshot()
	require.Len(t, calls, 2)
	require.Equal(t, "多  空格", calls[1].answer)

	// 空回答：仍然提交（与 REST answer_question 允许空回答一致）。
	result, err = backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"1"},
		Text:    "1",
	})
	require.NoError(t, err)
	require.True(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "空回答")
}

func TestRuntimeConnectorAnswerQuestionWithoutPendingReturnsNotHandled(t *testing.T) {
	host := &fakeRuntimeConnectorHost{}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"1", "x"},
		Text:    "1 x",
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "没有待回答的问题")
	require.Empty(t, host.answerCallsSnapshot())
}

func TestRuntimeConnectorAnswerQuestionMismatchedTargetReturnsNotHandled(t *testing.T) {
	host := &fakeRuntimeConnectorHost{pendingQuestion: "q-1"}
	backend := newRuntimeConnectorCommandBackend(host)

	result, err := backend.AnswerQuestion(context.Background(), connectors.CommandRequest{
		Command: "answer",
		Session: connectors.SessionRef{SessionID: "sess-1"},
		Args:    []string{"2", "x"},
		Text:    "2 x",
	})
	require.NoError(t, err)
	require.False(t, result.Handled)
	require.Contains(t, result.Reply[0].Text, "不匹配")
	require.Empty(t, host.answerCallsSnapshot())
}

// ── 事件映射与去重 ─────────────────────────────────────────────────────────

// TestRuntimeHandlerConnectorPendingLookupsReadDurableState 验证真 handler 的
// 挂起项解析：读 durable runtime state（与 GET /sessions/{id}/runtime 同源），
// 未知会话返回空串而不是报错（命令层据此回 Handled=false）。
func TestRuntimeHandlerConnectorPendingLookupsReadDurableState(t *testing.T) {
	handler := runtimeapi.NewHandler(nil, nil, nil)
	store := handler.SessionDeps("sess-1").RuntimeStore
	require.NotNil(t, store, "runtime handler 必须带运行态存储")
	require.NoError(t, store.SaveState(context.Background(), &runtimechat.RuntimeState{
		SessionID:       "sess-1",
		PendingApproval: &runtimechat.ApprovalRequest{ID: "apr-1", ToolName: "shell_exec"},
		PendingQuestion: &runtimechat.UserQuestionRequest{ID: "q-1", Prompt: "which env?"},
	}))

	approvalID, err := handler.ConnectorPendingApprovalID(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, "apr-1", approvalID)
	questionID, err := handler.ConnectorPendingQuestionID(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, "q-1", questionID)

	approvalID, err = handler.ConnectorPendingApprovalID(context.Background(), "sess-unknown")
	require.NoError(t, err)
	require.Empty(t, approvalID)
}

func TestConnectorAttentionNoticeForEventMapping(t *testing.T) {
	approval := runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-1",
		Payload:   map[string]any{"request_id": "apr-1", "tool_name": "shell_exec", "reason": "writes outside workspace"},
	}
	notice, key, ok := connectorAttentionNoticeForEvent(approval, nil)
	require.True(t, ok)
	require.Equal(t, connectors.AttentionKindApproval, notice.Kind)
	require.Equal(t, connectors.SessionRef{SessionID: "sess-1"}, notice.Session)
	require.Contains(t, notice.Text, "shell_exec")
	require.Contains(t, notice.Text, "writes outside workspace")
	require.Contains(t, notice.Text, "/approve apr-1")
	require.Equal(t, "approval|sess-1|apr-1", key)

	question := runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "sess-1",
		Payload:   map[string]any{"question_id": "q-1", "prompt": "which env?"},
	}
	notice, key, ok = connectorAttentionNoticeForEvent(question, nil)
	require.True(t, ok)
	require.Equal(t, connectors.AttentionKindQuestion, notice.Kind)
	require.Contains(t, notice.Text, "which env?")
	require.Contains(t, notice.Text, "/answer 1")
	require.Equal(t, "question|sess-1|q-1", key)

	done := runtimeevents.Event{Type: runtimeobserve.EventAgentTurnDone, SessionID: "sess-1", Timestamp: time.Now()}
	notice, key, ok = connectorAttentionNoticeForEvent(done, func(string) bool { return false })
	require.True(t, ok)
	require.Equal(t, connectors.AttentionKindCompletion, notice.Kind)
	require.Contains(t, notice.Text, "已完成")
	require.True(t, strings.HasPrefix(key, "completion|sess-1|"))

	// 托管挂起：run 收尾不等于 turn 完成（与 gateway_attention 同口径）。
	notice, _, ok = connectorAttentionNoticeForEvent(done, func(string) bool { return true })
	require.True(t, ok)
	require.Contains(t, notice.Text, "等待子任务")

	// 空 session / 未订阅类型：不产生通知。
	_, _, ok = connectorAttentionNoticeForEvent(runtimeevents.Event{Type: runtimechat.EventApprovalRequested}, nil)
	require.False(t, ok)
	_, _, ok = connectorAttentionNoticeForEvent(runtimeevents.Event{Type: "assistant_delta", SessionID: "sess-1"}, nil)
	require.False(t, ok)
}

func TestConnectorAttentionDeduperEvictsOldest(t *testing.T) {
	dedupe := newConnectorAttentionDeduper(2)
	require.True(t, dedupe.first("a"))
	require.False(t, dedupe.first("a"))
	require.True(t, dedupe.first("b"))
	require.True(t, dedupe.first("c")) // 淘汰 a
	require.True(t, dedupe.first("a"))
}

// ── 注意力出站端到端（真实总线 + 真实 Service） ─────────────────────────────

func TestSubscribeRuntimeConnectorAttentionDeliversToBoundChatOnce(t *testing.T) {
	handler := runtimeapi.NewHandler(nil, nil, nil)
	bus := handler.SessionDeps("bootstrap-test").EventBus
	require.NotNil(t, bus, "runtime handler 必须带事件总线")

	provider := &recordingProvider{name: "fake"}
	store := connectors.NewMemoryStore(connectors.Config{ID: "c1", Provider: "fake", Enabled: true})
	service := connectors.New(connectors.Deps{Store: store, Backend: stubConnectorBackend{}, Logf: func(string, ...any) {}})
	require.NoError(t, service.Register(provider))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	unsubscribe := subscribeRuntimeConnectorAttention(ctx, handler, service.NotifyAttention, func(string, ...any) {})
	require.NotNil(t, unsubscribe)
	defer unsubscribe()

	approval := runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-1",
		Timestamp: time.Now(),
		Payload:   map[string]any{"request_id": "apr-1", "tool_name": "shell_exec"},
	}

	// 无绑定的 session：事件被映射但不得出站（NotifySession 反查为空即 no-op）。
	unbound := approval
	unbound.SessionID = "sess-unbound"
	bus.Publish(unbound)
	time.Sleep(150 * time.Millisecond)
	require.Empty(t, provider.snapshot(), "无 IM 绑定的 session 不得出站")

	// 建立绑定后：同一事件重复投递只出站一次。
	require.NoError(t, store.PutBinding(connectors.Binding{ConnectorID: "c1", Chat: "chat-1", Session: connectors.SessionRef{SessionID: "sess-1"}}))
	bus.Publish(approval)
	bus.Publish(approval)
	require.Eventually(t, func() bool { return len(provider.snapshot()) == 1 }, 2*time.Second, 10*time.Millisecond)
	sends := provider.snapshot()
	require.Equal(t, "chat-1", sends[0].chat)
	require.Len(t, sends[0].texts, 1)
	require.Contains(t, sends[0].texts[0], "有待审批")
	require.Contains(t, sends[0].texts[0], "/approve apr-1")

	// 提问事件走同一出站链路。
	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventQuestionAsked,
		SessionID: "sess-1",
		Timestamp: time.Now(),
		Payload:   map[string]any{"question_id": "q-1", "prompt": "which env?"},
	})
	require.Eventually(t, func() bool { return len(provider.snapshot()) == 2 }, 2*time.Second, 10*time.Millisecond)
	require.Contains(t, provider.snapshot()[1].texts[0], "which env?")

	// 宿主 close（ctx 取消）后订阅退订：新事件不再出站。
	cancel()
	time.Sleep(100 * time.Millisecond)
	bus.Publish(runtimeevents.Event{
		Type:      runtimechat.EventApprovalRequested,
		SessionID: "sess-1",
		Timestamp: time.Now(),
		Payload:   map[string]any{"request_id": "apr-after-close", "tool_name": "shell_exec"},
	})
	time.Sleep(150 * time.Millisecond)
	require.Len(t, provider.snapshot(), 2, "退订后不得再出站")
}

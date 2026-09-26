package runtimeapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// recordingSessionStorage 包装真实存储，记录写入次数与写入时的 ctx 状态，用于验证
// 「中途落库/收尾落库」的写路径契约（尤其是客户端断开不得丢本回合）。
type recordingSessionStorage struct {
	chat.SessionStorage
	updates     int
	sawCtxErr   error
	hadDeadline bool
	failUpdates int
}

func (s *recordingSessionStorage) Update(ctx context.Context, session *chat.Session) error {
	s.updates++
	_, s.hadDeadline = ctx.Deadline()
	s.sawCtxErr = ctx.Err()
	if s.failUpdates > 0 {
		s.failUpdates--
		return errors.New("storage unavailable")
	}
	return s.SessionStorage.Update(ctx, session)
}

type agentChatCheckpointHarness struct {
	ctx            context.Context
	handler        *Handler
	storage        *recordingSessionStorage
	sessionManager *chat.SessionManager
	session        *chat.Session
}

// newAgentChatCheckpointHarness 复刻 handleAgentChat 的持久化前置条件：一个已落库
// 的会话 + 一个带存储管理器的 handler。
func newAgentChatCheckpointHarness(t *testing.T) *agentChatCheckpointHarness {
	t.Helper()
	ctx := context.Background()
	storage := &recordingSessionStorage{SessionStorage: chat.NewInMemoryStorage()}
	sessionManager := chat.NewSessionManager(storage, nil)
	session, err := sessionManager.Create(ctx, "user-1")
	require.NoError(t, err)
	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetSessionManager(sessionManager)

	return &agentChatCheckpointHarness{
		ctx:            ctx,
		handler:        handler,
		storage:        storage,
		sessionManager: sessionManager,
		session:        session,
	}
}

// seedTurn 构造一个「请求级上下文前缀 + 两轮既有对话 + 本轮 user/assistant」的
// execSession，与 handleAgentChat 进入 ReAct 前装配的形态一致。
func (h *agentChatCheckpointHarness) seedTurn(t *testing.T) (execSession *chat.Session, contextMessages []types.Message) {
	t.Helper()
	h.session.AddMessage(*types.NewUserMessage("first"))
	h.session.AddMessage(*types.NewAssistantMessage("second"))
	require.NoError(t, h.sessionManager.Update(h.ctx, h.session))

	contextMessages = []types.Message{*types.NewSystemMessage("request-scoped environment")}
	execSession = h.session.Clone()
	execSession.ReplaceHistory(prependContextMessages(h.session.GetMessages(), contextMessages))
	return execSession, contextMessages
}

func (h *agentChatCheckpointHarness) storedMessages(t *testing.T) []types.Message {
	t.Helper()
	stored, err := h.sessionManager.Get(h.ctx, h.session.ID)
	require.NoError(t, err)
	return stored.GetMessages()
}

func TestAgentChatHistoryCheckpointerPersistsDurableTranscriptMidTurn(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))
	execSession.AddMessage(*types.NewAssistantMessage("fourth"))

	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")
	require.True(t, checkpointer.enabled())

	before := h.storage.updates
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+1, h.storage.updates, "第一个提交点必须触发一次中途落库")

	messages := h.storedMessages(t)
	require.Len(t, messages, 4, "中途落库必须写入完整 durable 转录")
	require.Equal(t, "third", messages[2].Content)
	require.Equal(t, "fourth", messages[3].Content)
	for _, message := range messages {
		require.NotContains(t, message.Content, "request-scoped environment",
			"请求级上下文前缀不得进入权威转录")
	}

	require.Len(t, h.session.GetMessages(), 2,
		"中途落库写的是快照，不得就地改写仍在运行中的会话对象")
}

func TestAgentChatHistoryCheckpointerThrottlesWithinWindow(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))

	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")
	before := h.storage.updates
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+1, h.storage.updates)
	require.Len(t, h.storedMessages(t), 3)

	execSession.AddMessage(*types.NewAssistantMessage("fourth"))
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+1, h.storage.updates, "窗口内的第二个提交点必须被节流")
	require.Len(t, h.storedMessages(t), 3, "被节流的提交点不得写入存储")

	checkpointer.window.Reset()
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+2, h.storage.updates, "窗口重置后必须继续落库")
	require.Len(t, h.storedMessages(t), 4)
}

func TestAgentChatHistoryPersistFinalLandsAfterFailedTurn(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))

	// 模拟 turn 早期失败：没有任何中途提交点，只有收尾落库。
	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")
	require.NoError(t, checkpointer.PersistFinal())

	require.Len(t, h.storedMessages(t), 3, "失败收尾也必须把本轮已产生的对话落库")
	require.Len(t, h.session.GetMessages(), 3, "收尾落库就地更新请求持有的会话对象")
}

func TestAgentChatHistoryPersistFinalSurvivesClientDisconnect(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))

	request := httptest.NewRequest(http.MethodPost, "/api/agent/chat", nil)
	requestCtx, cancel := context.WithCancel(request.Context())
	cancel() // 客户端断开：SSE 请求 ctx 已取消
	request = request.WithContext(requestCtx)

	checkpointer := newAgentChatHistoryCheckpointer(h.handler, request, h.session, execSession, contextMessages, "turn-1")
	require.NoError(t, checkpointer.PersistFinal())

	require.NoError(t, h.storage.sawCtxErr, "落库不得继承客户端取消，否则整轮对话会在事件仓库之外永久缺失")
	require.True(t, h.storage.hadDeadline, "落库仍必须有自己的超时上限，避免写操作无限挂起")
	require.Len(t, h.storedMessages(t), 3)
}

func TestAgentChatHistoryCheckpointerDisabledWithoutPersistenceTarget(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession := h.session.Clone()
	before := h.storage.updates

	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, nil, execSession, nil, "turn-1")
	require.False(t, checkpointer.enabled())
	checkpointer.OnCheckpoint(h.ctx, nil) // 不得 panic、不得写入
	require.NoError(t, checkpointer.PersistFinal())
	require.Equal(t, before, h.storage.updates)

	var nilCheckpointer *agentChatHistoryCheckpointer
	require.False(t, nilCheckpointer.enabled())
	nilCheckpointer.OnCheckpoint(h.ctx, nil) // 不得 panic
	require.NoError(t, nilCheckpointer.PersistFinal())

	bare := &Handler{}
	bareCheckpointer := newAgentChatHistoryCheckpointer(bare, nil, h.session, execSession, nil, "turn-2")
	require.False(t, bareCheckpointer.enabled(), "无存储管理器的 handler 必须保持零成本")
	bareCheckpointer.OnCheckpoint(h.ctx, nil)
	require.NoError(t, bareCheckpointer.PersistFinal())
	require.Equal(t, before, h.storage.updates)
}

func TestAgentChatHistoryCheckpointerUsesCommitSnapshotsWithoutExecSession(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))
	execSession.AddMessage(*types.NewAssistantMessage("fourth"))

	// 非流式入口（runtimechatcore.ExecuteNonStream）拿不到 execSession 句柄，
	// 历史只能来自提交点快照。
	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, nil, contextMessages, "turn-1")
	require.True(t, checkpointer.enabled())

	before := h.storage.updates
	checkpointer.OnCheckpoint(h.ctx, execSession.GetMessages())
	require.Equal(t, before+1, h.storage.updates, "提交点快照必须能触发中途落库")

	messages := h.storedMessages(t)
	require.Len(t, messages, 4)
	require.Equal(t, "third", messages[2].Content)
	for _, message := range messages {
		require.NotContains(t, message.Content, "request-scoped environment")
	}

	// 快照是只读副本：调用方后续改动不得回灌到已记录的提交点。
	execSession.AddMessage(*types.NewAssistantMessage("fifth"))
	checkpointer.window.Reset()
	checkpointer.OnCheckpoint(h.ctx, execSession.GetMessages())
	require.Len(t, h.storedMessages(t), 5)

	// 失败收尾使用最近一次提交点快照（第 6 条消息尚未提交，不应出现）。
	execSession.AddMessage(*types.NewAssistantMessage("sixth"))
	require.NoError(t, checkpointer.PersistFinal())
	stored := h.storedMessages(t)
	require.Len(t, stored, 5, "失败收尾只能写入已提交的 durable 历史")
	require.Equal(t, "fifth", stored[4].Content)

	// 空提交点保留旧快照，不需要重复写入。
	snapshot := checkpointer.currentHistory()
	require.NotEmpty(t, snapshot)
	require.Equal(t, "fifth", snapshot[len(snapshot)-1].Content)
}

func TestAgentChatHistoryCheckpointerRetriesImmediatelyAfterPersistFailure(t *testing.T) {
	h := newAgentChatCheckpointHarness(t)
	execSession, contextMessages := h.seedTurn(t)
	execSession.AddMessage(*types.NewUserMessage("third"))

	checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")

	h.storage.failUpdates = 1
	before := h.storage.updates
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+1, h.storage.updates, "提交点必须尝试落库")
	require.Len(t, h.storedMessages(t), 2, "写入失败不得污染权威转录")

	// 失败回退节流戳：下一个提交点必须立即重试，而不是再等一个窗口。
	checkpointer.OnCheckpoint(h.ctx, nil)
	require.Equal(t, before+2, h.storage.updates, "失败后必须立刻重试")
	require.Len(t, h.storedMessages(t), 3)
}

// TestAgentChatCheckpointPreservesBrokerHandleAliases 是 [JOB_NOT_FOUND]
// background job reference not found 的回归：broker 在 background_task /
// spawn_agent 之后把 job_ref_* / session_ref_* 句柄别名经独立的会话行读改写写进
// 权威会话，而本回合持有的 session/execSession 快照早于那次写入。中途 checkpoint
// 与收尾落库都是整行写回，若不在写前合并别名注册表，模型手里的句柄会在下一次
// 落库后立即失效。
func TestAgentChatCheckpointPreservesBrokerHandleAliases(t *testing.T) {
	aliases := map[string]interface{}{
		"jobs": map[string]interface{}{
			"alias_to_actual": map[string]interface{}{"job_ref_test": "job_1"},
		},
	}
	seedBrokerAliases := func(t *testing.T, h *agentChatCheckpointHarness) {
		t.Helper()
		brokerSession, err := h.sessionManager.Get(h.ctx, h.session.ID)
		require.NoError(t, err)
		brokerSession.SetContext(toolbroker.SessionHandleAliasesContextKey, aliases)
		require.NoError(t, h.sessionManager.Update(h.ctx, brokerSession))
	}
	assertAliasesPreserved := func(t *testing.T, h *agentChatCheckpointHarness) {
		t.Helper()
		stored, err := h.sessionManager.Get(h.ctx, h.session.ID)
		require.NoError(t, err)
		got, ok := stored.GetContext(toolbroker.SessionHandleAliasesContextKey)
		require.True(t, ok, "整行写回不得抹掉 broker 句柄别名注册表")
		require.Equal(t, aliases, got)
	}

	t.Run("mid_turn_checkpoint", func(t *testing.T) {
		h := newAgentChatCheckpointHarness(t)
		execSession, contextMessages := h.seedTurn(t)
		execSession.AddMessage(*types.NewUserMessage("third"))
		seedBrokerAliases(t, h)

		checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")
		checkpointer.OnCheckpoint(h.ctx, nil)

		require.Len(t, h.storedMessages(t), 3)
		assertAliasesPreserved(t, h)
	})

	t.Run("final_persist", func(t *testing.T) {
		h := newAgentChatCheckpointHarness(t)
		execSession, contextMessages := h.seedTurn(t)
		execSession.AddMessage(*types.NewUserMessage("third"))
		seedBrokerAliases(t, h)

		checkpointer := newAgentChatHistoryCheckpointer(h.handler, nil, h.session, execSession, contextMessages, "turn-1")
		require.NoError(t, checkpointer.PersistFinal())

		require.Len(t, h.storedMessages(t), 3)
		assertAliasesPreserved(t, h)
	})
}

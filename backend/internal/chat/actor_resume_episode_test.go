package chat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// C3-7 / AC-P2-7a：挂起态（§6.12 parked turn）收到 steer ⇒ 起"同一 turn 的新
// episode"，账本不变（设计 §6.15 / §8 P2-5）。
//
// 承载点：`SessionActor.nextTurnID`（`actor_resume_episode.go`）在提交时回查
// durable batch 控制面上的挂起记录，命中则复用挂起 turn 的 `turn_id`；run 收尾
// 时 `syncSuspendedTurn` 把"本回合是否仍挂起"写回 `RuntimeState.SuspendedTurnID`。
// 只有 durable store 才能承载 §6.12 记录（I9 探测），因此用例必须用文件型
// SQLite store，而不是内存 store。

type resumeEpisodeHarness struct {
	actor   *SessionActor
	store   *InMemoryRuntimeStore
	storage *InMemoryStorage
	session *Session
	batches subagentbatch.BatchStore
	// C4-3 重启用例需要重建 actor 与新账本句柄：暴露 agent / runtime 与账本文件
	// 路径，让用例能在"上一个进程退出"后用新句柄打开同一份账本。
	apiAgent    *agent.Agent
	llmRuntime  *llm.LLMRuntime
	batchesPath string
	// preparedTurnIDs 记录每次 run 实际使用的 turn_id：PrepareRun 在 run 启动后
	// 立刻执行，此时 RuntimeState.CurrentTurnID 已是本回合的 turn_id。
	preparedTurnIDs []string
	// parkOnPrepare 由用例注入：在 run 内落盘一条 §6.12 挂起记录，模拟 agent loop
	// 的 background 派发（`loop.parkBackgroundTurn`）。
	parkOnPrepare func(turnID string)
}

func newResumeEpisodeHarness(t *testing.T) *resumeEpisodeHarness {
	t.Helper()
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	t.Cleanup(manager.Stop)
	session, err := manager.CreateSession(ctx, "resume-episode-user")
	require.NoError(t, err)

	store := NewInMemoryRuntimeStore(64)
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 1})
	provider := NewMockLLMProviderForChat()
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))
	require.NoError(t, runtime.RegisterProviderAlias("gpt-4", provider.Name()))
	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "resume-episode-agent",
		Model:    "gpt-4",
		MaxSteps: 3,
	}, nil, runtime)

	batchesPath := filepath.Join(t.TempDir(), "batches.db")
	batches, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: batchesPath,
	})
	require.NoError(t, err)
	require.True(t, batches.IsDurable())
	t.Cleanup(func() { _ = batches.Close() })
	apiAgent.SetSubagentBatchCoordinator(agent.NewSubagentBatchCoordinator(agent.SubagentBatchCoordinatorConfig{
		Store: batches,
	}))
	require.True(t, apiAgent.SupportsSuspension(), "durable store must pass the I9 probe")

	h := &resumeEpisodeHarness{
		store:       store,
		storage:     storage,
		session:     session,
		batches:     batches,
		apiAgent:    apiAgent,
		llmRuntime:  runtime,
		batchesPath: batchesPath,
	}
	// actor 先声明再赋值：PrepareRun 回调在闭包内引用它（短变量声明的作用域
	// 从语句结束才开始，写在字面量里会编译失败）。
	var actor *SessionActor
	actor, err = NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   store,
		EventStore:   store,
		PrepareRun: func(_ context.Context, _ *Session, _ bool) error {
			if summary, ok := actor.StateSummary(); ok {
				h.preparedTurnIDs = append(h.preparedTurnIDs, summary.CurrentTurnID)
				if h.parkOnPrepare != nil {
					h.parkOnPrepare(summary.CurrentTurnID)
				}
			}
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)
	h.actor = actor
	return h
}

func (h *resumeEpisodeHarness) seedSuspendedTurn(t *testing.T, turnID string) *subagentbatch.TurnSuspension {
	t.Helper()
	ctx := context.Background()
	parkedAt := time.Now().UTC().Truncate(time.Millisecond)
	record := &subagentbatch.TurnSuspension{
		TurnID:        turnID,
		SessionID:     h.session.ID,
		RootScopeID:   h.session.ID,
		ObligationIDs: []string{"batch-1", "task-1"},
		ParkedAt:      parkedAt,
		ResumeQueue:   []string{"batch-1"},
	}
	require.NoError(t, h.batches.ParkTurnSuspension(ctx, record))
	require.NoError(t, h.store.SaveState(ctx, &RuntimeState{
		SessionID:       h.session.ID,
		Status:          SessionIdle,
		SuspendedTurnID: turnID,
		UpdatedAt:       time.Now().UTC(),
	}))
	return record
}

func (h *resumeEpisodeHarness) history(t *testing.T) []runtimetypes.Message {
	t.Helper()
	session, err := h.storage.Load(context.Background(), h.session.ID)
	require.NoError(t, err)
	require.NotNil(t, session)
	return session.History
}

// TestSubmitPromptOnSuspendedTurnResumesSameTurnID 覆盖 AC-P2-7a 主路径：
// 挂起态收到 steer ⇒ 新 episode 复用挂起 turn 的 turn_id，用户输入就是本 episode
// 的 prompt（天然置顶），挂起记录（账本载体）逐字段不变。
func TestSubmitPromptOnSuspendedTurnResumesSameTurnID(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	record := h.seedSuspendedTurn(t, "turn_parked")

	result, err := h.actor.SubmitPrompt(ctx, "steer: 先做 A 再做 B，别等子任务", nil)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Equal(t, []string{"turn_parked"}, h.preparedTurnIDs,
		"挂起态收到 steer 必须以同一 turn_id 起新 episode，而不是新开 turn")
	require.True(t, historyContainsUserText(h.history(t), "steer: 先做 A 再做 B，别等子任务"),
		"steer 文本必须成为该 episode 的用户输入")

	state := h.actor.StateForInspection()
	require.NotNil(t, state)
	require.Equal(t, "", state.CurrentTurnID, "episode 结束后没有在途 run")
	require.Equal(t, "turn_parked", state.SuspendedTurnID,
		"挂起记录仍在 ⇒ turn 仍挂起，后续 steer 继续复用同一 turn_id")

	after, ok, err := h.batches.GetTurnSuspension(ctx, h.session.ID, "turn_parked")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, record.ObligationIDs, after.ObligationIDs, "steer 不得改写 obligation 账本")
	require.Equal(t, record.ResumeQueue, after.ResumeQueue)
	require.Equal(t, record.ParkedAt.UTC(), after.ParkedAt.UTC(), "steer 不是重新挂起")
}

// TestSubmitPromptClearsStaleSuspendedTurnID 覆盖缓存自愈：挂起记录已被清
// （resume 收尾 / 放弃）后，陈旧缓存不得继续吞掉用户输入的 turn_id。
func TestSubmitPromptClearsStaleSuspendedTurnID(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()
	require.NoError(t, h.store.SaveState(ctx, &RuntimeState{
		SessionID:       h.session.ID,
		Status:          SessionIdle,
		SuspendedTurnID: "turn_gone",
		UpdatedAt:       time.Now().UTC(),
	}))

	_, err := h.actor.SubmitPrompt(ctx, "普通新输入", nil)
	require.NoError(t, err)

	require.Len(t, h.preparedTurnIDs, 1)
	require.NotEqual(t, "turn_gone", h.preparedTurnIDs[0], "没有挂起记录时不得复用旧 turn_id")
	require.True(t, strings.HasPrefix(h.preparedTurnIDs[0], "turn_"))
	require.Equal(t, "", h.actor.StateForInspection().SuspendedTurnID,
		"记录已清 ⇒ 缓存必须失效")
}

// TestRunEndStampsSuspendedTurnIDForParkedTurn 覆盖写入侧：回合内派发了 durable
// background obligations（§6.12）后收尾 ⇒ turn_id 留在状态里，下一次 steer /
// wake resume 才能落在同一 turn 上（AC-P2-7a / AC-P1-1a）。
func TestRunEndStampsSuspendedTurnIDForParkedTurn(t *testing.T) {
	h := newResumeEpisodeHarness(t)
	ctx := context.Background()

	var parkedTurnID string
	var parkErr error
	h.parkOnPrepare = func(turnID string) {
		parkedTurnID = turnID
		parkErr = h.batches.ParkTurnSuspension(context.Background(), &subagentbatch.TurnSuspension{
			TurnID:        turnID,
			SessionID:     h.session.ID,
			RootScopeID:   h.session.ID,
			ObligationIDs: []string{"batch-2"},
			ParkedAt:      time.Now().UTC(),
			ResumeQueue:   []string{"batch-2"},
		})
	}

	_, err := h.actor.SubmitPrompt(ctx, "派发后台子任务后收尾", nil)
	require.NoError(t, err)
	require.NoError(t, parkErr)
	require.NotEmpty(t, parkedTurnID)
	require.Equal(t, parkedTurnID, h.actor.StateForInspection().SuspendedTurnID,
		"回合收尾发现挂起记录 ⇒ turn_id 必须留在状态里供后续 episode 复用")
}

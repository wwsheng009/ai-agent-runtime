package chat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// checkpointRecordingStore 记录会话存储的 Update 次数与每次写入时历史长度，
// 并可注入写失败，用于验证长 turn 中途落库的时机（长 turn 边跑边存）、节流
// 与失败隔离语义。
type checkpointRecordingStore struct {
	*InMemoryStorage
	updateCalls atomic.Int32
	failUpdate  atomic.Bool

	mu          sync.Mutex
	historyLens []int
}

func (s *checkpointRecordingStore) Update(ctx context.Context, session *Session) error {
	s.updateCalls.Add(1)
	s.mu.Lock()
	if session != nil {
		s.historyLens = append(s.historyLens, len(session.History))
	} else {
		s.historyLens = append(s.historyLens, -1)
	}
	s.mu.Unlock()
	if s.failUpdate.Load() {
		return errors.New("checkpoint write failed")
	}
	return s.InMemoryStorage.Update(ctx, session)
}

func (s *checkpointRecordingStore) recordedHistoryLens() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]int, len(s.historyLens))
	copy(out, s.historyLens)
	return out
}

func newCheckpointTestActor(t *testing.T, sessionID string, store *checkpointRecordingStore, bus *runtimeevents.Bus, interval time.Duration) *SessionActor {
	t.Helper()
	apiAgent := agent.NewAgent(&agent.Config{Name: "checkpoint-agent", Model: "test-model", MaxSteps: 1}, nil)
	actor, err := NewSessionActor(sessionID, SessionActorConfig{
		Agent:              apiAgent,
		SessionStore:       store,
		EventBus:           bus,
		CheckpointInterval: interval,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)
	return actor
}

func checkpointTestHistory(content string) []types.Message {
	return []types.Message{{Role: "user", Content: content}}
}

// TestSessionActorCheckpointWritesMidTurnHistoryWithThrottle 回归：长 turn 中途
// 提交 durable 历史后，权威会话存储必须在 turn 结束前就前进（而不是等 turn 收尾），
// 同时按 CheckpointInterval 节流，避免每个提交点都写库。
func TestSessionActorCheckpointWritesMidTurnHistoryWithThrottle(t *testing.T) {
	ctx := context.Background()
	recorder := &checkpointRecordingStore{InMemoryStorage: NewInMemoryStorage()}
	manager := NewSessionManager(recorder, nil)
	session, err := manager.CreateSession(ctx, "checkpoint-user")
	require.NoError(t, err)

	actor := newCheckpointTestActor(t, session.ID, recorder, nil, 60*time.Millisecond)
	baseline := recorder.updateCalls.Load()

	session.ReplaceHistory(checkpointTestHistory("long turn step 1"))
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline+1, recorder.updateCalls.Load(), "首次中途落库必须写入存储")
	require.Equal(t, []int{1}, recorder.recordedHistoryLens(), "写入的必须是 step 1 提交后的完整历史")

	// 节流窗口内的后续提交点只更新内存历史，不再写库。
	session.ReplaceHistory(append(checkpointTestHistory("long turn step 1"),
		types.Message{Role: "assistant", Content: "long turn step 2"}))
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline+1, recorder.updateCalls.Load(), "节流窗口内不得重复写库")

	// 窗口结束后，下一个提交点把新增历史增量补上。
	time.Sleep(80 * time.Millisecond)
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline+2, recorder.updateCalls.Load(), "窗口结束后必须继续中途落库")
	require.Equal(t, []int{1, 2}, recorder.recordedHistoryLens())

	stored, err := recorder.InMemoryStorage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.Len(t, stored.History, 2)
	require.Equal(t, "long turn step 2", stored.History[1].Content)
}

// TestSessionActorCheckpointFailureIsIsolatedAndObservable 回归：中途落库失败
// 必须就地隔离（不改变 turn 结果、不阻断后续提交点），并且回退节流戳立刻重试。
func TestSessionActorCheckpointFailureIsIsolatedAndObservable(t *testing.T) {
	ctx := context.Background()
	recorder := &checkpointRecordingStore{InMemoryStorage: NewInMemoryStorage()}
	manager := NewSessionManager(recorder, nil)
	session, err := manager.CreateSession(ctx, "checkpoint-failure-user")
	require.NoError(t, err)

	bus := runtimeevents.NewBus()
	received := make(chan runtimeevents.Event, 4)
	unsubscribe := bus.SubscribeCancelable("session.checkpoint_persist_error", func(event runtimeevents.Event) {
		select {
		case received <- event:
		default:
		}
	})
	defer unsubscribe()

	// 间隔取远大于测试时长：若实现没有回退节流戳，重试就不会发生。
	actor := newCheckpointTestActor(t, session.ID, recorder, bus, time.Hour)
	baseline := recorder.updateCalls.Load()
	recorder.failUpdate.Store(true)

	session.ReplaceHistory(checkpointTestHistory("failing checkpoint"))
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline+1, recorder.updateCalls.Load())

	select {
	case event := <-received:
		require.Equal(t, "session.checkpoint_persist_error", event.Type)
		require.Equal(t, session.ID, event.SessionID)
		require.Equal(t, "mid_turn_checkpoint", event.Payload["stage"])
	case <-time.After(2 * time.Second):
		t.Fatal("中途落库失败必须上报 session.checkpoint_persist_error 事件")
	}

	// 失败回退节流戳：下一提交点立即重试，而不是再等一个（1 小时）窗口。
	recorder.failUpdate.Store(false)
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline+2, recorder.updateCalls.Load(), "失败后必须在下一个提交点立即重试")
}

// TestSessionActorCheckpointIntervalSemantics 覆盖间隔解析语义：0 用默认值，
// 负数显式禁用中途落库（turn 结束的 post-turn sync 仍是最终一致性保证）。
func TestSessionActorCheckpointIntervalSemantics(t *testing.T) {
	require.Equal(t, DefaultSessionCheckpointInterval, resolveSessionCheckpointInterval(0))
	require.Equal(t, -1*time.Second, resolveSessionCheckpointInterval(-1*time.Second))
	require.Equal(t, 3*time.Second, resolveSessionCheckpointInterval(3*time.Second))

	ctx := context.Background()
	recorder := &checkpointRecordingStore{InMemoryStorage: NewInMemoryStorage()}
	manager := NewSessionManager(recorder, nil)
	session, err := manager.CreateSession(ctx, "checkpoint-disabled-user")
	require.NoError(t, err)

	actor := newCheckpointTestActor(t, session.ID, recorder, nil, -1*time.Second)
	baseline := recorder.updateCalls.Load()
	require.Equal(t, -1*time.Second, actor.checkpointInterval)
	require.Nil(t, actor.historyCheckpointLoopConfig(nil, nil, session).OnHistoryCheckpoint,
		"禁用中途落库时不得挂载 checkpoint 回调")

	session.ReplaceHistory(checkpointTestHistory("disabled checkpoint"))
	actor.checkpointSessionHistory(ctx, session)
	require.Equal(t, baseline, recorder.updateCalls.Load(), "禁用后不得中途写库")
}

// TestSessionActorCheckpointLoopConfigWiresCallback 回归：ReAct 配置必须挂上
// 中途落库回调，回调执行时把当前 durable 历史写入会话存储。
func TestSessionActorCheckpointLoopConfigWiresCallback(t *testing.T) {
	ctx := context.Background()
	recorder := &checkpointRecordingStore{InMemoryStorage: NewInMemoryStorage()}
	manager := NewSessionManager(recorder, nil)
	session, err := manager.CreateSession(ctx, "checkpoint-wiring-user")
	require.NoError(t, err)

	actor := newCheckpointTestActor(t, session.ID, recorder, nil, 60*time.Millisecond)
	baseline := recorder.updateCalls.Load()

	loopConfig := actor.historyCheckpointLoopConfig(nil, nil, session)
	require.NotNil(t, loopConfig)
	require.NotNil(t, loopConfig.OnHistoryCheckpoint, "长 turn 中途落库回调必须已挂载")

	session.ReplaceHistory(checkpointTestHistory("wired checkpoint"))
	loopConfig.OnHistoryCheckpoint(ctx, session.GetMessages())
	require.Equal(t, baseline+1, recorder.updateCalls.Load())
	require.Equal(t, []int{1}, recorder.recordedHistoryLens())

	// 回调不得改写共享 loopConfig（每次 run 必须拿到独立副本）。
	require.NotNil(t, actor.loopConfig)
	require.Nil(t, actor.loopConfig.OnHistoryCheckpoint, "共享 loopConfig 不应被 checkpoint 回调污染")
}

// blockingFirstCallProvider 在第一次 LLM 调用处阻塞，让测试可以在 run 仍在
// 运行（turn 未结束）时观察权威会话存储是否已前进。
type blockingFirstCallProvider struct {
	*MockLLMProviderForChat
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *blockingFirstCallProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
	return p.MockLLMProviderForChat.Call(ctx, req)
}

func (p *blockingFirstCallProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	p.once.Do(func() {
		close(p.entered)
		<-p.release
	})
	return p.MockLLMProviderForChat.Stream(ctx, req)
}

// TestSessionActorCheckpointPersistsWhileTurnStillRunning 端到端回归（对应线上
// 故障：会话行在长 turn 期间停在起始状态，46 分钟后才随 turn 收尾落库）：
// turn 仍在运行时（LLM 调用被阻塞），权威会话存储里必须已经能读到本 turn 已提交
// 的历史，而不是等 turn 结束。
func TestSessionActorCheckpointPersistsWhileTurnStillRunning(t *testing.T) {
	ctx := context.Background()
	recorder := &checkpointRecordingStore{InMemoryStorage: NewInMemoryStorage()}
	manager := NewSessionManager(recorder, nil)
	session, err := manager.CreateSession(ctx, "checkpoint-mid-run-user")
	require.NoError(t, err)

	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "gpt-4", MaxRetries: 1})
	provider := &blockingFirstCallProvider{
		MockLLMProviderForChat: NewMockLLMProviderForChat(),
		entered:                make(chan struct{}),
		release:                make(chan struct{}),
	}
	runtime.RegisterProvider(provider.Name(), provider)
	_ = runtime.RegisterProviderAlias("gpt-4", provider.Name())

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:         "checkpoint-mid-run-agent",
		Model:        "gpt-4",
		MaxSteps:     3,
		SystemPrompt: "You are a helpful assistant.",
	}, nil, runtime)

	// 显式传 0：走 actor 默认间隔（DefaultSessionCheckpointInterval），
	// 确保默认配置下长 turn 就会中途落库。
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: recorder,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)
	baseline := recorder.updateCalls.Load()

	runDone := make(chan error, 1)
	go func() {
		_, runErr := actor.SubmitPrompt(ctx, "长 turn 的第一段输入", nil)
		runDone <- runErr
	}()

	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("run 未进入首次 LLM 调用")
	}

	require.Eventually(t, func() bool {
		return recorder.updateCalls.Load() > baseline
	}, 5*time.Second, 10*time.Millisecond,
		"turn 仍在运行时，中途落库必须已把已提交历史写入权威会话存储")

	midRun, err := recorder.InMemoryStorage.Load(ctx, session.ID)
	require.NoError(t, err)
	require.NotEmpty(t, midRun.History, "turn 中途的权威会话必须已包含本 turn 已提交内容")
	require.Equal(t, "长 turn 的第一段输入", midRun.History[0].Content)

	// 放行 run，确认中途落库不影响 turn 正常收尾。
	close(provider.release)
	select {
	case runErr := <-runDone:
		require.NoError(t, runErr, "中途落库必须不影响 turn 结果")
	case <-time.After(10 * time.Second):
		t.Fatal("run 未在放行后结束")
	}
}

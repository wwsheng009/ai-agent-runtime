package chat

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestPartialRunProductPrefersResultOutputThenHistory 钉住 A4 的取源优先级：
// 历史里最后一条非空 assistant 消息优先于 result.Output（取消时 result.Output
// 常是宿主罐头停止提示）；两者都没有时返回空 summary，但 partial_steps 仍如实
// 报告已完成步骤。
func TestPartialRunProductPrefersResultOutputThenHistory(t *testing.T) {
	require.Empty(t, lastAssistantMessage(nil))

	session := NewSession("partial-product-user")
	session.ReplaceHistory([]types.Message{
		{Role: "user", Content: "do the work"},
		{Role: "assistant", Content: "plan: inspect first"},
		{Role: "tool", Content: "grep output ..."},
		{Role: "assistant", Content: "  latest deliverable  "},
	})

	summary, source, steps := partialRunProduct(nil, session)
	require.Equal(t, "latest deliverable", summary)
	require.Equal(t, "last_assistant_message", source)
	require.Equal(t, 1, steps)

	// 取消时 result.Output 往往是罐头停止提示，不能让它盖住历史里的真实产物。
	summary, source, _ = partialRunProduct(&agent.Result{Output: "当前运行已停止；已保留 0 条工具观察，可从现有会话继续。"}, session)
	require.Equal(t, "latest deliverable", summary)
	require.Equal(t, "last_assistant_message", source)

	// 历史里还没有内容时（首次 LLM 调用就被取消）才回退到 result.Output。
	summary, source, _ = partialRunProduct(
		&agent.Result{Output: "partial answer from the canceled run"},
		NewSession("partial-product-history-empty"),
	)
	require.Equal(t, "partial answer from the canceled run", summary)
	require.Equal(t, "result_output", source)

	empty, emptySource, emptySteps := partialRunProduct(nil, NewSession("partial-product-empty"))
	require.Empty(t, empty)
	require.Empty(t, emptySource)
	require.Zero(t, emptySteps)
}

// TestClipPartialProductBoundsRunawayTranscripts 钉住有界性：2,000 runes 上限 +
// 尾部省略号，避免一次取消把大 transcript 塞回父代理的历史预算。
func TestClipPartialProductBoundsRunawayTranscripts(t *testing.T) {
	long := strings.Repeat("中", partialProductSummaryRunes+500)
	clipped := clipPartialProduct(long)
	require.LessOrEqual(t, len([]rune(clipped)), partialProductSummaryRunes+1)
	require.True(t, strings.HasSuffix(clipped, "…"))

	require.Equal(t, "ok", clipPartialProduct("ok"))
}

// TestPendingBatchRecoveryPayloadCarriesPartialProduct 钉住 resume 型终态（恢复
// 未完成工具批）的部分产物接入：恢复中断时同样要带出历史里已产出的工作；没有
// 产物时保持原有载荷形状（不新增 partial_* 键、steps 仍为 0）。
func TestPendingBatchRecoveryPayloadCarriesPartialProduct(t *testing.T) {
	session := NewSession("pending-batch-recovery")
	session.ReplaceHistory([]types.Message{
		{Role: "user", Content: "resume the interrupted batch"},
		{Role: "assistant", Content: "batch step 2 running (tool call next)"},
		{Role: "tool", Content: "step 1 ok"},
	})

	payload := pendingBatchRecoveryPayload("turn-recovery-1", context.Canceled, SessionStopped, session)
	require.Equal(t, "turn-recovery-1", payload["turn_id"])
	require.Equal(t, true, payload["resume"])
	require.Equal(t, false, payload["success"])
	require.Equal(t, 0, payload["steps"])
	require.Equal(t, int64(0), payload["duration"])
	require.Equal(t, "context canceled", payload["error"])
	require.Equal(t, SessionStopped, payload["status"])
	require.Equal(t, "batch step 2 running (tool call next)", payload["partial_summary"])
	require.Equal(t, "last_assistant_message", payload["partial_source"])
	require.Equal(t, 1, payload["partial_steps"])

	// 无可救产物：原有形状不变（无 partial_* 键），error 为空串。
	empty := pendingBatchRecoveryPayload("turn-recovery-2", nil, SessionIdle, NewSession("pending-batch-recovery-empty"))
	require.Equal(t, "", empty["error"])
	require.Equal(t, 0, empty["steps"])
	require.NotContains(t, empty, "partial_summary")
	require.NotContains(t, empty, "partial_source")
	require.NotContains(t, empty, "partial_steps")
}

// TestEnrichSessionInterruptedPayloadAttachesPartialProduct 钉住中断型终态的两条
// 产物取源：主取源是 run 自己的快照（运行期存储里还没有产物，且无需任何 I/O）；
// 仅当没有活动 run 时才兜底读存储；有活动 run 但尚无产物时以 run 为准，不回退。
// 两条取源都 fail-open，取不到就保持原样且快速返回。
func TestEnrichSessionInterruptedPayloadAttachesPartialProduct(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "interrupted-partial-product-user")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		{Role: "user", Content: "long running task"},
		{Role: "assistant", Content: "interrupted run deliverable"},
		{Role: "tool", Content: "step output"},
	})
	require.NoError(t, storage.Save(ctx, session))

	// 主取源：run 快照；actor 故意不接 sessionStore，证明运行期不依赖存储。
	liveRun := &sessionRunControl{turnID: "turn-live"}
	liveRun.notePartialProduct(nil, session)
	liveActor := &SessionActor{id: session.ID}
	livePayload := map[string]interface{}{"reason": "interrupt"}
	start := time.Now()
	liveActor.enrichSessionInterruptedPayload(livePayload, liveRun)
	require.Less(t, time.Since(start), 50*time.Millisecond, "run 快照取源不得有任何 I/O")
	require.Equal(t, "interrupted run deliverable", livePayload["partial_summary"])
	require.Equal(t, "last_assistant_message", livePayload["partial_source"])
	require.Equal(t, 1, livePayload["partial_steps"])
	require.Equal(t, "interrupt", livePayload["reason"], "既有字段不得被改写")

	// 兜底取源：没有活动 run（会话已挂起/已终态）时读存储快照，仍带时间上限。
	actor := &SessionActor{id: session.ID, sessionStore: storage}
	payload := map[string]interface{}{"reason": "interrupt"}
	start = time.Now()
	actor.enrichSessionInterruptedPayload(payload, nil)
	require.Less(t, time.Since(start), time.Second, "兜底补产物必须有硬性时间上限")
	require.Equal(t, "interrupted run deliverable", payload["partial_summary"])
	require.Equal(t, "last_assistant_message", payload["partial_source"])
	require.Equal(t, 1, payload["partial_steps"])

	// 快照不存在：fail-open，不加 partial_* 键。
	missing := &SessionActor{id: "session_missing_snapshot", sessionStore: storage}
	missingPayload := map[string]interface{}{"reason": "stall_timeout"}
	missing.enrichSessionInterruptedPayload(missingPayload, nil)
	require.NotContains(t, missingPayload, "partial_summary")

	// 未接存储、也没有 run 快照：直接返回，不 panic。
	noStore := &SessionActor{id: session.ID}
	noStorePayload := map[string]interface{}{"reason": "interrupt"}
	noStore.enrichSessionInterruptedPayload(noStorePayload, nil)
	require.NotContains(t, noStorePayload, "partial_summary")

	// 有活动 run 但尚无产物：run 是权威，不回退到存储（否则会把上一轮的旧产物
	// 当作本轮中断产物上报）。
	emptyRun := &sessionRunControl{turnID: "turn-empty"}
	emptyRunPayload := map[string]interface{}{"reason": "interrupt"}
	actor.enrichSessionInterruptedPayload(emptyRunPayload, emptyRun)
	require.NotContains(t, emptyRunPayload, "partial_summary")
}

// TestHistoryCheckpointCallbackNotesPartialProductForInterrupt 钉住运行期接线：
// 每个 durable 历史提交点的回调都必须刷新 run 的产物快照（与落库节流/禁用无关），
// 使中断路径拿到的是**运行中**的产物，而不是滞后的存储快照。
func TestHistoryCheckpointCallbackNotesPartialProductForInterrupt(t *testing.T) {
	ctx := context.Background()
	session := NewSession("checkpoint-partial-note")
	session.ReplaceHistory([]types.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "mid-run deliverable"},
		{Role: "tool", Content: "step output"},
	})
	// 中途落库显式禁用：快照仍必须刷新（该路径零 I/O，不受节流影响）。
	actor := &SessionActor{id: session.ID, checkpointInterval: -1 * time.Second}
	run := &sessionRunControl{sessionID: session.ID, turnID: "turn-live"}
	runCtx := withSessionRunControl(ctx, run)

	cfg := actor.historyCheckpointLoopConfig(nil, nil, session)
	require.NotNil(t, cfg.OnHistoryCheckpoint, "A5 起回调必须挂载（快照刷新不依赖落库开关）")
	cfg.OnHistoryCheckpoint(runCtx, nil)

	product := run.partialProductSnapshot()
	require.NotNil(t, product, "历史提交点必须刷新 run 产物快照")
	require.Equal(t, "mid-run deliverable", product.summary)
	require.Equal(t, "last_assistant_message", product.source)
	require.Equal(t, 1, product.steps)

	// 端到端（运行中）：中断载荷直接取快照，无需存储。
	payload := map[string]interface{}{"reason": "interrupt"}
	actor.enrichSessionInterruptedPayload(payload, run)
	require.Equal(t, "mid-run deliverable", payload["partial_summary"])
	require.Equal(t, "last_assistant_message", payload["partial_source"])
	require.Equal(t, 1, payload["partial_steps"])

	// 工具执行前的通知：会话对象尚未同步，"已产出但未提交"的 assistant 文本只在
	// 回调携带的 messages 里——这是"长工具运行中被中断"的主场景。
	preToolRun := &sessionRunControl{sessionID: session.ID, turnID: "turn-pretool"}
	preToolCfg := actor.historyCheckpointLoopConfig(nil, nil, NewSession("checkpoint-pretool"))
	preToolCfg.OnHistoryCheckpoint(withSessionRunControl(ctx, preToolRun), []types.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", Content: "produced before a long tool"},
	})
	preToolProduct := preToolRun.partialProductSnapshot()
	require.NotNil(t, preToolProduct, "工具执行前的通知也必须刷新快照（此时会话尚未同步）")
	require.Equal(t, "produced before a long tool", preToolProduct.summary)
	require.Equal(t, 0, preToolProduct.steps, "工具还没执行完，步数应为 0")

	preToolPayload := map[string]interface{}{"reason": "interrupt"}
	actor.enrichSessionInterruptedPayload(preToolPayload, preToolRun)
	require.Equal(t, "produced before a long tool", preToolPayload["partial_summary"])
	require.Equal(t, "last_assistant_message", preToolPayload["partial_source"])
	require.Equal(t, 0, preToolPayload["partial_steps"])
}

// TestSessionActorInterruptEventCarriesPartialProduct 是中断路径的端到端回归：
// handleInterrupt 发布的 session_interrupted 必须带上已产出的部分产物，而不是只有
// reason/turn 信息。
func TestSessionActorInterruptEventCarriesPartialProduct(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "interrupt-event-partial-user")
	require.NoError(t, err)
	session.ReplaceHistory([]types.Message{
		{Role: "user", Content: "do a long task"},
		{Role: "assistant", Content: "halfway deliverable before ESC"},
		{Role: "tool", Content: "tool output"},
	})
	require.NoError(t, storage.Save(ctx, session))

	runtimeStore := NewInMemoryRuntimeStore(64)
	require.NoError(t, runtimeStore.SaveState(ctx, &RuntimeState{
		SessionID: session.ID,
		Status:    SessionRunning,
	}))
	actor := &SessionActor{
		id:           session.ID,
		stateStore:   runtimeStore,
		eventStore:   runtimeStore,
		eventBus:     runtimeevents.NewBus(),
		sessionStore: storage,
	}
	require.NoError(t, actor.loadState(ctx))

	reply := make(chan error, 1)
	actor.handleInterrupt(Interrupt{Reply: reply})
	require.NoError(t, <-reply)

	events, err := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
	require.NoError(t, err)
	var interrupted map[string]interface{}
	for _, event := range events {
		if event.Type == EventSessionInterrupted {
			interrupted = event.Payload
		}
	}
	require.NotNil(t, interrupted, "handleInterrupt 必须发布 session_interrupted")
	require.Equal(t, "interrupt", interrupted["reason"])
	require.Equal(t, "halfway deliverable before ESC", interrupted["partial_summary"])
	require.Equal(t, "last_assistant_message", interrupted["partial_source"])
	require.Equal(t, 1, interrupted["partial_steps"])
}

// TestSessionActorCanceledRunCarriesPartialProduct 是 A4 的端到端回归：一个已经
// 产出过工作的 run 被取消时，session_end 载荷必须带上部分产物（而不是让父代理
// 只看到 "context canceled"）。
func TestSessionActorCanceledRunCarriesPartialProduct(t *testing.T) {
	ctx := context.Background()
	storage := NewInMemoryStorage()
	manager := NewSessionManager(storage, nil)
	session, err := manager.CreateSession(ctx, "actor-partial-product-user")
	require.NoError(t, err)

	provider := &replyThenBlockLLMProvider{name: "partial-product-provider", entered: make(chan struct{}, 1)}
	runtime := llm.NewLLMRuntime(&llm.RuntimeConfig{DefaultModel: "test-model", MaxRetries: 1})
	require.NoError(t, runtime.RegisterProvider(provider.Name(), provider))

	apiAgent := agent.NewAgentWithLLM(&agent.Config{
		Name:     "actor-partial-product-test",
		Provider: provider.Name(),
		Model:    "test-model",
		MaxSteps: 3,
	}, nil, runtime)
	runtimeStore := NewInMemoryRuntimeStore(64)
	actor, err := NewSessionActor(session.ID, SessionActorConfig{
		Agent:        apiAgent,
		LLMRuntime:   runtime,
		SessionStore: storage,
		StateStore:   runtimeStore,
		EventStore:   runtimeStore,
	})
	require.NoError(t, err)
	t.Cleanup(actor.Stop)

	// 第一轮成功产出；它的 assistant 消息就是随后被取消的 run 可以挽救的产物。
	first, err := actor.SubmitPrompt(ctx, "produce the first deliverable", nil)
	require.NoError(t, err)
	require.NotNil(t, first)

	responseCh := make(chan error, 1)
	submitCtx, cancelSubmit := context.WithCancel(ctx)
	go func() {
		_, submitErr := actor.SubmitPrompt(submitCtx, "now block until canceled", nil)
		responseCh <- submitErr
	}()

	select {
	case <-provider.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start the blocked second call")
	}
	cancelSubmit()
	select {
	case submitErr := <-responseCh:
		require.ErrorIs(t, submitErr, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("SubmitPrompt did not return after cancel")
	}

	// 被取消的 run 在 SubmitPrompt 返回后才异步落终态事件，按既有 parent-cancel
	// 测试的节奏等待带 cancel_source 的 session_end 出现。
	var sessionEnd map[string]interface{}
	require.Eventually(t, func() bool {
		events, listErr := runtimeStore.ListEvents(ctx, session.ID, 0, 0)
		if listErr != nil {
			return false
		}
		for _, event := range events {
			if event.Type != EventSessionEnd {
				continue
			}
			if _, canceled := event.Payload["cancel_source"]; !canceled {
				continue
			}
			sessionEnd = event.Payload
		}
		return sessionEnd != nil
	}, 3*time.Second, 20*time.Millisecond, "the canceled run must publish a session_end carrying cancel_source")
	t.Logf("canceled session_end: success=%v cancel_source=%v partial=%v",
		sessionEnd["success"], sessionEnd["cancel_source"], sessionEnd["partial_summary"] != nil)

	summary, _ := sessionEnd["partial_summary"].(string)
	require.Contains(t, summary, "first-turn deliverable",
		"a canceled run must publish the product it already produced")
	require.Equal(t, "last_assistant_message", sessionEnd["partial_source"])
	require.NotEmpty(t, sessionEnd["cancel_source"],
		"the partial product must ride the canceled run's terminal payload")
}

// replyThenBlockLLMProvider answers the first call with a normal deliverable and
// blocks on every later call until the context is canceled.
type replyThenBlockLLMProvider struct {
	name    string
	entered chan struct{}
	calls   int32
}

func (p *replyThenBlockLLMProvider) Name() string { return p.name }

func (p *replyThenBlockLLMProvider) Call(ctx context.Context, req *llm.LLMRequest) (*llm.LLMResponse, error) {
	if atomic.AddInt32(&p.calls, 1) == 1 {
		return &llm.LLMResponse{
			Content: "first-turn deliverable",
			Usage:   &types.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
			Model:   "test-model",
		}, nil
	}
	select {
	case p.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func (p *replyThenBlockLLMProvider) Stream(ctx context.Context, req *llm.LLMRequest) (<-chan llm.StreamChunk, error) {
	return nil, fmt.Errorf("streaming is not supported")
}

func (p *replyThenBlockLLMProvider) CountTokens(text string) int { return len(text) / 4 }

func (p *replyThenBlockLLMProvider) GetCapabilities() *llm.ModelCapabilities {
	return &llm.ModelCapabilities{MaxContextTokens: 128000, SupportsTools: true}
}

func (p *replyThenBlockLLMProvider) CheckHealth(ctx context.Context) error { return nil }

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
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestPartialRunProductPrefersResultOutputThenHistory 钉住 A4 的取源优先级：
// 当前 run 的部分输出优先于历史里的最后一条 assistant 消息；两者都没有时返回空
// summary，但 partial_steps 仍如实报告已完成步骤。
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

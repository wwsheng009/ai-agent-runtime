package commands

// A6 根因回归（2026-09-27 会话 session_20260927073805_QbWBceF5 实测）：
// 运行时刷新（模型/provider/reasoning/路由/目录切换）过去无条件 StopContext，
// 经 actor.cancelActive 以 actor_stop 取消在途 run，用户只看到 context canceled。
// 本文件钉住修复后的契约：
//   - actor 在途 → 不驱逐、不打断，只登记延迟重建（回合入口兑现）；
//   - actor 空闲 + 选择变化 → 立即整包刷新（旧行为保留）；
//   - actor 空闲 + 同值重选 → 保留 actor，只做配置级对账。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// gatedRuntimeRefreshProvider 在 release 关闭前不返回，让测试稳定地落在
// "run 在途"窗口内触发运行时刷新。
type gatedRuntimeRefreshProvider struct {
	release chan struct{}
}

func (p *gatedRuntimeRefreshProvider) Name() string { return "mock" }

func (p *gatedRuntimeRefreshProvider) Call(ctx context.Context, req *runtimellm.LLMRequest) (*runtimellm.LLMResponse, error) {
	select {
	case <-p.release:
		return &runtimellm.LLMResponse{Content: "released ok", Model: req.Model}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *gatedRuntimeRefreshProvider) Stream(ctx context.Context, req *runtimellm.LLMRequest) (<-chan runtimellm.StreamChunk, error) {
	ch := make(chan runtimellm.StreamChunk, 2)
	go func() {
		defer close(ch)
		resp, err := p.Call(ctx, req)
		if err != nil {
			ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeError, Error: err.Error()}
			return
		}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeText, Content: resp.Content}
		ch <- runtimellm.StreamChunk{Type: runtimellm.EventTypeDone, Done: true}
	}()
	return ch, nil
}

func (p *gatedRuntimeRefreshProvider) CountTokens(text string) int { return len(text) }

func (p *gatedRuntimeRefreshProvider) GetCapabilities() *runtimellm.ModelCapabilities {
	return &runtimellm.ModelCapabilities{
		MaxContextTokens:  128000,
		MaxOutputTokens:   4096,
		SupportsTools:     true,
		SupportsStreaming: true,
	}
}

func (p *gatedRuntimeRefreshProvider) CheckHealth(ctx context.Context) error { return nil }

func newRuntimeRefreshTestSession(hub *runtimechat.SessionHub) *ChatSession {
	return &ChatSession{
		ProviderName:     "mock",
		Model:            "mock",
		RuntimeSession:   &runtimechat.Session{ID: "session-1"},
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
}

func waitForRuntimeRefreshRunIdle(t *testing.T, actor *runtimechat.SessionActor) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !actor.RunInFlight() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("actor run did not finish in time")
}

func TestRuntimeRefreshDoesNotInterruptInFlightTurn(t *testing.T) {
	provider := &gatedRuntimeRefreshProvider{release: make(chan struct{})}
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newRuntimeRefreshTestSession(hub)
	actor, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	type runOutcome struct {
		result *agent.Result
		err    error
	}
	done := make(chan runOutcome, 1)
	go func() {
		result, runErr := actor.SubmitPrompt(context.Background(), "hold", nil)
		done <- runOutcome{result: result, err: runErr}
	}()
	waitForTestActorBusy(t, actor)

	if err := refreshLocalRuntimeAfterSelection(session, true, chatActorRebuildReasonModelSelection); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, ok := hub.Get("session-1"); !ok {
		t.Fatal("in-flight runtime refresh must keep the actor alive (never interrupt an in-flight turn)")
	}
	if !session.actorRebuildPending {
		t.Fatal("in-flight runtime refresh must leave a deferred rebuild marker")
	}
	if session.actorRebuildReason != chatActorRebuildReasonModelSelection {
		t.Fatalf("deferred reason = %q, want %q", session.actorRebuildReason, chatActorRebuildReasonModelSelection)
	}

	close(provider.release)
	outcome := <-done
	if outcome.err != nil {
		t.Fatalf("in-flight turn was aborted by runtime refresh: %v", outcome.err)
	}
	if outcome.result == nil || !outcome.result.Success {
		t.Fatalf("in-flight turn did not complete successfully: %#v", outcome.result)
	}

	// 回合入口兑现：actor 空闲后驱逐旧 actor（延迟标记被消费），下一轮 GetOrCreate 重建。
	waitForRuntimeRefreshRunIdle(t, actor)
	reconcilePendingChatActorRebuild(session)
	if session.actorRebuildPending {
		t.Fatal("reconcile must consume the deferred rebuild marker")
	}
	if _, ok := hub.Get("session-1"); ok {
		t.Fatal("reconcile must evict the stale actor once the turn finished")
	}
}

func TestRuntimeRefreshEvictsIdleActorWhenSelectionChanged(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("hello", "ok")
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newRuntimeRefreshTestSession(hub)
	idle, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	// 模拟一次真实的模型切换：现值相对快照发生变化。
	before := snapshotChatRuntimeSelection(session)
	session.Model = "mock-next"
	if !before.changed(session) {
		t.Fatal("sanity: a different model must be reported as changed")
	}

	if err := refreshLocalRuntimeAfterSelection(session, before.changed(session), chatActorRebuildReasonModelSelection); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if session.actorRebuildPending {
		t.Fatal("idle refresh must not leave a deferred rebuild marker")
	}
	if current, ok := hub.Get("session-1"); ok && current == idle {
		t.Fatal("idle refresh with a changed selection must evict the stale actor")
	}
}

func TestRuntimeRefreshKeepsActorForUnchangedSelection(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 0)
	provider.SetResponse("hello", "ok")
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newRuntimeRefreshTestSession(hub)
	idle, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	before := snapshotChatRuntimeSelection(session)
	if before.changed(session) {
		t.Fatal("sanity: a fresh snapshot must not report a change")
	}

	if err := refreshLocalRuntimeAfterSelection(session, before.changed(session), chatActorRebuildReasonModelSelection); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if session.actorRebuildPending {
		t.Fatal("same-value reselect must not leave a deferred rebuild marker")
	}
	current, ok := hub.Get("session-1")
	if !ok || current != idle {
		t.Fatal("same-value reselect must keep the current actor (no needless agent rebuild)")
	}
}

// 路由写入撞上在途 turn：TUI 必须如实显示"自下一轮生效"，而不是静默当已刷新。
func TestChatRoutingRefreshAfterWriteReportsDeferralWhileTurnInFlight(t *testing.T) {
	provider := &gatedRuntimeRefreshProvider{release: make(chan struct{})}
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newRuntimeRefreshTestSession(hub)
	actor, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, runErr := actor.SubmitPrompt(context.Background(), "hold", nil)
		done <- runErr
	}()
	waitForTestActorBusy(t, actor)

	note := chatRoutingRefreshRuntimeAfterWrite(session)
	if !strings.Contains(note, "在途 turn") {
		t.Fatalf("路由写入撞上在途 turn 必须如实提示延迟生效，got %q", note)
	}
	if _, ok := hub.Get("session-1"); !ok {
		t.Fatal("在途 actor 不得被驱逐（A6：绝不打断在途回合）")
	}
	if session.actorRebuildReason != chatActorRebuildReasonRoutingWrite {
		t.Fatalf("deferred reason = %q, want %q", session.actorRebuildReason, chatActorRebuildReasonRoutingWrite)
	}

	close(provider.release)
	if runErr := <-done; runErr != nil {
		t.Fatalf("in-flight turn was aborted by routing refresh: %v", runErr)
	}
	waitForRuntimeRefreshRunIdle(t, actor)
	reconcilePendingChatActorRebuild(session)
	if _, ok := hub.Get("session-1"); ok {
		t.Fatal("边界兑现后旧 actor 必须被驱逐，使下一轮按新路由构建")
	}
}

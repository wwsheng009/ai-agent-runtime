package commands

// 修复回归（实测发现）：本地 chat 的回合由 actor 执行，agent 的工具执行策略在
// buildSessionActor 期由 session.ToolPolicy 固化。只清稳定工具面缓存不驱逐 actor，
// 下一个 turn 的 provider 请求仍带旧工具面（实测：`/profile use` 后 45 → 45，
// 而同 profile 走启动路径是 26）。本测试钉住失效动作的机制面：
//   空闲切换 → 立即驱逐（下一次 GetOrCreate 重建）；
//   在途切换 → 不驱逐 + 登记延迟重建 → 回合入口 reconcile 兑现。

import (
	"testing"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func newActorProfileSwitchTestSession(t *testing.T, hub *runtimechat.SessionHub) *ChatSession {
	t.Helper()
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-1"
	return &ChatSession{
		RuntimeSession:   runtimeSession,
		SessionUserID:    "tester",
		LocalRuntimeHost: &localChatRuntimeHost{SessionHub: hub},
	}
}

func TestProfileSwitchEvictsIdleActorSoNextTurnRebuildsAgent(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 0)
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newActorProfileSwitchTestSession(t, hub)
	if _, err := hub.GetOrCreate("session-1"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	scope, invalidated, evicted := invalidateChatProfileRuntime(session, false)
	if scope != profileSwitchSurfaceScopeActor {
		t.Fatalf("scope = %q, want %q", scope, profileSwitchSurfaceScopeActor)
	}
	if !invalidated {
		t.Fatal("expected stable tool surface to be invalidated")
	}
	if !evicted {
		t.Fatal("idle profile switch must evict the actor that froze the old tool policy")
	}
	if _, ok := hub.Get("session-1"); ok {
		t.Fatal("evicted actor must be gone so the next GetOrCreate rebuilds it")
	}
	if session.profileRebuildPending {
		t.Fatal("a fulfilled eviction must not leave a pending rebuild marker")
	}
	if actor, err := hub.GetOrCreate("session-1"); err != nil || actor == nil {
		t.Fatalf("next GetOrCreate must rebuild the actor: actor=%v err=%v", actor, err)
	}
}

func TestProfileSwitchInFlightActorDefersRebuildToTurnEntry(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 0)
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newActorProfileSwitchTestSession(t, hub)
	if _, err := hub.GetOrCreate("session-1"); err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	scope, invalidated, evicted := invalidateChatProfileRuntime(session, true)
	if scope != profileSwitchSurfaceScopeActor || !invalidated {
		t.Fatalf("unexpected scope/invalidated: %q/%v", scope, invalidated)
	}
	if evicted {
		t.Fatal("an in-flight turn must never be interrupted by actor eviction (D18/A3)")
	}
	if !session.profileRebuildPending {
		t.Fatal("in-flight switch must leave a pending rebuild marker")
	}
	if _, ok := hub.Get("session-1"); !ok {
		t.Fatal("in-flight switch must keep the current actor alive")
	}

	// 回合入口兑现：本轮结束（actor 空闲）后驱逐，让本轮的 GetOrCreate 重建。
	reconcilePendingChatProfileRebuild(session)
	if session.profileRebuildPending {
		t.Fatal("reconcile must consume the pending marker")
	}
	if _, ok := hub.Get("session-1"); ok {
		t.Fatal("reconcile must evict the stale actor once it is idle")
	}
}

func TestProfileSwitchWithoutRuntimeHostReportsNone(t *testing.T) {
	session := &ChatSession{}
	runtimeSession := runtimechat.NewSession("tester")
	runtimeSession.ID = "session-1"
	session.RuntimeSession = runtimeSession

	scope, invalidated, evicted := invalidateChatProfileRuntime(session, false)
	if scope != profileSwitchSurfaceScopeNone || invalidated || evicted {
		t.Fatalf("no runtime host must report none/false/false, got %q/%v/%v", scope, invalidated, evicted)
	}
}

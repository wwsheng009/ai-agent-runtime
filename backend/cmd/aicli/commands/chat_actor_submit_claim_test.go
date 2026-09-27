package commands

// P2 回归（docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md）：
// 提交认领（submit claim）保护"已取得 actor、但尚未 BeginRun"的预跑窗口。
// 该窗口内运行期刷新若把 actor 驱逐（空闲判定成立），提交会从 Stabilized 的
// actor 上开始运行——静默撕裂的候选机制之一。契约：认领期间刷新必须改为登记
// 延迟重建（下一轮兑现），绝不驱逐。

import (
	"testing"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

func TestActorSubmitClaimLifecycle(t *testing.T) {
	host := &localChatRuntimeHost{} // 结构体字面量：认领状态必须惰性初始化
	if host.actorSubmitClaimed("s1") {
		t.Fatal("fresh host must not report a claim")
	}

	release1 := host.beginActorSubmitClaim("s1")
	release2 := host.beginActorSubmitClaim("s1")
	if !host.actorSubmitClaimed("s1") {
		t.Fatal("claim must be visible after begin")
	}
	release1()
	release1() // 幂等：重复释放不得把叠加的认领清掉
	if !host.actorSubmitClaimed("s1") {
		t.Fatal("releasing one claim twice must not drop the remaining claim")
	}
	release2()
	if host.actorSubmitClaimed("s1") {
		t.Fatal("claims must clear once every holder releases")
	}
	release1()
	if host.actorSubmitClaimed("s1") {
		t.Fatal("stale release must not corrupt claim state")
	}
	if host.actorSubmitClaimed("") {
		t.Fatal("empty session id must never report a claim")
	}

	// nil host / nil session 包装必须安全（读路径与释放路径都不得 panic）。
	var nilHost *localChatRuntimeHost
	nilHost.beginActorSubmitClaim("s1")()
	if nilHost.actorSubmitClaimed("s1") {
		t.Fatal("nil host must not report a claim")
	}
	beginChatActorSubmitClaim(nil)()
	if chatActorSubmitClaimed(nil) {
		t.Fatal("nil session must not report a claim")
	}
}

func TestRuntimeRefreshDefersWhileSubmitClaimed(t *testing.T) {
	provider := runtimellm.NewMockProvider("mock", 0)
	hub := buildTestSessionHubWithProvider(t, provider)
	t.Cleanup(hub.StopAll)

	session := newRuntimeRefreshTestSession(hub)
	idle, err := hub.GetOrCreate("session-1")
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}

	release := beginChatActorSubmitClaim(session)
	if err := refreshLocalRuntimeAfterSelection(session, true, chatActorRebuildReasonModelSelection); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	kept, ok := hub.Get("session-1")
	if !ok || kept != idle {
		t.Fatal("runtime refresh must not evict the actor while a submit claim is held")
	}
	if !session.actorRebuildPending || session.actorRebuildReason != chatActorRebuildReasonModelSelection {
		t.Fatalf("deferred refresh must be recorded (pending=%v reason=%q)",
			session.actorRebuildPending, session.actorRebuildReason)
	}

	release()
	reconcilePendingChatActorRebuild(session)
	if session.actorRebuildPending {
		t.Fatal("reconcile must consume the deferred rebuild marker")
	}
	if _, ok := hub.Get("session-1"); ok {
		t.Fatal("deferred refresh must evict the stale actor on the next turn entry")
	}
}

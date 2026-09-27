package commands

// P3 回归（docs/plan/aicli-chat-submit-run-epoch-wedge-hardening.md）：
// 复刻事故序列 —— `runtime_refresh:model` 在 actor 空闲时把它整包驱逐，
// 随后用户提交。契约：
//  1. 提交必须重建 actor 而不是静默丢弃；
//  2. 必须开启新的 run epoch（BeginRun），turn 结束后收敛（runActive=false）；
//  3. 等待态（Analyzing）在 turn 结束后不得残留——这是"幽灵假忙"的直接指纹。

import (
	"context"
	"strings"
	"testing"

	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
)

func TestSubmitAfterIdleRuntimeRefreshEvictStartsFreshRun(t *testing.T) {
	manager, userID, dir, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	runtimeSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}

	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: t.TempDir() + "/team.db"})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	provider := runtimellm.NewMockProvider("test-provider", 0)
	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{
		DefaultProvider: "test-provider",
		DefaultModel:    "test-model",
	})
	if err := llmRuntime.RegisterProvider("test-provider", provider); err != nil {
		t.Fatalf("RegisterProvider: %v", err)
	}
	if err := llmRuntime.RegisterProviderAlias("test-model", "test-provider"); err != nil {
		t.Fatalf("RegisterProviderAlias: %v", err)
	}

	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	t.Cleanup(host.SessionHub.StopAll)

	session := &ChatSession{
		ProviderName:     "test-provider",
		PermissionMode:   runtimepolicy.ModeDefault,
		Model:            "test-model",
		SessionManager:   manager,
		RuntimeSession:   runtimeSession,
		SessionUserID:    userID,
		SessionDir:       dir,
		LocalRuntimeHost: host,
		ChatExecutor:     newAICLIActorChatExecutor(),
		cancelCtx:        context.Background(),
	}
	host.BaseSession = session
	coord := newTestChatInteractionCoordinator(t, session)
	t.Cleanup(coord.Shutdown)
	session.Interaction = coord

	// 1) 事故前置：actor 已建好且空闲。
	idleActor, err := host.SessionHub.GetOrCreate(runtimeSession.ID)
	if err != nil {
		t.Fatalf("GetOrCreate: %v", err)
	}
	// 2) 复刻 runtime_refresh:model：空闲 actor 被整包驱逐。
	if err := refreshLocalRuntimeAfterSelection(session, true, chatActorRebuildReasonModelSelection); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if current, ok := host.SessionHub.Get(runtimeSession.ID); ok && current == idleActor {
		t.Fatal("sanity: idle runtime refresh must evict the stale actor")
	}

	// 3) 用户提交：必须重建 actor、开新 run 并完成（不得静默丢弃）。
	response, err := sendMessage(session, "hello")
	if err != nil {
		t.Fatalf("submit after evict must not fail: %v", err)
	}
	if strings.TrimSpace(response) == "" {
		t.Fatal("submit after evict returned an empty response")
	}
	rebuilt, ok := host.SessionHub.Get(runtimeSession.ID)
	if !ok || rebuilt == idleActor {
		t.Fatal("submit after evict must rebuild the actor")
	}

	// 4) 提交-运行协议一致性：epoch 已开、run 已收敛、等待态无残留。
	bridge := session.RuntimeEventBridge
	if bridge == nil {
		t.Fatal("submit must install the runtime event bridge")
	}
	if bridge.RunEpoch() == 0 {
		t.Fatal("submit after evict must open a fresh run epoch")
	}
	if bridge.RunActive() {
		t.Fatal("run must be closed once the turn returns")
	}
	if coord.WaitingArmed() {
		t.Fatal("waiting/Analyzing must be cleared once the turn returns")
	}
}

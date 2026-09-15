package commands

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	runtimellm "github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/team"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolbroker"
)

// TestLocalActorRegistry_PartialAgentsConfigKeepsDepthCeiling 覆盖「部分写入的
// `agents:` 块」这一组合语义（方案 附录 B 未决项 4）：只写了 maxThreads 时 maxDepth
// 仍是未设置（0），必须回落到内置默认上限，而不是被读成「无上限」。
//
// 修复前 localAgentsConfig() 的「整个结构体全零才回退默认」短路会被这份配置绕过：
// enforceLocalAgentSpawnLimits 看到 MaxDepth=0，`limits.MaxDepth > 0` 不成立，于是整棵
// 子树没有深度上限（fail-open）。本用例断言孙会话（depth=2）仍被默认上限挡住。
func TestLocalActorRegistry_PartialAgentsConfigKeepsDepthCeiling(t *testing.T) {
	manager, userID, _, err := newChatSessionManager(t.TempDir())
	if err != nil {
		t.Fatalf("newChatSessionManager: %v", err)
	}
	defer manager.Stop()

	rootSession, err := manager.Create(context.Background(), userID)
	if err != nil {
		t.Fatalf("manager.Create: %v", err)
	}
	teamStore, err := team.NewSQLiteStore(&team.StoreConfig{Path: filepath.Join(t.TempDir(), "team.db")})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	defer teamStore.Close()

	llmRuntime := runtimellm.NewLLMRuntime(&runtimellm.RuntimeConfig{})
	host := newLocalOrchestrationTestHost(t, manager, userID, llmRuntime, teamStore)
	host.RuntimeConfig = runtimecfg.DefaultRuntimeConfig()
	// 部分写入：maxDepth / wait / reconcile 等字段全部留空。
	host.RuntimeConfig.Agents = runtimecfg.AgentsConfig{MaxThreads: 3}
	host.BaseSession = &ChatSession{
		RuntimeSession: rootSession,
		SessionUserID:  userID,
	}
	registry := host.ActorRegistry

	if _, err := registry.Spawn(context.Background(), rootSession.ID, toolbroker.SpawnAgentArgs{ID: "partial-parent"}); err != nil {
		t.Fatalf("spawn parent: %v", err)
	}

	defaultCeiling := runtimecfg.NormalizeAgentsConfig(runtimecfg.AgentsConfig{}).MaxDepth
	_, err = registry.Spawn(context.Background(), "partial-parent", toolbroker.SpawnAgentArgs{ID: "partial-child"})
	if err == nil {
		t.Fatalf("expected the built-in depth ceiling (%d) to apply to a partially configured agents block", defaultCeiling)
	}
	for _, want := range []string{"depth limit", "requested_child_depth=2", "SPAWN_DEPTH_LIMIT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected depth-limit diagnostic containing %q, got %v", want, err)
		}
	}

	// 默认上限是「未设置」的回落值，显式写入更大的上限仍然生效。
	host.RuntimeConfig.Agents.MaxDepth = 2
	if _, err := registry.Spawn(context.Background(), "partial-parent", toolbroker.SpawnAgentArgs{ID: "partial-child"}); err != nil {
		t.Fatalf("explicit maxDepth=2 must allow depth 2: %v", err)
	}
}

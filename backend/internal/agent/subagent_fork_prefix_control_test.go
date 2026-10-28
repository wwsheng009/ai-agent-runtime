package agent

// subagent_fork_prefix_control_test.go — §6.4-5 对照实验（agent 侧接线）：
// fork 子代理的**可缓存前缀身份**由"父快照 + route + 工具面 + 目标"共同决定，
// 因此对照必须同时覆盖"完全相同"与"某一维度不同"两组：
//
//   - 完全相同（同快照/同 route/同工具面/同目标）→ 首请求 messages/tools 逐项一致
//     （provider 侧前缀缓存具备命中的字节前提）；
//   - 工具面不同 → 前缀身份不同（Tools 变化）；
//   - fresh vs fork → 前缀不同（不含父历史）；
//   - 快照哈希只描述"父快照身份"：工具面不同不影响它（快照哈希相同 ≠ 缓存前缀相同）。
//
// provider 侧是否真的对"同前缀"报缓存命中，取决于 upstream 策略（最小可缓存长度/
// TTL/namespace），本地无法确定性复现；本用例固定的是**我们能控制的一半**：把可缓存
// 前缀构造得可对照、可解释（配合 fork_snapshot_hash / parent_prompt_cache_key 对账）。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// newForkControlRig 与 newAttemptEventRig 同形，但**接线 ParentHistoryFor**——
// fork 任务的父历史快照由宿主提供，缺它时 fork 子代理会以可操作错误失败（不是
// 静默降级 fresh）。
func newForkControlRig(t *testing.T) (*Agent, chan BatchTerminalLifecycle, *runtimeevents.Bus) {
	t.Helper()
	agent := &Agent{
		config: &Config{
			Name:         "test-agent",
			Model:        "test-provider",
			MaxSteps:     4,
			SystemPrompt: "You are a helpful assistant.",
		},
		skillRouter: &skill.Router{},
		skillExec:   &skill.Executor{},
		// 真实内置工具面（view/grep/glob…）：工具面参与可缓存前缀，对照实验必须
		// 让白名单解析出非空且可区分的工具集合（MockMCPManager 下解析为空）。
		mcpManager: runtimetools.NewAgentAdapter(runtimetools.NewDefaultManager(nil)),
	}
	bus := runtimeevents.NewBus()
	agent.SetEventBus(bus)
	terminal := make(chan BatchTerminalLifecycle, 1)
	// ForkContext 默认关闭：fork 请求会被准入拒绝（可操作错误，不静默降级），
	// 对照实验必须先授予能力位。
	caps := DefaultCapabilities()
	caps.ForkContext = true
	scheduler := NewSubagentScheduler(agent, SubagentSchedulerConfig{
		MaxConcurrent: 4,
		MaxDepth:      1,
		Capabilities:  &caps,
	})
	agent.SetSubagentScheduler(scheduler)
	store, err := subagentbatch.NewSQLiteBatchStore(&subagentbatch.StoreConfig{
		Path: filepath.Join(t.TempDir(), "batches.db"),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	agent.SetSubagentBatchCoordinator(NewSubagentBatchCoordinator(SubagentBatchCoordinatorConfig{
		Store:     store,
		Scheduler: scheduler,
		// §6.4-5 对照必需：fork 快照数据源。
		ParentHistoryFor: func(context.Context, string) []types.Message { return forkFixture() },
	}))
	agent.SetBatchLifecycleProjector(func(_ context.Context, event BatchTerminalLifecycle) error {
		terminal <- event
		return nil
	})
	return agent, terminal, bus
}

func TestForkChildPrefixIdentityControls(t *testing.T) {
	agent, terminal, bus := newForkControlRig(t)

	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	provider := &roleRoutingLLMProvider{
		SequenceLLMProvider: &SequenceLLMProvider{name: "test-provider"},
		childResponses: []*llm.LLMResponse{
			{Content: "child one done", Model: "test-model"},
			{Content: "child two done", Model: "test-model"},
			{Content: "child three done", Model: "test-model"},
			{Content: "child four done", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	agent.llmRuntime = llmRuntime

	var mu sync.Mutex
	resolved := map[string]map[string]interface{}{}
	bus.Subscribe(runtimeevents.EventSubagentRouteResolved, func(event runtimeevents.Event) {
		mu.Lock()
		defer mu.Unlock()
		if id, _ := event.Payload["subagent_id"].(string); id != "" {
			resolved[id] = event.Payload
		}
	})

	const goal = "summarize the recent decisions"
	const parentKey = "parent-fork-ctl#prompt-cache-epoch-1"
	coordinator := agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID:      "parent-fork-ctl",
		RootScopeID:          "parent-fork-ctl",
		ExecutionMode:        subagentbatch.ExecutionModeBackground,
		ParentPromptCacheKey: parentKey,
	}, []SubagentTask{
		// c1/c2：完全相同（同快照、同 route、同工具面、同目标）。
		{ID: "c1", Goal: goal, ReadOnly: true, Context: SubagentContextFork, ToolsWhitelist: []string{"view", "grep"}},
		{ID: "c2", Goal: goal, ReadOnly: true, Context: SubagentContextFork, ToolsWhitelist: []string{"view", "grep"}},
		// c3：仅工具面不同。
		{ID: "c3", Goal: goal, ReadOnly: true, Context: SubagentContextFork, ToolsWhitelist: []string{"view", "glob"}},
		// c4：fresh（同 route、同工具面、同目标，但不注入父历史）。
		{ID: "c4", Goal: goal, ReadOnly: true, ToolsWhitelist: []string{"view", "grep"}},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	select {
	case <-terminal:
	case <-time.After(15 * time.Second):
		t.Fatal("fork 对照批次未在 15s 内收敛")
	}

	requests := provider.childRequestSnapshot()
	require.Len(t, requests, 4, "四个 child 各恰一次首请求：%d", len(requests))
	byTask := map[string]*llm.LLMRequest{}
	keys := map[string]string{}
	for _, req := range requests {
		key, _ := req.Metadata["prompt_cache_key"].(string)
		key = strings.TrimSpace(key)
		require.NotEmpty(t, key)
		for _, id := range []string{"c1", "c2", "c3", "c4"} {
			if strings.Contains(key, "subagent_"+id+"_") {
				byTask[id] = req
				keys[id] = key
			}
		}
	}
	require.Len(t, byTask, 4, "每个 child 的首请求必须可按 session 归位")

	// 0) key 隔离：同目标/同快照也不共享 key（按 session 派生）。
	distinct := map[string]bool{}
	for _, key := range keys {
		distinct[key] = true
	}
	require.Len(t, distinct, 4, "四个 child 的 prompt_cache_key 必须互不相同：%v", keys)

	// 1) 相同对照：前缀逐项一致（provider 侧缓存命中的字节前提）。
	require.Equal(t, byTask["c1"].Messages, byTask["c2"].Messages,
		"同快照/同 route/同工具面/同目标的 fork child 首请求 messages 必须逐项一致")
	require.Equal(t, byTask["c1"].Tools, byTask["c2"].Tools)
	require.Equal(t, byTask["c1"].Provider, byTask["c2"].Provider)
	require.Equal(t, byTask["c1"].Model, byTask["c2"].Model)

	// 2) 工具面不同：前缀身份不同（工具 schema 参与可缓存前缀）。
	require.NotEqual(t, byTask["c1"].Tools, byTask["c3"].Tools,
		"工具面不同的 child 前缀身份必须不同")

	// 3) fresh vs fork：父历史注入是前缀差异的来源。
	require.Less(t, len(byTask["c4"].Messages), len(byTask["c1"].Messages),
		"fresh child 不应携带父历史（更短的前缀）")

	// 4) 父 key 对账：同一批次所有 child 携带同一父 epoch key。
	for _, req := range requests {
		require.Equal(t, parentKey, req.Metadata["parent_prompt_cache_key"])
	}

	// 5) 快照哈希只描述父快照身份：同快照的三个 fork child 一致，但**不代表**
	//    缓存前缀相同（c3 工具面不同）——这正是需要对照测试保留的结论。
	mu.Lock()
	defer mu.Unlock()
	require.NotNil(t, resolved["c1"])
	hash := fmt.Sprint(resolved["c1"]["fork_snapshot_hash"])
	require.NotEmpty(t, hash)
	require.Equal(t, hash, fmt.Sprint(resolved["c2"]["fork_snapshot_hash"]))
	require.Equal(t, hash, fmt.Sprint(resolved["c3"]["fork_snapshot_hash"]),
		"快照哈希不含工具面：工具面不同不改变快照身份")
	// fresh 的载荷可不携带该键（nil 与空串同义：都表示"无快照指纹"）。
	require.Empty(t, resolved["c4"]["fork_snapshot_hash"], "fresh 无快照指纹")
	require.Equal(t, false, resolved["c4"]["parent_history_injected"])
	require.Equal(t, "fork", resolved["c1"]["effective_context"])
	require.Equal(t, "fresh", resolved["c4"]["effective_context"])
}

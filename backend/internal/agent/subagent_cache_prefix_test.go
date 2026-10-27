package agent

// subagent_cache_prefix_test.go — §8.4 两项 cache 专项：
//   - 同一个 child 的 retry 是否复用预期前缀（调度层接线：同 key/epoch/前缀字节）；
//   - 并发 child 是否按 session 隔离 cache key（不因目标相同或父子同批而错误共享）。
//
// provider 侧的请求体字节面（扩容重试保持 messages/tools 前缀）由 llm 包的
// TestProviderRetryWidenedMaxTokensKeepsCachePrefix 固定；本文件固定 agent/
// 调度层的接线，两者合起来才构成「重试复用预期前缀」的端到端证据。

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/subagentbatch"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestSubagentRetryReusesExpectedCachePrefix 锁定 §8.4：注入第一次子请求
// UPSTREAM_UNAVAILABLE 后，任务级 attempt 2 必须复用 attempt 1 的「预期前缀」：
//   - 同一 child session 的 prompt-cache key/epoch 不因重试变化（缓存命名空间一致）；
//   - messages 与 tools 逐项一致（provider 侧前缀缓存只能在这种前提下命中）；
//   - provider/model 一致（前缀缓存按 provider 隔离，换 provider 的"重试"不可能复用）；
//   - attempt 2 的缓存命中可观测并计入任务回执。
func TestSubagentRetryReusesExpectedCachePrefix(t *testing.T) {
	rig := newAttemptEventRig(t)

	llmRuntime := llm.NewLLMRuntime(nil)
	// 关掉 runtime 层重试：每次任务 attempt 恰好一次 provider 调用，否则 provider
	// 层重试会吃掉 attempt 2 的成功响应，任务级重试无法被观测。
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	baseProvider := &SequenceLLMProvider{
		name: "test-provider",
		responses: []*llm.LLMResponse{
			{
				Content: "I will delegate.",
				Model:   "test-model",
				ToolCalls: []types.ToolCall{
					{
						Name: "spawn_subagents",
						Args: map[string]interface{}{
							"agents": []interface{}{
								map[string]interface{}{
									"id":        "cache-child",
									"goal":      "Inspect cache prefix reuse",
									"read_only": true,
								},
							},
						},
					},
				},
			},
			{Content: "Final answer from parent.", Model: "test-model"},
		},
	}
	provider := &firstChildFailureProvider{
		roleRoutingLLMProvider: &roleRoutingLLMProvider{
			SequenceLLMProvider: baseProvider,
			childResponses: []*llm.LLMResponse{
				{
					Content: "Recovered child answer.",
					Model:   "test-model",
					Usage: &types.TokenUsage{
						PromptTokens:      30,
						CompletionTokens:  5,
						TotalTokens:       35,
						CachedTokens:      20,
						CacheReadTokens:   20,
						InputTotalTokens:  50,
						CacheReadReported: true,
					},
				},
			},
		},
		errText: "UPSTREAM_UNAVAILABLE: injected transient child failure (cache prefix reuse test)",
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))

	loop := NewReActLoop(rig.agent, llmRuntime, &LoopReActConfig{
		MaxSteps:        4,
		EnableThought:   true,
		EnableToolCalls: true,
	})
	result, err := loop.Run(context.Background(), "Find the root cause.")
	require.NoError(t, err)
	require.True(t, result.Success)

	rig.waitTerminal(t)

	// 失败的那次也要看得到：firstChildFailureProvider 在注入失败时未下发到内层
	// provider，故用它的 observedChildRequests（含失败请求）。
	requests := provider.observedChildRequests()
	require.Len(t, requests, 2,
		"attempt 1（注入失败）与 attempt 2（成功）各恰一次子请求；实际 %d（provider 调用 %d 次）",
		len(requests), provider.callCount())
	first, second := requests[0], requests[1]
	require.Equal(t, 1, intValue(first.Metadata["task_attempt"]))
	require.Equal(t, 2, intValue(second.Metadata["task_attempt"]))

	// 1) 缓存命名空间一致：重试不得换 key，也不得推进 epoch（历史未被改写）。
	key1, _ := first.Metadata["prompt_cache_key"].(string)
	key2, _ := second.Metadata["prompt_cache_key"].(string)
	require.NotEmpty(t, key1, "child 请求必须携带自己的 cache key")
	require.Equal(t, key1, key2, "重试必须复用同一次 attempt 的 prompt-cache key")
	require.Equal(t, intValue(first.Metadata["prompt_cache_epoch"]), intValue(second.Metadata["prompt_cache_epoch"]),
		"重试不得推进 prompt-cache epoch（append-only 重跑，无前缀改写）")

	// 2) 前缀逐项一致：provider 侧前缀缓存只能在 messages/tools 字节稳定的前提下命中。
	require.Equal(t, first.Messages, second.Messages, "重试的 messages 前缀必须与失败的那次逐项一致")
	require.Equal(t, first.Tools, second.Tools, "重试的 tools 前缀必须与失败的那次一致")

	// 3) provider/model 一致：前缀缓存按 provider 隔离。
	require.Equal(t, first.Model, second.Model, "重试必须保持同一模型")
	require.Equal(t, first.Provider, second.Provider, "重试必须保持同一 provider")

	// 4) attempt 2 的缓存命中可观测并计入任务回执（usage_cache_read_tokens）。
	events := rig.snapshot()
	var completed map[string]interface{}
	for _, event := range events {
		if event.eventType == "subagent.completed" {
			completed = event.payload
		}
	}
	require.NotNil(t, completed, "任务终态必须可见")
	require.Equal(t, true, completed["success"])
	require.Equal(t, 20, completed["usage_cache_read_tokens"],
		"attempt 2 命中的缓存读取必须计入任务回执（复用预期前缀的直接收益）")
}

// TestConcurrentChildrenKeepDistinctCacheKeys 锁定 §8.4：同一批次内并发运行的
// child 各自持有按 session 派生的 cache key——即使两个任务目标文字完全相同，
// 也不得共享 key（错误共享会在后续轮次把不同前缀关联到同一缓存命名空间）。
// key 以 child session（subagent_<task>_<uuid>）命名，与父会话 key 不重合。
func TestConcurrentChildrenKeepDistinctCacheKeys(t *testing.T) {
	rig := newAttemptEventRig(t)

	llmRuntime := llm.NewLLMRuntime(nil)
	llmRuntime.UpdateRetryConfig(0, llm.RetryTuning{}, nil)
	provider := &roleRoutingLLMProvider{
		SequenceLLMProvider: &SequenceLLMProvider{name: "test-provider"},
		childResponses: []*llm.LLMResponse{
			{Content: "Child one done.", Model: "test-model"},
			{Content: "Child two done.", Model: "test-model"},
		},
	}
	require.NoError(t, llmRuntime.RegisterProvider("test-provider", provider))
	// 子代理走 agent.llmRuntime（child_factory 取 parent.llmRuntime）；本例不经父
	// turn，故直接装配同一 runtime（等价于 NewReActLoop 的接线）。
	rig.agent.llmRuntime = llmRuntime

	// 直接走协调器（不经父 turn）：两个 read_only 任务目标文字完全相同——若 key
	// 按内容派生而非按 session 派生，这里就会出现共享。
	const parentSessionID = "parent-cache-scope"
	coordinator := rig.agent.GetSubagentBatchCoordinator()
	require.NotNil(t, coordinator)
	batch, err := coordinator.StartBackground(context.Background(), BatchStartOptions{
		ParentSessionID: parentSessionID,
		RootScopeID:     parentSessionID,
		ExecutionMode:   subagentbatch.ExecutionModeBackground,
	}, []SubagentTask{
		{ID: "c1", Goal: "Inspect the same thing", ReadOnly: true},
		{ID: "c2", Goal: "Inspect the same thing", ReadOnly: true},
	})
	require.NoError(t, err)
	require.NotNil(t, batch)
	rig.waitTerminal(t)

	requests := provider.childRequestSnapshot()
	require.Len(t, requests, 2, "两个任务各恰一次子请求：%d", len(requests))

	keys := make([]string, 0, len(requests))
	for _, req := range requests {
		key, _ := req.Metadata["prompt_cache_key"].(string)
		key = strings.TrimSpace(key)
		require.NotEmpty(t, key, "child 请求必须携带自己的 cache key")
		require.Equal(t, 0, intValue(req.Metadata["prompt_cache_epoch"]),
			"新 child 的首次运行不应带 epoch 后缀")
		keys = append(keys, key)
	}
	require.NotEqual(t, keys[0], keys[1], "并发 child 不得共享 cache key：%v", keys)

	// key 绑定各自的 child session（subagent_<task>_<uuid>）：排序后逐一对号。
	sort.Strings(keys)
	require.Contains(t, keys[0], "subagent_c1_", "key 必须按 child session 派生：%v", keys)
	require.Contains(t, keys[1], "subagent_c2_", "key 必须按 child session 派生：%v", keys)

	// 与父会话命名空间不重合（父子同批也不得错误复用父 key）。
	parentKey := promptCacheKeyForEpoch(parentSessionID, 0)
	for _, key := range keys {
		require.NotEqual(t, parentKey, key, "child 不得复用父会话 cache key")
	}
}

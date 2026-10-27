package commands

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// TestLocalSpawnContextReceiptForkAndFresh 锁定 §6.5/§6.4：direct spawn 的 receipt
// 与 batch 路径同字段名、同语义（requested/effective 分离、空快照降级 fresh、
// 父 epoch 派生 prompt-cache key）。
func TestLocalSpawnContextReceiptForkAndFresh(t *testing.T) {
	parent := runtimechat.NewSession("user-1")
	parent.SetContext(agent.PromptCacheEpochSessionContextKey, 3)
	messages := []types.Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a"},
	}

	forkReceipt := localSpawnContextReceipt(parent, "parent-1", localAgentForkAll, messages)
	require.Equal(t, "fork", forkReceipt["requested_context"])
	require.Equal(t, "fork", forkReceipt["effective_context"])
	require.Equal(t, true, forkReceipt["parent_history_injected"])
	require.Equal(t, 2, forkReceipt["fork_snapshot_messages"])
	require.Equal(t, agent.ForkSnapshotHash(messages), forkReceipt["fork_snapshot_hash"])
	require.Equal(t, "fork", forkReceipt["context_mode"])
	require.Equal(t, "parent-1#prompt-cache-epoch-3", forkReceipt["parent_prompt_cache_key"])

	freshReceipt := localSpawnContextReceipt(parent, "parent-1", localAgentForkNone, nil)
	require.Equal(t, "fresh", freshReceipt["requested_context"])
	require.Equal(t, "fresh", freshReceipt["effective_context"])
	require.Equal(t, false, freshReceipt["parent_history_injected"])
	require.Equal(t, 0, freshReceipt["fork_snapshot_messages"])
	_, hasHash := freshReceipt["fork_snapshot_hash"]
	require.False(t, hasHash, "fresh spawn must not fabricate a snapshot hash")
	require.Equal(t, "parent-1#prompt-cache-epoch-3", freshReceipt["parent_prompt_cache_key"])

	// 请求 fork 但父历史为空：requested 保留、effective 降级 fresh（与 batch 一致）。
	emptyFork := localSpawnContextReceipt(parent, "parent-1", localAgentForkAll, nil)
	require.Equal(t, "fork", emptyFork["requested_context"])
	require.Equal(t, "fresh", emptyFork["effective_context"])
	require.Equal(t, false, emptyFork["parent_history_injected"])

	// 父 epoch 缺失：退化为裸 session id（不猜测世代）。
	bareParent := runtimechat.NewSession("user-1")
	require.Equal(t, "parent-2", localSpawnContextReceipt(bareParent, "parent-2", localAgentForkNone, nil)["parent_prompt_cache_key"])

	// JSON 往返后的 float64/字符串 epoch 同样可解析。
	parentFloat := runtimechat.NewSession("user-1")
	parentFloat.SetContext(agent.PromptCacheEpochSessionContextKey, float64(5))
	require.Equal(t, "parent-3#prompt-cache-epoch-5", localSpawnContextReceipt(parentFloat, "parent-3", localAgentForkNone, nil)["parent_prompt_cache_key"])
}

// TestCopyLocalSpawnReceiptToPayload 锁定完成镜像转发：只转发子会话上下文里
// 实际存在的 receipt 键，未写入的键不臆造。
func TestCopyLocalSpawnReceiptToPayload(t *testing.T) {
	child := runtimechat.NewSession("user-1")
	child.SetContext("context_mode", "fork")
	child.SetContext("fork_snapshot_messages", 2)
	child.SetContext("fork_snapshot_hash", "abc123")
	payload := map[string]interface{}{"subagent_id": "child-1"}

	copyLocalSpawnReceiptToPayload(child, payload)

	require.Equal(t, "fork", payload["context_mode"])
	require.Equal(t, 2, payload["fork_snapshot_messages"])
	require.Equal(t, "abc123", payload["fork_snapshot_hash"])
	_, hasRequested := payload["requested_context"]
	require.False(t, hasRequested, "keys absent from the child context must not be fabricated")

	// nil 安全：空子会话/空载荷不得 panic。
	copyLocalSpawnReceiptToPayload(runtimechat.NewSession("user-1"), payload)
	copyLocalSpawnReceiptToPayload(nil, payload)
}

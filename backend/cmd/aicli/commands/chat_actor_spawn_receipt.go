package commands

// chat_actor_spawn_receipt.go — direct `subagent_spawn` 的上下文 receipt
//（§6.5/§6.4 统一）：把 batch 路径 `subagent.route.resolved` 上同名的 spawn 事实
// （requested/effective context、父历史注入事实、快照条数/指纹、父 prompt-cache
// key）落到子会话上下文，并在完成镜像载荷里转发，使 direct 与 batch 两条派发
// 路径在分析侧可用同一组字段对账。

import (
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agent"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// localSpawnReceiptKeys 是 receipt 的稳定字段名（与 batch 路径事件载荷同名）。
var localSpawnReceiptKeys = []string{
	"requested_context",
	"effective_context",
	"parent_history_injected",
	"fork_snapshot_messages",
	"fork_snapshot_hash",
	"context_mode",
	"parent_prompt_cache_key",
}

// localSpawnContextReceipt 计算 direct spawn 的 receipt：
//   - requested：forkMode 非 none → fork，否则 fresh；
//   - effective：仅当实际注入了非空快照才是 fork（fork 但父历史为空 → fresh，
//     与 batch 路径 BuildForkHistory 的"首轮 fork 等价 fresh"一致）；
//   - fork_snapshot_hash：快照非空时与 batch 路径同源指纹；
//   - parent_prompt_cache_key：父会话 spawn 时刻 epoch 派生（缺失/0 → 裸 id）。
func localSpawnContextReceipt(parent *runtimechat.Session, parentSessionID string, forkMode localAgentForkMode, forkMessages []types.Message) map[string]interface{} {
	requested := agent.SubagentContextModeFresh
	if forkMode != localAgentForkNone {
		requested = agent.SubagentContextModeFork
	}
	receipt := agent.SubagentContextReceipt(requested, len(forkMessages))
	receipt["context_mode"] = receipt["effective_context"]
	if len(forkMessages) > 0 {
		receipt["fork_snapshot_hash"] = agent.ForkSnapshotHash(forkMessages)
	}
	if key := agent.SubagentParentPromptCacheKey(parentSessionID, func() int { return localParentPromptCacheEpoch(parent) }); key != "" {
		receipt["parent_prompt_cache_key"] = key
	}
	return receipt
}

// localParentPromptCacheEpoch 读取父会话上下文中持久化的 prompt-cache 世代
// （与 agent.PromptCacheEpochSessionContextKey 同键）。JSON 往返可能把整数变成
// float64/字符串，统一归一化；缺失或不可解析返回 0（不猜测）。
func localParentPromptCacheEpoch(parent *runtimechat.Session) int {
	if parent == nil || parent.Metadata.Context == nil {
		return 0
	}
	return localSpawnEpochFromContext(parent.Metadata.Context[agent.PromptCacheEpochSessionContextKey])
}

func localSpawnEpochFromContext(raw interface{}) int {
	switch value := raw.(type) {
	case int:
		if value > 0 {
			return value
		}
	case int64:
		if value > 0 {
			return int(value)
		}
	case float64:
		if value > 0 {
			return int(value)
		}
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

// copyLocalSpawnReceiptToPayload 把 spawn 时写入子会话上下文的 receipt 字段
// 转发进完成镜像载荷；未写入的键不臆造（读取侧把"未携带"与"零值"分开）。
func copyLocalSpawnReceiptToPayload(child *runtimechat.Session, payload map[string]interface{}) {
	if child == nil || payload == nil || child.Metadata.Context == nil {
		return
	}
	for _, key := range localSpawnReceiptKeys {
		if value, ok := child.Metadata.Context[key]; ok && value != nil {
			payload[key] = value
		}
	}
}

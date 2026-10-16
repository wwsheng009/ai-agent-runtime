package agent

// fork_audit.go — fork 子级首请求的继承前缀审计（P2-1' G3 验收度量）。
//
// 背景：fork 委派的核心收益是父级已完成轮次的一次性快照可被复用为请求前缀
// （KV Cache 命中前提）。但快照注入后仍可能被上下文装配改写（预算裁剪、
// 上下文管理器的 stage 替换、preflight 压缩）——"注入过"不等于"最终请求里
// 还在"。本文件在 fork 子级的**首个组装请求**上做一次前缀保持性检查，并把
// 结果盖进请求 metadata：随 `llm.request.started` 载荷进入 cache analytics
// 的 cache_requests 记录（record_json，无需 schema 迁移），从而可以按会话
// 聚合出"fork 前缀保持率"与"fork 请求缓存读取命中"两项指标。
//
// 指标读法（cache_requests.fork_* 字段 + usage）：
//   - fork 前缀保持率 = fork_prefix_preserved=true 的记录数 / 全部非空
//     fork_prefix_preserved 的记录数；
//   - 提供方缓存命中证据 = 同批记录中 usage.cache_read_tokens > 0 的占比
//     （cache_hit_ratio 已有本地重算口径）。

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// ForkAuditSpec 启用一次 run 的 fork 前缀审计；Snapshot 是 fork 时冻结的
// 继承快照（与注入子级的 History 同源）。
type ForkAuditSpec struct {
	Snapshot []types.Message
}

// fork 审计的请求 metadata 键（随 llm.request.started 载荷流转）。
const (
	forkPrefixPreservedKey    = "fork_prefix_preserved"
	forkSnapshotMessagesKey   = "fork_snapshot_messages"
	forkSnapshotTokensKey     = "fork_snapshot_tokens"
	forkPrefixKeptMessagesKey = "fork_prefix_kept_messages"
)

// forkPrefixStats 是一次前缀保持性检查的结果。
type forkPrefixStats struct {
	// preserved 为 true 表示快照在最终装配消息中仍以连续整段存在。
	preserved bool
	// snapshotMessages / snapshotTokens 是快照自身的规模（审计对照基线）。
	snapshotMessages int
	snapshotTokens   int
	// keptMessages 是最终装配消息中仍然连续保持的快照前缀长度
	//（preserved=true 时等于 snapshotMessages；部分改写时给出缩水程度）。
	keptMessages int
}

// metadata 把审计结果摊成请求 metadata / 事件载荷键值。
func (s forkPrefixStats) metadata() map[string]interface{} {
	return map[string]interface{}{
		forkPrefixPreservedKey:    s.preserved,
		forkSnapshotMessagesKey:   s.snapshotMessages,
		forkSnapshotTokensKey:     s.snapshotTokens,
		forkPrefixKeptMessagesKey: s.keptMessages,
	}
}

// evaluateForkPrefixPreservation 在 managed（最终装配消息）中查找 snapshot
// 的连续整段：整段命中 → 完全保持；否则给出最长连续保持前缀长度。
// countTokens 为 nil 时回退保守估算（与 fork 快照上限同口径）。
func evaluateForkPrefixPreservation(snapshot, managed []types.Message, countTokens func([]types.Message) int) forkPrefixStats {
	stats := forkPrefixStats{snapshotMessages: len(snapshot)}
	if len(snapshot) == 0 {
		return stats
	}
	counter := countTokens
	if counter == nil {
		counter = forkSnapshotFallbackTokens
	}
	stats.snapshotTokens = counter(snapshot)
	if len(managed) == 0 {
		return stats
	}
	snapshotFingerprints := make([]string, len(snapshot))
	for i, message := range snapshot {
		snapshotFingerprints[i] = promptMessageFingerprint([]types.Message{message})
	}
	managedFingerprints := make([]string, len(managed))
	for i, message := range managed {
		managedFingerprints[i] = promptMessageFingerprint([]types.Message{message})
	}
	best := 0
	for start := 0; start < len(managedFingerprints); start++ {
		if managedFingerprints[start] != snapshotFingerprints[0] {
			continue
		}
		run := 0
		for run < len(snapshotFingerprints) &&
			start+run < len(managedFingerprints) &&
			managedFingerprints[start+run] == snapshotFingerprints[run] {
			run++
		}
		if run > best {
			best = run
		}
		if best == len(snapshotFingerprints) {
			break
		}
	}
	stats.keptMessages = best
	stats.preserved = best == len(snapshot)
	return stats
}

// forkAuditSpecForTask 仅在 fork 子级的**首次尝试**上启用前缀审计：重试会
// 重新注入同一快照，重复审计没有增量信息（且会稀释按请求聚合的分母）。
// 非 fork 任务或快照为空时返回 nil（不产出任何 metadata）。
func forkAuditSpecForTask(task SubagentTask, forkHistory []types.Message, attempt int) *ForkAuditSpec {
	if strings.TrimSpace(task.Context) != SubagentContextFork || attempt != 1 || len(forkHistory) == 0 {
		return nil
	}
	return &ForkAuditSpec{Snapshot: forkHistory}
}

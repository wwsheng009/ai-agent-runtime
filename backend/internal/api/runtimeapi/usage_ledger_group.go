package runtimeapi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// usageLedgerGroupByProfile 是 GetUsageLedger 支持的分组维度（FR-13：
// 按 profile 对比 token 消耗）。当前只有 profile 一个维度，保持显式白名单，
// 非法值一律 400，不做静默降级。
const usageLedgerGroupByProfile = "profile"

// usageLedgerProfileGroup 是按 profile 聚合的一组用量。
//
// 除 token 三件套外，还汇总知识层探索归因的 9 个指标（Phase 0 交付 2 / `06`
// §5.2："在 usageanalytics 暴露"）。聚合落点在 ledger 的读取路径（本文件），
// 因为 `internal/usageanalytics` 不读 `usageledger`；口径与验收一致：
// 9 个字段全部是**可加计数/token**，按组求和即可复算 "每个任务平均多少 token
// 花在探索 / 重复读取"。
//
// 9 个新字段一律 `omitempty`：`mode=off`（默认）下所有归因字段为 0，序列化
// 结果与新增前逐字节一致——保持硬不变量。
type usageLedgerProfileGroup struct {
	Profile      string `json:"profile"`
	Requests     int    `json:"requests"`
	Failures     int    `json:"failures"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`

	// 探索归因 9 指标（ADR-0003 §4.1 的 ledger 列）。零值省略。
	ExplorationTokens             int `json:"exploration_tokens,omitempty"`
	ReuseTokens                   int `json:"reuse_tokens,omitempty"`
	IndexLookupCount              int `json:"index_lookup_count,omitempty"`
	IndexHit                      int `json:"index_hit,omitempty"`
	FallbackCount                 int `json:"fallback_count,omitempty"`
	UnsafeReuseCount              int `json:"unsafe_reuse_count,omitempty"`
	ToolCallsPerTask              int `json:"tool_calls_per_task,omitempty"`
	RepeatedReadCount             int `json:"repeated_read_count,omitempty"`
	KnowledgeVersionMismatchCount int `json:"knowledge_version_mismatch_count,omitempty"`
}

// aggregateUsageLedgerByProfile 按记录元数据里的 profile 键聚合（FR-13）。
//
// 口径：
//   - 缺失 profile 键（历史行 / 未接线入口）与显式空值统一归入 profile=""
//     组（"未归属"）：分组请求数总和恒等于输入非 nil 记录数，便于用空组
//     观察未接线路径，不做二次猜测（与写时"不猜"纪律一致）。
//   - nil 记录跳过（防御 store 层异常数据）。
//   - 排序：total_tokens 降序 → profile 升序，保证同一批数据输出确定。
func aggregateUsageLedgerByProfile(records []*entity.TokenUsageHistory) []usageLedgerProfileGroup {
	groups := make([]usageLedgerProfileGroup, 0, 4)
	if len(records) == 0 {
		return groups
	}
	positions := make(map[string]int, 4)
	for _, record := range records {
		if record == nil {
			continue
		}
		profile := ""
		if record.Metadata != nil {
			if raw, ok := record.Metadata["profile"]; ok && raw != nil {
				profile = strings.TrimSpace(fmt.Sprint(raw))
			}
		}
		position, ok := positions[profile]
		if !ok {
			position = len(groups)
			positions[profile] = position
			groups = append(groups, usageLedgerProfileGroup{Profile: profile})
		}
		group := &groups[position]
		group.Requests++
		if !record.Success {
			group.Failures++
		}
		group.InputTokens += record.InputTokens
		group.OutputTokens += record.OutputTokens
		group.TotalTokens += record.TotalTokens
		// 探索归因 9 指标：与 token 同口径按组求和（可加字段，无需去重）。
		group.ExplorationTokens += record.ExplorationTokens
		group.ReuseTokens += record.ReuseTokens
		group.IndexLookupCount += record.IndexLookupCount
		group.IndexHit += record.IndexHit
		group.FallbackCount += record.FallbackCount
		group.UnsafeReuseCount += record.UnsafeReuseCount
		group.ToolCallsPerTask += record.ToolCallsPerTask
		group.RepeatedReadCount += record.RepeatedReadCount
		group.KnowledgeVersionMismatchCount += record.KnowledgeVersionMismatchCount
	}
	sort.SliceStable(groups, func(i, j int) bool {
		if groups[i].TotalTokens != groups[j].TotalTokens {
			return groups[i].TotalTokens > groups[j].TotalTokens
		}
		return groups[i].Profile < groups[j].Profile
	})
	return groups
}

// ledgerProfileName 返回 profile 在 usage ledger（FR-13）中的聚合身份：
// 声明名（Resolved.ProfileName）优先，为空时回退解析出的引用（Reference）。
// 两者皆空返回 ""——调用方据此不写 profile 键（不猜）。
func ledgerProfileName(state *profileRuntimeState) string {
	if state == nil {
		return ""
	}
	if state.Resolved != nil {
		if name := strings.TrimSpace(state.Resolved.ProfileName); name != "" {
			return name
		}
	}
	return strings.TrimSpace(state.Reference)
}

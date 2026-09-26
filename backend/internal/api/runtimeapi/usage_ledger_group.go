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
type usageLedgerProfileGroup struct {
	Profile      string `json:"profile"`
	Requests     int    `json:"requests"`
	Failures     int    `json:"failures"`
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
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

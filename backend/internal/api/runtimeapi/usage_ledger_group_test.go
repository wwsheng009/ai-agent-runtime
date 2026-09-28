package runtimeapi

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// FR-13：聚合面（纯函数）与 UsageScope.Profile 的序列化契约。

func ledgerGroupRecord(profile interface{}, success bool, input, output, total int) *entity.TokenUsageHistory {
	metadata := entity.JSONMap{}
	if profile != nil {
		metadata["profile"] = profile
	}
	return &entity.TokenUsageHistory{
		Success:      success,
		InputTokens:  input,
		OutputTokens: output,
		TotalTokens:  total,
		Metadata:     metadata,
	}
}

func TestAggregateUsageLedgerByProfile(t *testing.T) {
	groups := aggregateUsageLedgerByProfile([]*entity.TokenUsageHistory{
		ledgerGroupRecord("coding", true, 100, 20, 120),
		ledgerGroupRecord("coding", false, 50, 10, 60),
		ledgerGroupRecord("review", true, 10, 2, 12),
		ledgerGroupRecord("", true, 5, 1, 6),
		ledgerGroupRecord(nil, true, 7, 3, 10),
		nil,
	})

	require.Len(t, groups, 3)

	// coding：两请求，一失败，token 求和。
	require.Equal(t, "coding", groups[0].Profile)
	require.Equal(t, 2, groups[0].Requests)
	require.Equal(t, 1, groups[0].Failures)
	require.Equal(t, 150, groups[0].InputTokens)
	require.Equal(t, 30, groups[0].OutputTokens)
	require.Equal(t, 180, groups[0].TotalTokens)

	// 未归属组：显式空值与缺失键合并（total 16 > review 12，按 total 降序）。
	require.Equal(t, "", groups[1].Profile)
	require.Equal(t, 2, groups[1].Requests)
	require.Equal(t, 0, groups[1].Failures)
	require.Equal(t, 16, groups[1].TotalTokens)

	require.Equal(t, "review", groups[2].Profile)
	require.Equal(t, 12, groups[2].TotalTokens)

	// 分组守恒：请求数总和 == 输入非 nil 记录数（6 条含 1 条 nil）。
	requests := 0
	for _, group := range groups {
		requests += group.Requests
	}
	require.Equal(t, 5, requests)
}

func TestAggregateUsageLedgerByProfileEmptyInput(t *testing.T) {
	require.Empty(t, aggregateUsageLedgerByProfile(nil))
	require.Empty(t, aggregateUsageLedgerByProfile([]*entity.TokenUsageHistory{nil}))
}

// 反证：把 profile 写入路径禁用（记录不带 profile 键）时，全部记录落入
// "未归属"组——聚合结果不会凭空出现 profile 名。
func TestAggregateUsageLedgerByProfileWithoutRecordedProfile(t *testing.T) {
	groups := aggregateUsageLedgerByProfile([]*entity.TokenUsageHistory{
		ledgerGroupRecord(nil, true, 10, 1, 11),
		ledgerGroupRecord(nil, true, 20, 2, 22),
	})
	require.Len(t, groups, 1)
	require.Equal(t, "", groups[0].Profile)
	require.Equal(t, 2, groups[0].Requests)
	require.Equal(t, 33, groups[0].TotalTokens)
}

// 知识层探索归因 9 指标（Phase 0 交付 2 / `06` §5.2）：按 profile 组求和。
func TestAggregateUsageLedgerByProfileSumsExplorationAttribution(t *testing.T) {
	first := ledgerGroupRecord("coding", true, 100, 20, 120)
	first.ExplorationTokens = 40
	first.ReuseTokens = 8
	first.IndexLookupCount = 3
	first.IndexHit = 2
	first.FallbackCount = 1
	first.ToolCallsPerTask = 7
	first.RepeatedReadCount = 4
	// UnsafeReuseCount / KnowledgeVersionMismatchCount 是硬门槛（=0），
	// 第一行保持 0，第二行给非零以验证求和确实发生。

	second := ledgerGroupRecord("coding", true, 50, 10, 60)
	second.ExplorationTokens = 2
	second.UnsafeReuseCount = 0
	second.KnowledgeVersionMismatchCount = 0
	second.ToolCallsPerTask = 5

	groups := aggregateUsageLedgerByProfile([]*entity.TokenUsageHistory{first, second})

	require.Len(t, groups, 1)
	group := groups[0]
	require.Equal(t, "coding", group.Profile)
	require.Equal(t, 42, group.ExplorationTokens)
	require.Equal(t, 8, group.ReuseTokens)
	require.Equal(t, 3, group.IndexLookupCount)
	require.Equal(t, 2, group.IndexHit)
	require.Equal(t, 1, group.FallbackCount)
	require.Equal(t, 0, group.UnsafeReuseCount)
	require.Equal(t, 12, group.ToolCallsPerTask)
	require.Equal(t, 4, group.RepeatedReadCount)
	require.Equal(t, 0, group.KnowledgeVersionMismatchCount)
}

// mode=off（默认）下 9 个归因字段全为 0，聚合行不得多出任何键——
// 保证旧响应逐字节不变（硬不变量）。
func TestUsageLedgerProfileGroupOmitsZeroAttribution(t *testing.T) {
	raw, err := json.Marshal(usageLedgerProfileGroup{
		Profile:      "coding",
		Requests:     1,
		InputTokens:  10,
		OutputTokens: 2,
		TotalTokens:  12,
	})
	require.NoError(t, err)
	for _, key := range []string{
		"exploration_tokens", "reuse_tokens", "index_lookup_count", "index_hit",
		"fallback_count", "unsafe_reuse_count", "tool_calls_per_task",
		"repeated_read_count", "knowledge_version_mismatch_count",
	} {
		require.NotContains(t, string(raw), key, "零值归因字段不得出现在序列化结果中")
	}
}

func TestUsageScopeJSONOmitsEmptyProfile(t *testing.T) {
	raw, err := json.Marshal(UsageScope{
		TenantID:  "tenant-a",
		ProjectID: "project-a",
		UserID:    "user-a",
		ScopeKey:  "tenant-a|project-a|user-a",
	})
	require.NoError(t, err)
	require.NotContains(t, string(raw), "profile", "空 Profile 不得进入序列化结果（旧响应逐字节不变）")

	rawWithProfile, err := json.Marshal(UsageScope{ScopeKey: "k", Profile: "coding"})
	require.NoError(t, err)
	require.Contains(t, string(rawWithProfile), `"profile":"coding"`)
}

func TestLedgerProfileName(t *testing.T) {
	require.Equal(t, "", ledgerProfileName(nil))
	require.Equal(t, "fallback-ref", ledgerProfileName(&profileRuntimeState{Reference: " fallback-ref "}))
	require.Equal(t, "declared", ledgerProfileName(&profileRuntimeState{
		Reference: "ref-path",
		Resolved:  &profilesys.ResolvedAgent{ProfileName: "declared"},
	}))
	require.Equal(t, "ref-path", ledgerProfileName(&profileRuntimeState{
		Reference: "ref-path",
		Resolved:  &profilesys.ResolvedAgent{},
	}))
	require.Equal(t, "", ledgerProfileName(&profileRuntimeState{}))
}

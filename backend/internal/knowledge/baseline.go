package knowledge

import (
	"sort"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// BaselineReport 是 Phase 0 基线报告（04 §7.1 / §7.2 / §7.7）的复算结果。
//
// 它只依赖 `usageledger` 记录：同一份数据任意次数复算必须得到完全相同的结果
// （Phase 0 验收门槛：“数字可由 ledger 复算”）。因此这里不做采样、不做时间
// 窗口裁剪、不做并发聚合——输入相同，输出必然相同。
type BaselineReport struct {
	// Tasks 是参与统计的 ledger 记录数（一条记录 = 一次请求/任务）。
	Tasks int
	// TotalTokens 是 token 总量（分母）。
	TotalTokens int
	// ExplorationTokens / ReuseTokens 是探索与复用归因。
	ExplorationTokens int
	ReuseTokens       int
	// ToolCalls 是工具调用总数；ToolCallsPerTask 是均值（护栏指标）。
	ToolCalls         int
	ToolCallsPerTask  float64
	RepeatedReads     int
	IndexLookups      int
	IndexHits         int
	Fallbacks         int
	UnsafeReuse       int
	VersionMismatches int
	FailedTasks       int
	SuccessfulTasks   int
}

// Summarize 从 ledger 记录复算基线报告。
//
// nil 记录被跳过（GetSince 理论上不返回 nil，但复算不应因脏数据崩掉）；
// 度量字段为 0 的记录仍然计入 Tasks——它们是 mode=off 的真实样本。
func Summarize(records []*entity.TokenUsageHistory) BaselineReport {
	report := BaselineReport{}
	for _, record := range records {
		if record == nil {
			continue
		}
		report.Tasks++
		report.TotalTokens += record.TotalTokens
		report.ExplorationTokens += record.ExplorationTokens
		report.ReuseTokens += record.ReuseTokens
		report.ToolCalls += record.ToolCallsPerTask
		report.RepeatedReads += record.RepeatedReadCount
		report.IndexLookups += record.IndexLookupCount
		report.IndexHits += record.IndexHit
		report.Fallbacks += record.FallbackCount
		report.UnsafeReuse += record.UnsafeReuseCount
		report.VersionMismatches += record.KnowledgeVersionMismatchCount
		if record.Success {
			report.SuccessfulTasks++
		} else {
			report.FailedTasks++
		}
	}
	if report.Tasks > 0 {
		report.ToolCallsPerTask = float64(report.ToolCalls) / float64(report.Tasks)
	}
	return report
}

// ExplorationTokenShare 返回探索 token 占比（04 §7.2，基线要回答的第一个问题）。
// 分母为 0 时返回 0，避免把“没有样本”报成“占比 0%”。
func (r BaselineReport) ExplorationTokenShare() float64 {
	if r.TotalTokens <= 0 {
		return 0
	}
	return float64(r.ExplorationTokens) / float64(r.TotalTokens)
}

// ReuseTokenShare 返回复用 token 占比。
func (r BaselineReport) ReuseTokenShare() float64 {
	if r.TotalTokens <= 0 {
		return 0
	}
	return float64(r.ReuseTokens) / float64(r.TotalTokens)
}

// RepeatedReadPerTask 返回每任务重复读取次数（04 §7.2）。
func (r BaselineReport) RepeatedReadPerTask() float64 {
	if r.Tasks <= 0 {
		return 0
	}
	return float64(r.RepeatedReads) / float64(r.Tasks)
}

// IndexHitRate 返回索引命中率（分母为 0 时返回 0）。
func (r BaselineReport) IndexHitRate() float64 {
	if r.IndexLookups <= 0 {
		return 0
	}
	return float64(r.IndexHits) / float64(r.IndexLookups)
}

// FallbackRate 返回 fallback 率（分母为 0 时返回 0）。
func (r BaselineReport) FallbackRate() float64 {
	if r.IndexLookups <= 0 {
		return 0
	}
	return float64(r.Fallbacks) / float64(r.IndexLookups)
}

// SafetyViolations 返回被违反的硬门槛（04 §7.3），空表示通过。
func (r BaselineReport) SafetyViolations() []string {
	return Counters{
		UnsafeReuseCount:              r.UnsafeReuse,
		KnowledgeVersionMismatchCount: r.VersionMismatches,
	}.SafetyViolations()
}

// Percentiles 返回给定样本的 p50 / p95（最近秩法，Nearest-Rank）。
//
// 用于基线报告的延迟指标（04 §7.1：p95 延迟）。样本为原始毫秒值；空样本返回
// (0, 0)。最近秩法不插值，保证同一份样本复算结果完全一致。
func Percentiles(samples []float64) (p50 float64, p95 float64) {
	if len(samples) == 0 {
		return 0, 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)

	return sorted[nearestRankIndex(len(sorted), 0.50)], sorted[nearestRankIndex(len(sorted), 0.95)]
}

// nearestRankIndex 返回最近秩法（ceil(p*n)-1）的下标，越界时收敛到两端。
func nearestRankIndex(n int, percentile float64) int {
	if n <= 0 {
		return 0
	}
	index := int(float64(n)*percentile+0.999999999) - 1
	if index < 0 {
		return 0
	}
	if index >= n {
		return n - 1
	}
	return index
}

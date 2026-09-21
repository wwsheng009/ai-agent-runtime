package knowledge

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

func baselineRecords() []*entity.TokenUsageHistory {
	return []*entity.TokenUsageHistory{
		{
			RequestID:         "task-1",
			TotalTokens:       100,
			ExplorationTokens: 30,
			ReuseTokens:       10,
			IndexLookupCount:  4,
			IndexHit:          3,
			FallbackCount:     1,
			ToolCallsPerTask:  6,
			RepeatedReadCount: 2,
			Success:           true,
		},
		{
			RequestID:         "task-2",
			TotalTokens:       300,
			ExplorationTokens: 90,
			ReuseTokens:       30,
			IndexLookupCount:  6,
			IndexHit:          4,
			FallbackCount:     2,
			ToolCallsPerTask:  9,
			RepeatedReadCount: 4,
			Success:           false,
		},
	}
}

// Phase 0 验收门槛：同一份数据两次复算必须完全一致。
func TestSummarizeIsDeterministic(t *testing.T) {
	records := baselineRecords()

	first := Summarize(records)
	second := Summarize(records)

	require.Equal(t, first, second)
	require.Equal(t, 2, first.Tasks)
	require.Equal(t, 400, first.TotalTokens)
	require.Equal(t, 120, first.ExplorationTokens)
	require.Equal(t, 40, first.ReuseTokens)
	require.Equal(t, 10, first.IndexLookups)
	require.Equal(t, 7, first.IndexHits)
	require.Equal(t, 3, first.Fallbacks)
	require.Equal(t, 15, first.ToolCalls)
	require.Equal(t, 6, first.RepeatedReads)
	require.Equal(t, 1, first.SuccessfulTasks)
	require.Equal(t, 1, first.FailedTasks)
	require.InDelta(t, 7.5, first.ToolCallsPerTask, 1e-9)
}

// 派生比率必须能直接回答“每个任务平均多少 token 花在探索 / 重复读取”。
func TestSummarizeDerivedRates(t *testing.T) {
	report := Summarize(baselineRecords())

	require.InDelta(t, 0.3, report.ExplorationTokenShare(), 1e-9)
	require.InDelta(t, 0.1, report.ReuseTokenShare(), 1e-9)
	require.InDelta(t, 3.0, report.RepeatedReadPerTask(), 1e-9)
	require.InDelta(t, 0.7, report.IndexHitRate(), 1e-9)
	require.InDelta(t, 0.3, report.FallbackRate(), 1e-9)
	require.Empty(t, report.SafetyViolations())
}

// 空样本与全零样本不得产生 NaN：mode=off 的库会命中这条路径。
func TestSummarizeEmptyAndZeroSamples(t *testing.T) {
	empty := Summarize(nil)
	require.Equal(t, 0, empty.Tasks)
	require.Zero(t, empty.ExplorationTokenShare())
	require.Zero(t, empty.RepeatedReadPerTask())
	require.Zero(t, empty.IndexHitRate())
	require.Zero(t, empty.FallbackRate())

	zero := Summarize([]*entity.TokenUsageHistory{nil, {RequestID: "legacy", TotalTokens: 20}})
	require.Equal(t, 1, zero.Tasks, "nil 记录必须被跳过")
	require.Zero(t, zero.ExplorationTokens)
	require.Zero(t, zero.ExplorationTokenShare())
}

// 硬门槛违反必须能被报告层点名（04 §7.3）。
func TestSummarizeSafetyViolations(t *testing.T) {
	report := Summarize([]*entity.TokenUsageHistory{
		{RequestID: "unsafe", UnsafeReuseCount: 1, KnowledgeVersionMismatchCount: 2},
	})

	require.Len(t, report.SafetyViolations(), 2)
}

// p95 用最近秩法：样本固定，结果必须可复算且不插值。
func TestPercentilesNearestRank(t *testing.T) {
	p50, p95 := Percentiles(nil)
	require.Zero(t, p50)
	require.Zero(t, p95)

	samples := []float64{40, 10, 30, 20}
	p50, p95 = Percentiles(samples)
	require.Equal(t, 20.0, p50)
	require.Equal(t, 40.0, p95)

	// 输入顺序不影响结果（内部排序），且不得改动调用方切片。
	shuffled := []float64{30, 40, 10, 20}
	p50Again, p95Again := Percentiles(shuffled)
	require.Equal(t, p50, p50Again)
	require.Equal(t, p95, p95Again)
	require.Equal(t, []float64{30, 40, 10, 20}, shuffled)

	// 单样本时 p50 = p95 = 该样本。
	p50, p95 = Percentiles([]float64{7})
	require.Equal(t, 7.0, p50)
	require.Equal(t, 7.0, p95)
}

// 端到端形状：记录 → 计数 → ledger 字段 → 复算，四步一致。
func TestBaselineRoundTripThroughLedgerFields(t *testing.T) {
	recorder := NewRecorder()
	recorder.Add(Counters{
		ExplorationTokens: 30,
		ReuseTokens:       10,
		IndexLookupCount:  4,
		IndexHit:          3,
		FallbackCount:     1,
		ToolCallsPerTask:  6,
		RepeatedReadCount: 2,
	})

	record := &entity.TokenUsageHistory{TotalTokens: 100, Success: true, CreatedAt: entity.Time(time.Now().UTC())}
	recorder.ApplyTo(record)

	report := Summarize([]*entity.TokenUsageHistory{record})
	require.InDelta(t, 0.3, report.ExplorationTokenShare(), 1e-9)
	require.InDelta(t, 2.0, report.RepeatedReadPerTask(), 1e-9)
	require.InDelta(t, 0.75, report.IndexHitRate(), 1e-9)
}

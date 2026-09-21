package knowledge

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// Phase 0 交付 2：计数器必须能完整映射到 ledger 的 9 个字段。
func TestCountersApplyWritesAllLedgerFields(t *testing.T) {
	counters := Counters{
		ExplorationTokens:             30,
		ReuseTokens:                   12,
		IndexLookupCount:              7,
		IndexHit:                      5,
		FallbackCount:                 2,
		UnsafeReuseCount:              1,
		ToolCallsPerTask:              9,
		RepeatedReadCount:             3,
		KnowledgeVersionMismatchCount: 1,
	}

	record := &entity.TokenUsageHistory{}
	counters.Apply(record)

	require.Equal(t, 30, record.ExplorationTokens)
	require.Equal(t, 12, record.ReuseTokens)
	require.Equal(t, 7, record.IndexLookupCount)
	require.Equal(t, 5, record.IndexHit)
	require.Equal(t, 2, record.FallbackCount)
	require.Equal(t, 1, record.UnsafeReuseCount)
	require.Equal(t, 9, record.ToolCallsPerTask)
	require.Equal(t, 3, record.RepeatedReadCount)
	require.Equal(t, 1, record.KnowledgeVersionMismatchCount)
}

// 零值计数器不得改变 ledger 记录：mode=off 时落库结果与改动前一致。
func TestCountersZeroValueLeavesRecordUntouched(t *testing.T) {
	require.True(t, Counters{}.IsZero())

	record := &entity.TokenUsageHistory{InputTokens: 10, TotalTokens: 10}
	Counters{}.Apply(record)

	require.Equal(t, entity.TokenUsageHistory{InputTokens: 10, TotalTokens: 10}, *record)
}

// 比率在分母为 0 时返回 0，而不是 NaN（基线报告要能直接复算）。
func TestCountersRates(t *testing.T) {
	require.Zero(t, Counters{}.IndexHitRate())
	require.Zero(t, Counters{}.FallbackRate())

	counters := Counters{IndexLookupCount: 10, IndexHit: 7, FallbackCount: 3}
	require.InDelta(t, 0.7, counters.IndexHitRate(), 1e-9)
	require.InDelta(t, 0.3, counters.FallbackRate(), 1e-9)
}

// 04 §7.3 的两个硬门槛：非零即违反，且必须点名。
func TestCountersSafetyViolations(t *testing.T) {
	require.Empty(t, Counters{IndexLookupCount: 5, IndexHit: 5}.SafetyViolations())

	require.Len(t, Counters{UnsafeReuseCount: 1}.SafetyViolations(), 1)
	require.Len(t, Counters{KnowledgeVersionMismatchCount: 2}.SafetyViolations(), 1)
	require.Len(t, Counters{UnsafeReuseCount: 1, KnowledgeVersionMismatchCount: 2}.SafetyViolations(), 2)
}

func TestRecorderAccumulatesAndResets(t *testing.T) {
	recorder := NewRecorder()

	recorder.RecordIndexLookup(true)
	recorder.RecordIndexLookup(false)
	recorder.RecordFallback()
	recorder.RecordRepeatedRead()
	recorder.Add(Counters{ExplorationTokens: 5, ReuseTokens: 3, ToolCallsPerTask: 4})

	snapshot := recorder.Snapshot()
	require.Equal(t, 2, snapshot.IndexLookupCount)
	require.Equal(t, 1, snapshot.IndexHit)
	require.Equal(t, 1, snapshot.FallbackCount)
	require.Equal(t, 1, snapshot.RepeatedReadCount)
	require.Equal(t, 5, snapshot.ExplorationTokens)
	require.Equal(t, 3, snapshot.ReuseTokens)
	require.Equal(t, 4, snapshot.ToolCallsPerTask)

	record := &entity.TokenUsageHistory{}
	recorder.ApplyTo(record)
	require.Equal(t, 2, record.IndexLookupCount)
	require.Equal(t, 5, record.ExplorationTokens)
	require.Equal(t, snapshot, recorder.Snapshot(), "ApplyTo 不得重置计数")

	recorder.Reset()
	require.True(t, recorder.Snapshot().IsZero())
}

// 并发累加不得丢计数（-race 下同时验证数据竞争）。
func TestRecorderConcurrentAdds(t *testing.T) {
	recorder := NewRecorder()

	const goroutines = 8
	const perGoroutine = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perGoroutine; j++ {
				recorder.RecordIndexLookup(true)
			}
		}()
	}
	wg.Wait()

	snapshot := recorder.Snapshot()
	require.Equal(t, goroutines*perGoroutine, snapshot.IndexLookupCount)
	require.Equal(t, goroutines*perGoroutine, snapshot.IndexHit)
}

// nil 接收者必须安全：知识层未启用时调用方无需判空。
func TestRecorderNilSafety(t *testing.T) {
	var recorder *Recorder

	require.NotPanics(t, func() {
		recorder.Add(Counters{IndexHit: 1})
		recorder.RecordIndexLookup(true)
		recorder.RecordFallback()
		recorder.RecordRepeatedRead()
		recorder.RecordUnsafeReuse()
		recorder.RecordVersionMismatch()
		recorder.ApplyTo(&entity.TokenUsageHistory{})
		recorder.Reset()
	})
	require.True(t, recorder.Snapshot().IsZero())
}

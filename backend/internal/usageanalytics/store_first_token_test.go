package usageanalytics

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// firstTokenPayload 构造带首字时间的成功终态载荷（耗时由事件时间差计算，
// 见 collector.onRequestFinished：duration = 终态时刻 - 起始时刻）。
func firstTokenPayload(firstTokenMS int) map[string]interface{} {
	payload := usagePayload(100, 10, 110, 0)
	payload["first_token_ms"] = firstTokenMS
	return payload
}

// TestFirstTokenMsIngestedAndAggregated 锁定首字时间全链路：终态事件 → 请求明细列
// → 会话预聚合列（总量 + 样本数）→ 列表/汇总/详情读路径。
// 口径：平均首字时间 = Σ首字时间 / Σ样本数，0 样本 → 0（表示"未采集"）。
func TestFirstTokenMsIngestedAndAggregated(t *testing.T) {
	now := time.Date(2026, 9, 17, 16, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()
	require.True(t, store.StatsReady())

	// turn-1：两个成功请求（首字 200/400ms，耗时 900/1100ms）；turn-2：一个（600ms/700ms）。
	publishRequestStarted(bus, "sess-ft", "req-ft1", "trace-ft1", "turn-1", 1, now.Add(-900*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-ft", "req-ft1", firstTokenPayload(200))
	publishRequestStarted(bus, "sess-ft", "req-ft2", "trace-ft1", "turn-1", 2, now.Add(-1100*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-ft", "req-ft2", firstTokenPayload(400))
	publishRequestStarted(bus, "sess-ft", "req-ft3", "trace-ft2", "turn-2", 1, now.Add(-700*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-ft", "req-ft3", firstTokenPayload(600))

	// 1) 明细列：逐请求落 duration_ms / first_token_ms。
	row := queryRow(t, store, `SELECT duration_ms, first_token_ms FROM usage_requests WHERE llm_request_id='req-ft2'`)
	require.Equal(t, []interface{}{int64(1100), int64(400)}, row)

	// 2) 预聚合列：总量与样本数（耗时口径与首字口径并列维护）。
	stats := readStoredStats(t, store, "sess-ft")
	require.Equal(t, int64(2700), stats.totalDuration)
	require.Equal(t, int64(3), stats.durationSamples)
	require.Equal(t, int64(1200), stats.totalFirstToken)
	require.Equal(t, int64(3), stats.firstTokenSamples)

	// 3) 列表路径：会话行给出均值与样本数。
	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, int64(400), list.Sessions[0].AverageFirstTokenMs)
	require.Equal(t, 3, list.Sessions[0].FirstTokenSamples)
	require.Equal(t, int64(900), list.Sessions[0].AverageResponseTimeMs)
	require.Equal(t, int64(400), list.Totals.AverageFirstTokenMs)
	require.Equal(t, 3, list.Totals.FirstTokenSamples)

	// 4) 汇总路径：全局合计与分组桶按样本加权。
	summary, err := store.Summarize(Query{GroupBy: "provider"})
	require.NoError(t, err)
	require.Equal(t, int64(400), summary.Totals.AverageFirstTokenMs)
	require.Equal(t, 3, summary.Totals.FirstTokenSamples)
	require.Len(t, summary.Groups, 1)
	require.Equal(t, int64(400), summary.Groups[0].AverageFirstTokenMs)
	require.Equal(t, 3, summary.Groups[0].FirstTokenSamples)

	// 5) 详情路径：步骤明细逐请求回放，turn 级给出均值。
	detail, err := store.SessionUsage("sess-ft")
	require.NoError(t, err)
	require.Len(t, detail.Steps, 3)
	// step 号在 turn 内计数（两个 turn 都有 step=1），按 trace + step 建键。
	byStep := map[[2]string]int64{}
	for _, step := range detail.Steps {
		byStep[[2]string{step.TraceID, strconv.Itoa(step.Step)}] = step.FirstTokenMs
	}
	require.Equal(t, int64(200), byStep[[2]string{"trace-ft1", "1"}])
	require.Equal(t, int64(400), byStep[[2]string{"trace-ft1", "2"}])
	require.Equal(t, int64(600), byStep[[2]string{"trace-ft2", "1"}])
	require.Len(t, detail.Turns, 2)
	require.Equal(t, int64(300), detail.Turns[0].FirstTokenMs) // (200+400)/2
	require.Equal(t, 2, detail.Turns[0].FirstTokenSamples)
	require.Equal(t, int64(600), detail.Turns[1].FirstTokenMs)
	require.Equal(t, 1, detail.Turns[1].FirstTokenSamples)
}

// TestFirstTokenMsNotCollectedStaysZero 锁定"0 = 未采集"：非流式/历史请求没有首字
// 时间时，预聚合样本数保持 0，均值不得被 0 摊平。
func TestFirstTokenMsNotCollectedStaysZero(t *testing.T) {
	now := time.Date(2026, 9, 17, 17, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	// req-a 有首字观测，req-b 无（历史/非流式）。
	publishRequestStarted(bus, "sess-mix", "req-a", "trace-1", "turn-1", 1, now.Add(-500*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-mix", "req-a", firstTokenPayload(250))
	publishRequestStarted(bus, "sess-mix", "req-b", "trace-1", "turn-1", 2, now.Add(-300*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-mix", "req-b", usagePayload(10, 1, 11, 0))

	stats := readStoredStats(t, store, "sess-mix")
	require.Equal(t, int64(250), stats.totalFirstToken)
	require.Equal(t, int64(1), stats.firstTokenSamples, "无观测请求不得进入样本数")

	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, int64(250), list.Sessions[0].AverageFirstTokenMs, "均值只按有观测的样本计算")
	require.Equal(t, 1, list.Sessions[0].FirstTokenSamples)
}

// TestFirstTokenMsSurvivesFailedTerminal 锁定终态覆盖规则：失败终态载荷没有首字
// 时间（0）时，明细列与会话计数都必须保留已记录的首字观测。
func TestFirstTokenMsSurvivesFailedTerminal(t *testing.T) {
	now := time.Date(2026, 9, 17, 18, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	publishRequestStarted(bus, "sess-retry", "req-r", "trace-1", "turn-1", 1, now.Add(-800*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-retry", "req-r", firstTokenPayload(320))
	// 同一请求随后收到失败终态（无 first_token_ms 字段）。
	publishRequestFinished(bus, "sess-retry", "req-r", map[string]interface{}{"success": false, "error_code": "upstream"})

	row := queryRow(t, store, `SELECT first_token_ms FROM usage_requests WHERE llm_request_id='req-r'`)
	require.Equal(t, []interface{}{int64(320)}, row)

	stats := readStoredStats(t, store, "sess-retry")
	require.Equal(t, int64(320), stats.totalFirstToken, "明细列保留的观测不得从聚合里减掉")
	require.Equal(t, int64(1), stats.firstTokenSamples)

	list, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, list.Sessions, 1)
	require.Equal(t, int64(320), list.Sessions[0].AverageFirstTokenMs)
	require.Equal(t, 1, list.Sessions[0].FirstTokenSamples)
}

// TestFirstTokenMsLegacyPathReportsNotCollected 锁定逃生开关语义：旧读路径
// （EnvDisableStats=1 / 未迁移库）无法保证 usage_requests 具备首字时间列，恒报
// 0 样本 = "未采集"；默认预聚合路径给出真实观测值。两者列数/列序必须一致，
// 否则 scanSessionRows 会按下标错位。
func TestFirstTokenMsLegacyPathReportsNotCollected(t *testing.T) {
	now := time.Date(2026, 9, 17, 19, 0, 0, 0, time.UTC)
	service, bus := newTestService(t, nil, now)
	store := service.Store()

	publishRequestStarted(bus, "sess-esc", "req-e", "trace-1", "turn-1", 1, now.Add(-1000*time.Millisecond), "acme", "m1")
	publishRequestFinished(bus, "sess-esc", "req-e", firstTokenPayload(450))

	t.Setenv(EnvDisableStats, "1")
	legacy, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, legacy.Sessions, 1)
	require.Equal(t, int64(0), legacy.Sessions[0].AverageFirstTokenMs)
	require.Equal(t, 0, legacy.Sessions[0].FirstTokenSamples)
	require.Equal(t, int64(1000), legacy.Sessions[0].TotalDurationMs, "耗时在旧路径仍可聚合")

	legacySummary, err := store.Summarize(Query{GroupBy: "day"})
	require.NoError(t, err)
	require.Equal(t, int64(0), legacySummary.Totals.AverageFirstTokenMs)
	require.Equal(t, 0, legacySummary.Totals.FirstTokenSamples)

	t.Setenv(EnvDisableStats, "")
	stats, err := store.ListSessions(Query{})
	require.NoError(t, err)
	require.Len(t, stats.Sessions, 1)
	require.Equal(t, int64(450), stats.Sessions[0].AverageFirstTokenMs)
	require.Equal(t, 1, stats.Sessions[0].FirstTokenSamples)
}

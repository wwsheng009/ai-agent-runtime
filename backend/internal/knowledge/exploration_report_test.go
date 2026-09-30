package knowledge

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// W7 测量装置的自检：口径、边界、确定性。真实 A/B 的 live 入口见
// exploration_report_live_test.go（env 门控，默认跳过）。

// phase2Sample 构造一条任务样本（只填本用例关心的字段）。
func phase2Sample(taskID string, repeated, unsafe, stale, mismatches int, latency float64) TaskSample {
	return TaskSample{
		TaskID:            taskID,
		RepeatedReadCount: repeated,
		UnsafeReuseCount:  unsafe,
		StaleItemInjected: stale,
		VersionMismatches: mismatches,
		LatencyMS:         latency,
	}
}

// phase2Paired 构造 n 个配对任务：baseline/current 重复读取次数恒定。
func phase2Paired(n, baselineRepeated, currentRepeated int) ([]TaskSample, []TaskSample) {
	baseline := make([]TaskSample, 0, n)
	current := make([]TaskSample, 0, n)
	for i := 0; i < n; i++ {
		taskID := fmt.Sprintf("task-%02d", i)
		baseline = append(baseline, phase2Sample(taskID, baselineRepeated, 0, 0, 0, 0))
		current = append(current, phase2Sample(taskID, currentRepeated, 0, 0, 0, 0))
	}
	return baseline, current
}

func phase2AllCheckedG4() G4Evidence {
	return G4Evidence{Checked: true, Pass: true, Detail: "contextmgr/agent DeepEqual"}
}

// TestMedianCI95OrderStatistic 锁定 order-statistic CI 的精确取值与边界。
func TestMedianCI95OrderStatistic(t *testing.T) {
	// n=20：最近秩中位数 = x_10 = 10；CI = [x_7, x_15] = [7, 15]
	// （P(Bin(20,0.5)<=6)=0.0577 > 0.025；P(Bin(20,0.5)<=14)=0.9793 >= 0.975）。
	samples := make([]float64, 0, 20)
	for i := 1; i <= 20; i++ {
		samples = append(samples, float64(i))
	}
	median, low, high := MedianCI95(samples)
	require.Equal(t, 10.0, median)
	require.Equal(t, 7.0, low)
	require.Equal(t, 15.0, high)

	// 中位数必落在 CI 内（不插值、不越界）。
	for _, n := range []int{1, 2, 3, 5, 19, 20, 21, 50} {
		values := make([]float64, 0, n)
		for i := 1; i <= n; i++ {
			values = append(values, float64(i))
		}
		m, lo, hi := MedianCI95(values)
		require.LessOrEqual(t, lo, m, "n=%d", n)
		require.LessOrEqual(t, m, hi, "n=%d", n)
		require.GreaterOrEqual(t, lo, values[0], "n=%d", n)
		require.LessOrEqual(t, hi, values[n-1], "n=%d", n)
	}

	// 单样本与空样本。
	median, low, high = MedianCI95([]float64{42})
	require.Equal(t, 42.0, median)
	require.Equal(t, 42.0, low)
	require.Equal(t, 42.0, high)
	median, low, high = MedianCI95(nil)
	require.Zero(t, median)
	require.Zero(t, low)
	require.Zero(t, high)
}

// TestSummarizePhase2G1PairedReduction 锁定 G1 的配对口径与 median + CI。
func TestSummarizePhase2G1PairedReduction(t *testing.T) {
	// 20 个配对任务：10 → 6，逐任务下降 40% → median = 0.40，CI = [0.40, 0.40]。
	// 两臂等量延迟样本（100ms）让 G3 也被核验，从而整体判 Pass。
	baseline, current := phase2Paired(MinPhase2Sample, 10, 6)
	for i := range baseline {
		baseline[i].LatencyMS = 100
	}
	for i := range current {
		current[i].LatencyMS = 100
	}
	report := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.Equal(t, MinPhase2Sample, report.G1.PairedTasks)
	require.Zero(t, report.G1.UnpairedTasks)
	require.Equal(t, 200, report.G1.AggregateBaseline)
	require.Equal(t, 120, report.G1.AggregateCurrent)
	require.InDelta(t, 0.40, report.G1.MedianReduction, 1e-9)
	require.InDelta(t, 0.40, report.G1.AggregateReduction, 1e-9)
	require.InDelta(t, 0.40, report.G1.CILow, 1e-9)
	require.InDelta(t, 0.40, report.G1.CIHigh, 1e-9)
	require.True(t, report.G1.Sufficient)
	require.True(t, report.G1.Pass)
	require.Equal(t, Phase2VerdictPass, report.Verdict)

	// 下降不足 30% → G1 Fail → 总判 Fail（样本足够时不得回落 incomplete）。
	baseline, current = phase2Paired(MinPhase2Sample, 10, 9)
	report = SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.InDelta(t, 0.10, report.G1.MedianReduction, 1e-9)
	require.False(t, report.G1.Pass)
	require.Equal(t, Phase2VerdictFail, report.Verdict)

	// 样本不足（19 个配对任务）→ Sufficient=false，即使降幅达标也不得判 Pass。
	baseline, current = phase2Paired(MinPhase2Sample-1, 10, 5)
	report = SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.Equal(t, MinPhase2Sample-1, report.G1.PairedTasks)
	require.False(t, report.G1.Sufficient)
	require.False(t, report.G1.Pass)
	require.Equal(t, Phase2VerdictIncomplete, report.Verdict)
}

// TestSummarizePhase2G1UnpairedAndZeroBaseline 锁定配对失败与零分母的如实计数。
func TestSummarizePhase2G1UnpairedAndZeroBaseline(t *testing.T) {
	baseline := []TaskSample{
		phase2Sample("a", 0, 0, 0, 0, 0), // 配对但分母为 0
		phase2Sample("b", 4, 0, 0, 0, 0),
		phase2Sample("c", 6, 0, 0, 0, 0), // 仅 baseline 出现 → 未配对
	}
	current := []TaskSample{
		phase2Sample("a", 0, 0, 0, 0, 0),
		phase2Sample("b", 2, 0, 0, 0, 0),
		phase2Sample("d", 1, 0, 0, 0, 0), // 仅 current 出现 → 未配对
	}
	report := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.Equal(t, 1, report.G1.PairedTasks)
	require.Equal(t, 2, report.G1.UnpairedTasks)
	require.Equal(t, 1, report.G1.ZeroBaselinePaired)
	require.InDelta(t, 0.50, report.G1.MedianReduction, 1e-9)
	require.Equal(t, 4, report.G1.AggregateBaseline)
	require.Equal(t, 2, report.G1.AggregateCurrent)
}

// TestSummarizePhase2G2HardGates 锁定 G2 的硬门槛语义（任一非零即 Fail）。
func TestSummarizePhase2G2HardGates(t *testing.T) {
	baseline, current := phase2Paired(MinPhase2Sample, 10, 6)
	report := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.True(t, report.G2.Pass)
	require.Empty(t, report.G2.Violations)

	cases := []struct {
		name   string
		mutate func(*[]TaskSample)
		field  string
	}{
		{"unsafe_reuse_count", func(s *[]TaskSample) { (*s)[3].UnsafeReuseCount = 1 }, "unsafe_reuse_count != 0"},
		{"stale_item_injected", func(s *[]TaskSample) { (*s)[7].StaleItemInjected = 1 }, "stale_item_injected != 0"},
		{"version_mismatch", func(s *[]TaskSample) { (*s)[11].VersionMismatches = 1 }, "knowledge_version_mismatch_count != 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, current := phase2Paired(MinPhase2Sample, 10, 6)
			tc.mutate(&current)
			report := SummarizePhase2(nil, current, phase2AllCheckedG4())
			require.False(t, report.G2.Pass)
			require.Equal(t, Phase2VerdictFail, report.Verdict, "硬门槛优先于样本不足")
			require.Contains(t, report.G2.Violations, tc.field)
		})
	}
}

// TestSummarizePhase2G3P95Increase 锁定 G3 的 p95 增幅口径（≤10%）。
func TestSummarizePhase2G3P95Increase(t *testing.T) {
	baseline, current := phase2Paired(MinPhase2Sample, 10, 6)
	for i := range baseline {
		baseline[i].LatencyMS = 100
	}
	for i := range current {
		current[i].LatencyMS = 105
	}
	report := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.True(t, report.G3.Checked)
	require.True(t, report.G3.Sufficient)
	require.InDelta(t, 100, report.G3.BaselineP95, 1e-9)
	require.InDelta(t, 105, report.G3.CurrentP95, 1e-9)
	require.InDelta(t, 0.05, report.G3.IncreaseRatio, 1e-9)
	require.True(t, report.G3.Pass)
	require.Equal(t, Phase2VerdictPass, report.Verdict)

	// 增幅 20% → Fail。
	for i := range current {
		current[i].LatencyMS = 120
	}
	report = SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.InDelta(t, 0.20, report.G3.IncreaseRatio, 1e-9)
	require.False(t, report.G3.Pass)
	require.Equal(t, Phase2VerdictFail, report.Verdict)

	// 缺会话级延迟埋点 → 未核验 → incomplete（不得判 Pass）。
	for i := range baseline {
		baseline[i].LatencyMS = 0
	}
	for i := range current {
		current[i].LatencyMS = 0
	}
	report = SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.False(t, report.G3.Checked)
	require.Equal(t, Phase2VerdictIncomplete, report.Verdict)

	// 延迟样本不足（19 条）→ incomplete。
	for i := range baseline {
		baseline[i].LatencyMS = 100
	}
	for i := range current {
		current[i].LatencyMS = 100
	}
	baseline = baseline[:MinPhase2Sample-1]
	report = SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.True(t, report.G3.Checked)
	require.False(t, report.G3.Sufficient)
	require.Equal(t, Phase2VerdictIncomplete, report.Verdict)
}

// TestSummarizePhase2G4Gate 锁定 G4 未核验/不通过的判定。
func TestSummarizePhase2G4Gate(t *testing.T) {
	baseline, current := phase2Paired(MinPhase2Sample, 10, 6)
	report := SummarizePhase2(baseline, current, G4Evidence{})
	require.Equal(t, Phase2VerdictIncomplete, report.Verdict, "G4 未核验不得判 Pass")

	report = SummarizePhase2(baseline, current, G4Evidence{Checked: true, Pass: false, Detail: "off != baseline"})
	require.Equal(t, Phase2VerdictFail, report.Verdict)
}

// TestSummarizePhase2Deterministic 锁定「同一批记录两次计算一致」。
func TestSummarizePhase2Deterministic(t *testing.T) {
	baseline, current := phase2Paired(MinPhase2Sample, 10, 6)
	for i := range baseline {
		baseline[i].LatencyMS = float64(100 + i)
		baseline[i].UnsafeReuseCount = 0
	}
	for i := range current {
		current[i].LatencyMS = float64(101 + i)
	}
	first := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	second := SummarizePhase2(baseline, current, phase2AllCheckedG4())
	require.Equal(t, first, second)

	// 输入顺序不得影响结果（排序 + 配对在函数内完成）。
	reversed := make([]TaskSample, len(current))
	for i := range current {
		reversed[len(current)-1-i] = current[i]
	}
	third := SummarizePhase2(baseline, reversed, phase2AllCheckedG4())
	require.Equal(t, first, third)
}

// TestCalibrateTokenBudget 锁定 04 §7.6 的「中位数 + 95% CI → CI 上界取整」。
func TestCalibrateTokenBudget(t *testing.T) {
	samples := make([]float64, 0, 20)
	for i := 1; i <= 20; i++ {
		samples = append(samples, float64(i)*10) // 10..200
	}
	budget, median, ciHigh := CalibrateTokenBudget(samples, 100)
	require.Equal(t, 100.0, median)
	require.Equal(t, 150.0, ciHigh)
	require.Equal(t, 200, budget, "CI 上界 150 按 100 向上取整 = 200")

	budget, median, ciHigh = CalibrateTokenBudget(nil, 100)
	require.Zero(t, budget, "无样本必须返回 0，调用方保持默认值")
	require.Zero(t, median)
	require.Zero(t, ciHigh)

	budget, _, _ = CalibrateTokenBudget([]float64{7}, 100)
	require.Equal(t, 100, budget, "单样本 CI 退化到样本值，按粒度取整")
}

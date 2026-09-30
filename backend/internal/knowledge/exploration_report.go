package knowledge

import (
	"math"
	"sort"
	"strings"
)

// W7 验收测量（06 §4 W7 / 04 §7.1–§7.5）：从 ledger / 会话埋点复算 G1–G4。
//
// 口径与不变量：
//   - 纯函数、无 DB、无时钟、无 RNG：同一批输入任意次数复算必须逐位一致
//     （04 §7.1「每个指标必须能从现有数据复算」；06 §4 W7「复算一致性」）。
//   - G1 的置信区间用分布无关的 order-statistic 法：G1 的统计量是 median，
//     不假设正态、不引入 RNG。Phase 1 报告用的是 n=3 的 t 区间（均值口径），
//     两者不可混用。
//   - G2 是硬门槛（04 §7.3）：unsafe_reuse_count = 0 且 stale_item_injected = 0；
//     任一非零即 Fail，样本不足也不例外（真实数据里出现一次就是违反）。
//   - G3 用最近秩 p95（与 baseline.go Percentiles 同口径），增幅 ≤ 10%
//     （04 §7.4「端到端 p95 延迟增幅」；测量点为会话级埋点）。
//   - G4 的运行时等价断言由 contextmgr / agent 的 DeepEqual 用例承载（W5/W7a），
//     本结构只接收其结论（G4Evidence），不自行编造。
//   - 样本不足（n < MinPhase2Sample）一律不得判 Pass：Verdict=incomplete
//     （「N/A 待实测」），这是 06 §4 W7「A/B ≥ 20 任务」的硬口径。
const (
	// MinPhase2Sample 是 G1/G3 判定所需的最小任务样本量（06 §9 G1：样本 ≥ 20）。
	MinPhase2Sample = 20
	// Phase2TargetReduction 是 G1 目标：重复工具调用次数下降 ≥ 30%（06 §9 G1）。
	Phase2TargetReduction = 0.30
	// Phase2MaxP95Increase 是 G3 目标：端到端 p95 延迟增幅 ≤ 10%（06 §9 G3）。
	Phase2MaxP95Increase = 0.10
	// phase2Alpha 是置信区间的显著性水平（95% CI）。
	phase2Alpha = 0.05

	// Phase2VerdictPass 全部门槛在足够样本上通过。
	Phase2VerdictPass = "pass"
	// Phase2VerdictFail 任一硬门槛被违反（G2 非零，或足够样本下 G1/G3/G4 不达）。
	Phase2VerdictFail = "fail"
	// Phase2VerdictIncomplete 样本/埋点不足或 G4 未核验：不得判 Pass（N/A 待实测）。
	Phase2VerdictIncomplete = "incomplete"
)

// TaskSample 是一条任务级测量样本（A/B 两臂同构）。
//
// 数据来源（04 §7.5：任务集必须来自真实 ledger 日志采样）：
//   - RepeatedReadCount / ToolCalls / TotalTokens / ExplorationTokens /
//     UnsafeReuseCount 来自 `usageledger`（TokenUsageHistory 同名列）；
//   - StaleItemInjected 来自 contextmgr BuildResult metadata
//     （`knowledge_stale_item_injected`，当前实现恒为 0，为 Phase 6 预留）；
//   - LatencyMS 来自会话级埋点（usageanalytics 的请求耗时；0 = 该样本无延迟数据）。
//
// TaskID 是两臂配对键（同一任务集在 off / on 两臂各跑一次）；真实 A/B 必须
// 保证两臂任务集一致，否则配对失败会如实反映在 UnpairedTasks 上。
type TaskSample struct {
	TaskID            string  `json:"task_id"`
	RepeatedReadCount int     `json:"repeated_read_count"`
	ToolCalls         int     `json:"tool_calls"`
	TotalTokens       int     `json:"total_tokens"`
	ExplorationTokens int     `json:"exploration_tokens"`
	UnsafeReuseCount  int     `json:"unsafe_reuse_count"`
	StaleItemInjected int     `json:"stale_item_injected"`
	VersionMismatches int     `json:"knowledge_version_mismatch_count"`
	LatencyMS         float64 `json:"latency_ms,omitempty"`
}

// G1Result 是「多轮任务重复工具调用次数下降 ≥ 30%」的复算结果。
type G1Result struct {
	// PairedTasks 是两臂都出现且 baseline 分母 > 0 的配对任务数（CI 的 n）。
	PairedTasks int `json:"paired_tasks"`
	// UnpairedTasks 是只出现在单臂的任务数（配对失败，如实报告）。
	UnpairedTasks int `json:"unpaired_tasks"`
	// ZeroBaselinePaired 是配对成功但 baseline 重复读取为 0 的任务数
	// （相对下降无定义，不进 median/CI 分母）。
	ZeroBaselinePaired int `json:"zero_baseline_paired"`
	// AggregateBaseline / AggregateCurrent 是配对任务的重复读取总数。
	AggregateBaseline int `json:"aggregate_baseline"`
	AggregateCurrent  int `json:"aggregate_current"`
	// AggregateReduction 是配对任务的汇总相对下降（报告展示用）。
	AggregateReduction float64 `json:"aggregate_reduction"`
	// MedianReduction 是逐任务相对下降的最近秩中位数（G1 判据）。
	MedianReduction float64 `json:"median_reduction"`
	// CILow / CIHigh 是 median 的 95% 置信区间（order-statistic 法）。
	CILow  float64 `json:"ci_low"`
	CIHigh float64 `json:"ci_high"`
	// Sufficient 表示 PairedTasks >= MinPhase2Sample。
	Sufficient bool `json:"sufficient"`
	// Pass = Sufficient && MedianReduction >= Phase2TargetReduction。
	Pass bool `json:"pass"`
}

// G2Result 是硬门槛复算（04 §7.3）：unsafe_reuse_count / stale_item_injected /
// knowledge_version_mismatch_count 必须全为 0。
type G2Result struct {
	UnsafeReuseCount  int      `json:"unsafe_reuse_count"`
	StaleItemInjected int      `json:"stale_item_injected"`
	VersionMismatches int      `json:"knowledge_version_mismatch_count"`
	Pass              bool     `json:"pass"`
	Violations        []string `json:"violations,omitempty"`
}

// G3Result 是「端到端 p95 延迟增幅 ≤ 10%」的复算结果。
type G3Result struct {
	// Checked 表示两臂都有延迟样本（会话级埋点缺失时为 false）。
	Checked         bool    `json:"checked"`
	SamplesBaseline int     `json:"samples_baseline"`
	SamplesCurrent  int     `json:"samples_current"`
	BaselineP95     float64 `json:"baseline_p95"`
	CurrentP95      float64 `json:"current_p95"`
	// IncreaseRatio = (CurrentP95 - BaselineP95) / BaselineP95。
	IncreaseRatio float64 `json:"increase_ratio"`
	// Sufficient 表示两臂样本都 >= MinPhase2Sample。
	Sufficient bool `json:"sufficient"`
	Pass       bool `json:"pass"`
}

// G4Evidence 是「关闭 KnowledgeMode 后指标回到基线（可逆）」的核验结论。
//
// 运行时等价性由 W5/W7a 的 DeepEqual 用例（contextmgr/knowledge_test.go、
// agent/agent_knowledge_test.go）承载；本结构只记录其结论与证据出处，
// 测量装置不重复执行运行时断言。
type G4Evidence struct {
	Checked bool   `json:"checked"`
	Pass    bool   `json:"pass"`
	Detail  string `json:"detail,omitempty"`
}

// Phase2Report 是 G1–G4 的复算结果（04 §7.7 报告模板的数据面）。
type Phase2Report struct {
	G1 G1Result   `json:"g1"`
	G2 G2Result   `json:"g2"`
	G3 G3Result   `json:"g3"`
	G4 G4Evidence `json:"g4"`
	// Verdict: pass | fail | incomplete（样本不足时不得判 Pass）。
	Verdict string `json:"verdict"`
}

// SummarizePhase2 从两臂任务样本 + G4 证据复算 G1–G4。
//
// 判定优先级：Fail（硬门槛被违反）> incomplete（样本/埋点不足）> Pass。
// 同一批输入重复调用必须得到逐位相同的结果（06 §4 W7 复算一致性）。
func SummarizePhase2(baseline, current []TaskSample, g4 G4Evidence) Phase2Report {
	report := Phase2Report{
		G1: summarizeG1(baseline, current),
		G2: summarizeG2(baseline, current),
		G3: summarizeG3(baseline, current),
		G4: g4,
	}
	switch {
	case !report.G2.Pass:
		report.Verdict = Phase2VerdictFail
	case report.G4.Checked && !report.G4.Pass:
		report.Verdict = Phase2VerdictFail
	case report.G1.Sufficient && !report.G1.Pass:
		report.Verdict = Phase2VerdictFail
	case report.G3.Checked && report.G3.Sufficient && !report.G3.Pass:
		report.Verdict = Phase2VerdictFail
	case !report.G1.Sufficient || !report.G3.Checked || !report.G3.Sufficient || !report.G4.Checked:
		report.Verdict = Phase2VerdictIncomplete
	default:
		report.Verdict = Phase2VerdictPass
	}
	return report
}

// summarizeG1 复算重复读取的逐任务相对下降（配对）+ median + 95% CI。
func summarizeG1(baseline, current []TaskSample) G1Result {
	out := G1Result{}
	baselineByTask := make(map[string]TaskSample, len(baseline))
	for _, sample := range baseline {
		if key := strings.TrimSpace(sample.TaskID); key != "" {
			baselineByTask[key] = sample
		}
	}
	seenCurrent := make(map[string]bool, len(current))
	reductions := make([]float64, 0, len(current))
	for _, sample := range current {
		key := strings.TrimSpace(sample.TaskID)
		if key == "" {
			out.UnpairedTasks++
			continue
		}
		seenCurrent[key] = true
		base, ok := baselineByTask[key]
		if !ok {
			out.UnpairedTasks++
			continue
		}
		if base.RepeatedReadCount <= 0 {
			out.ZeroBaselinePaired++
			continue
		}
		out.PairedTasks++
		out.AggregateBaseline += base.RepeatedReadCount
		out.AggregateCurrent += sample.RepeatedReadCount
		reductions = append(reductions, 1-float64(sample.RepeatedReadCount)/float64(base.RepeatedReadCount))
	}
	for key := range baselineByTask {
		if !seenCurrent[key] {
			out.UnpairedTasks++
		}
	}
	if out.AggregateBaseline > 0 {
		out.AggregateReduction = 1 - float64(out.AggregateCurrent)/float64(out.AggregateBaseline)
	}
	out.MedianReduction, out.CILow, out.CIHigh = MedianCI95(reductions)
	out.Sufficient = out.PairedTasks >= MinPhase2Sample
	out.Pass = out.Sufficient && out.MedianReduction >= Phase2TargetReduction
	return out
}

// summarizeG2 累加两臂的硬门槛计数；任一非零即 Fail（含样本为空时）。
func summarizeG2(baseline, current []TaskSample) G2Result {
	out := G2Result{}
	for _, samples := range [][]TaskSample{baseline, current} {
		for _, sample := range samples {
			out.UnsafeReuseCount += sample.UnsafeReuseCount
			out.StaleItemInjected += sample.StaleItemInjected
			out.VersionMismatches += sample.VersionMismatches
		}
	}
	out.Pass = out.UnsafeReuseCount == 0 && out.StaleItemInjected == 0 && out.VersionMismatches == 0
	if out.UnsafeReuseCount != 0 {
		out.Violations = append(out.Violations, "unsafe_reuse_count != 0")
	}
	if out.StaleItemInjected != 0 {
		out.Violations = append(out.Violations, "stale_item_injected != 0")
	}
	if out.VersionMismatches != 0 {
		out.Violations = append(out.Violations, "knowledge_version_mismatch_count != 0")
	}
	return out
}

// summarizeG3 复算两臂会话级延迟的 p95 增幅（最近秩法，不插值）。
func summarizeG3(baseline, current []TaskSample) G3Result {
	out := G3Result{}
	baselineLatency := latencySamples(baseline)
	currentLatency := latencySamples(current)
	out.SamplesBaseline = len(baselineLatency)
	out.SamplesCurrent = len(currentLatency)
	if len(baselineLatency) == 0 || len(currentLatency) == 0 {
		return out
	}
	out.Checked = true
	_, out.BaselineP95 = Percentiles(baselineLatency)
	_, out.CurrentP95 = Percentiles(currentLatency)
	switch {
	case out.BaselineP95 > 0:
		out.IncreaseRatio = (out.CurrentP95 - out.BaselineP95) / out.BaselineP95
	case out.CurrentP95 > 0:
		out.IncreaseRatio = math.Inf(1)
	default:
		out.IncreaseRatio = 0
	}
	out.Sufficient = len(baselineLatency) >= MinPhase2Sample && len(currentLatency) >= MinPhase2Sample
	out.Pass = out.Sufficient && out.IncreaseRatio <= Phase2MaxP95Increase
	return out
}

// latencySamples 收集 >0 的延迟样本（0 = 该样本无延迟埋点，不参与 p95）。
func latencySamples(samples []TaskSample) []float64 {
	out := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.LatencyMS > 0 {
			out = append(out, sample.LatencyMS)
		}
	}
	return out
}

// MedianCI95 返回样本的最近秩中位数与 95% 置信区间（order-statistic 法）。
//
// 方法：对 n 个样本，median 的分布无关 CI 由次序统计量给出——
//
//	lower = 最大的 r 使 P(Bin(n, 0.5) <= r-1) <= α/2  （取 x_(r)）
//	upper = 最小的 r 使 P(Bin(n, 0.5) <= r-1) >= 1-α/2（取 x_(r)）
//
// 与最近秩中位数同源：中位数必落在区间内；不插值、无 RNG，同一份样本复算
// 逐位一致（04 §7.1 / 06 §4 W7）。空样本返回 (0,0,0)；单样本返回该值。
func MedianCI95(samples []float64) (median, low, high float64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	n := len(sorted)
	medianIndex := nearestRankIndex(n, 0.5)
	if n == 1 {
		return sorted[0], sorted[0], sorted[0]
	}
	lowIndex := 0
	for r := 1; r <= n; r++ {
		if binomialCDF(n, r-1, 0.5) > phase2Alpha/2 {
			lowIndex = r - 1
			break
		}
	}
	highIndex := n - 1
	for r := 1; r <= n; r++ {
		if binomialCDF(n, r-1, 0.5) >= 1-phase2Alpha/2 {
			highIndex = r - 1
			break
		}
	}
	if lowIndex > medianIndex {
		lowIndex = medianIndex
	}
	if highIndex < medianIndex {
		highIndex = medianIndex
	}
	return sorted[medianIndex], sorted[lowIndex], sorted[highIndex]
}

// binomialCDF 返回 P(X <= k)，X ~ Bin(n, p)。用对数空间计算避免 n 较大时
// 组合数/2^n 溢出；结果确定性（纯浮点累加，顺序固定）。
func binomialCDF(n, k int, p float64) float64 {
	if k < 0 {
		return 0
	}
	if k >= n {
		return 1
	}
	if p <= 0 {
		return 1
	}
	if p >= 1 {
		if k >= n {
			return 1
		}
		return 0
	}
	logP := math.Log(p)
	logQ := math.Log(1 - p)
	sum := 0.0
	for i := 0; i <= k; i++ {
		logTerm := logBinomialCoefficient(n, i) + float64(i)*logP + float64(n-i)*logQ
		sum += math.Exp(logTerm)
	}
	if sum > 1 {
		return 1
	}
	return sum
}

// logBinomialCoefficient 返回 ln(C(n, k))（Lgamma 法，确定性）。
func logBinomialCoefficient(n, k int) float64 {
	if k < 0 || k > n {
		return math.Inf(-1)
	}
	return logGamma(float64(n)+1) - logGamma(float64(k)+1) - logGamma(float64(n-k)+1)
}

// logGamma 丢弃 math.Lgamma 的符号位（本用途参数恒为正，符号位必为 1）。
func logGamma(x float64) float64 {
	value, _ := math.Lgamma(x)
	return value
}

// CalibrateTokenBudget 按 04 §7.6「用中位数 + 95% CI 校准阈值」从实测样本
// 产出 token 预算建议：取 CI 上界按 roundTo 向上取整。
//
// samples 是「被预算对象」的实测成本样本（rune 计）：既可以是单条目行成本
// （broad 行格式），也可以是整条注入消息成本（header + Σ行，语义与
// contextmgr 的预算一致）。无样本返回 0（调用方应保持现有默认值，不得把
// 「无数据」当「阈值为 0」）。这是 W7 实测回写入口：真实 A/B 跑完后用它回写
// contextmgr.DefaultKnowledgeTokens。
func CalibrateTokenBudget(samples []float64, roundTo int) (budget int, median, ciHigh float64) {
	if len(samples) == 0 {
		return 0, 0, 0
	}
	median, _, ciHigh = MedianCI95(samples)
	if roundTo <= 0 {
		roundTo = 1
	}
	budget = int(math.Ceil(ciHigh/float64(roundTo))) * roundTo
	if budget < roundTo {
		budget = roundTo
	}
	return budget, median, ciHigh
}

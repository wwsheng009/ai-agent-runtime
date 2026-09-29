package knowledge

import (
	"math"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

// AttributionBreakdown 是一个分组（全局 / 按 tool / 按 source / 按 project）
// 的 M1–M4 复算结果（ADR-0003 §4.5）。
//
// 所有字段都由同一批 exploration_attribution 行决定：相同输入重复调用
// SummarizeAttribution 必须得到逐位相同的结果（ADR-0003 §8「可复算」）。
type AttributionBreakdown struct {
	Calls        int `json:"calls"`
	Denominator  int `json:"denominator"`
	ZeroBaseline int `json:"zero_baseline"`
	Usable       int `json:"usable"`

	// M1 调用级可用率 = mean(usable)，Phase 1 主门槛（ADR-0003 §4.5）。
	M1 float64 `json:"m1_usable_rate"`
	// M2 覆盖度 = mean(coverage)。
	M2 float64 `json:"m2_coverage_mean"`
	// M3 经济性 = mean(economy)（只在 economy 有定义的样本上取均值；
	// baseline_tokens == 0 的行记 economy = NULL，不参与 M3 分母）。
	M3 float64 `json:"m3_economy_mean"`
	// M4 token 收益 = Σ(baseline_tokens − candidate_tokens) / Σ baseline_tokens。
	M4 float64 `json:"m4_token_gain"`

	CoverageMean float64 `json:"coverage_mean"`
	CoverageP50  float64 `json:"coverage_p50"`
	CoverageP90  float64 `json:"coverage_p90"`
	EconomyMean  float64 `json:"economy_mean"`
	EconomyP50   float64 `json:"economy_p50"`
	EconomyP90   float64 `json:"economy_p90"`
}

// AttributionReport 是 Phase1-shadow 的 M1–M4 报告。
type AttributionReport struct {
	// Alpha 是本次复算 usable 使用的覆盖率阈值。α 校准要求"同一批数据
	// 换多个 α 重算"，因此这里不使用落库时写入的联调值（ADR-0003 §4.2）。
	Alpha float64 `json:"alpha"`
	AttributionBreakdown

	ByTool    map[string]AttributionBreakdown `json:"by_tool,omitempty"`
	BySource  map[string]AttributionBreakdown `json:"by_source,omitempty"`
	ByProject map[string]AttributionBreakdown `json:"by_project,omitempty"`

	FirstCall time.Time `json:"first_call,omitempty"`
	LastCall  time.Time `json:"last_call,omitempty"`
}

// SummarizeAttribution 从归因行复算 M1–M4（ADR-0003 §4.5 / §8）。
//
// 口径：
//   - 分母（Denominator）= baseline_n > 0 的调用；baseline_n == 0 的调用单独
//     计入 ZeroBaseline，不进 M1/M2/M3 分母（D2）。
//   - usable 不信任落库值：按传入 α 用 (coverage >= α) AND (economy <= 1.0)
//     重算，使 α 校准与"复算一致"验证在同一函数上完成。
//   - α <= 0 时退回 DefaultShadowAlpha。
func SummarizeAttribution(records []*entity.ExplorationAttribution, alpha float64) AttributionReport {
	if alpha <= 0 {
		alpha = DefaultShadowAlpha
	}
	report := AttributionReport{
		Alpha:                alpha,
		AttributionBreakdown: summarizeAttributionSlice(records, alpha),
	}
	grouped := map[string]map[string][]*entity.ExplorationAttribution{
		"tool":    {},
		"source":  {},
		"project": {},
	}
	for _, rec := range records {
		if rec == nil {
			continue
		}
		if report.FirstCall.IsZero() || (!rec.CreatedAt.IsZero() && rec.CreatedAt.Before(report.FirstCall)) {
			report.FirstCall = rec.CreatedAt
		}
		if rec.CreatedAt.After(report.LastCall) {
			report.LastCall = rec.CreatedAt
		}
		grouped["tool"][nonEmptyKey(strings.ToLower(rec.Tool))] = append(grouped["tool"][nonEmptyKey(strings.ToLower(rec.Tool))], rec)
		grouped["source"][nonEmptyKey(strings.ToLower(rec.Source))] = append(grouped["source"][nonEmptyKey(strings.ToLower(rec.Source))], rec)
		if key := strings.TrimSpace(rec.ProjectID); key != "" {
			grouped["project"][key] = append(grouped["project"][key], rec)
		}
	}
	report.ByTool = summarizeAttributionGroups(grouped["tool"], alpha)
	report.BySource = summarizeAttributionGroups(grouped["source"], alpha)
	report.ByProject = summarizeAttributionGroups(grouped["project"], alpha)
	return report
}

// CalibrateShadowAlpha 按 04 §7.6「用中位数校准阈值」从 shadow 实测样本产出 α：
// 取分母样本 coverage 的中位数，按 0.05 粒度取整并收敛到 [0.1, 1.0]。
//
// 无样本（或全是零结果调用）时返回 DefaultShadowAlpha（管线联调值）；
// 这不构成"写死阈值"——ADVISORY 值仍由 Phase1-shadow 数据决定，只是数据
// 不足时保持可运行。
func CalibrateShadowAlpha(records []*entity.ExplorationAttribution) float64 {
	var coverages []float64
	for _, rec := range records {
		if rec == nil || rec.BaselineN <= 0 || rec.Coverage == nil {
			continue
		}
		coverages = append(coverages, *rec.Coverage)
	}
	if len(coverages) == 0 {
		return DefaultShadowAlpha
	}
	median := percentileValue(coverages, 0.5)
	rounded := math.Round(median*20) / 20
	if rounded < 0.1 {
		rounded = 0.1
	}
	if rounded > 1 {
		rounded = 1
	}
	return rounded
}

// summarizeAttributionSlice 计算一个记录集（含全部调用）的 M1–M4。
func summarizeAttributionSlice(records []*entity.ExplorationAttribution, alpha float64) AttributionBreakdown {
	var (
		out               AttributionBreakdown
		coverages         []float64
		economies         []float64
		usableCount       int
		coverageSum       float64
		economySum        float64
		baselineTokenSum  int
		candidateTokenSum int
	)
	for _, rec := range records {
		if rec == nil {
			continue
		}
		out.Calls++
		if rec.BaselineN <= 0 {
			out.ZeroBaseline++
			continue
		}
		out.Denominator++
		// usable 用本次 α 重算，而不是读落库值（α 校准需要换阈值重算）。
		if attributionUsable(rec, alpha) {
			usableCount++
		}
		if rec.Coverage != nil {
			coverages = append(coverages, *rec.Coverage)
			coverageSum += *rec.Coverage
		}
		if rec.Economy != nil {
			economies = append(economies, *rec.Economy)
			economySum += *rec.Economy
		}
		baselineTokenSum += rec.BaselineTokens
		candidateTokenSum += rec.CandidateTokens
	}
	out.Usable = usableCount
	if out.Denominator > 0 {
		out.M1 = float64(usableCount) / float64(out.Denominator)
	}
	if len(coverages) > 0 {
		out.M2 = coverageSum / float64(len(coverages))
		out.CoverageMean = out.M2
		out.CoverageP50 = percentileValue(coverages, 0.5)
		out.CoverageP90 = percentileValue(coverages, 0.9)
	}
	if len(economies) > 0 {
		out.M3 = economySum / float64(len(economies))
		out.EconomyMean = out.M3
		out.EconomyP50 = percentileValue(economies, 0.5)
		out.EconomyP90 = percentileValue(economies, 0.9)
	}
	if baselineTokenSum > 0 {
		out.M4 = float64(baselineTokenSum-candidateTokenSum) / float64(baselineTokenSum)
	}
	return out
}

// summarizeAttributionGroups 对每个分组键复算同一套 M1–M4。
func summarizeAttributionGroups(groups map[string][]*entity.ExplorationAttribution, alpha float64) map[string]AttributionBreakdown {
	if len(groups) == 0 {
		return nil
	}
	out := make(map[string]AttributionBreakdown, len(groups))
	for key, records := range groups {
		out[key] = summarizeAttributionSlice(records, alpha)
	}
	return out
}

// attributionUsable 复现 fillShadowMetrics 的判定：
// (coverage >= α) AND (economy <= 1.0)；economy 为 NULL（两侧零成本）视为通过。
func attributionUsable(rec *entity.ExplorationAttribution, alpha float64) bool {
	if rec == nil || rec.Coverage == nil || *rec.Coverage < alpha {
		return false
	}
	if rec.Economy != nil && *rec.Economy > 1.0 {
		return false
	}
	return true
}

// percentileValue 返回最近秩法分位数（与 baseline.go 的 Percentiles 同口径，
// 不插值，保证同一份样本复算逐位一致）。
func percentileValue(samples []float64, percentile float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := make([]float64, len(samples))
	copy(sorted, samples)
	sort.Float64s(sorted)
	return sorted[nearestRankIndex(len(sorted), percentile)]
}

// nonEmptyKey 把空分组键折叠成 "unknown"，避免报告里出现空键。
func nonEmptyKey(key string) string {
	if strings.TrimSpace(key) == "" {
		return "unknown"
	}
	return key
}

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
	// ADR-0008 §4.1 起 grep 行（有 file-level 数据时）以 file_coverage 判定，
	// 该通道的 file-level 独立口径另见 M1File。
	M1 float64 `json:"m1_usable_rate"`
	// M2 覆盖度 = mean(coverage)（行级，ADR-0008 §4.2 保留为诊断）。
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

	// 以下为 ADR-0008 §4.1 的 file-level 复算（grep 主判据，历史行/view 行
	// baseline_files_n = 0 不计入分母）：
	//   FileDenominator = baseline_files_n > 0 的调用数；
	//   M1File          = mean(file_coverage >= α 且 economy <= 1.0)；
	//   FilePrecision   = Σoverlap_files_n / Σbaseline_files_n（文件级精确率）。
	// Answerable / AnswerableRate（candidate_n > 0）是 ADR-0008 §4.2 的诊断项，
	// 不判 Pass/Fail。
	FileDenominator  int     `json:"file_denominator"`
	FileUsable       int     `json:"file_usable"`
	M1File           float64 `json:"m1_file_usable_rate"`
	FileCoverageMean float64 `json:"file_coverage_mean"`
	FileCoverageP50  float64 `json:"file_coverage_p50"`
	FileCoverageP90  float64 `json:"file_coverage_p90"`
	FilePrecision    float64 `json:"file_precision"`
	Answerable       int     `json:"answerable"`
	AnswerableRate   float64 `json:"answerable_rate"`
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
//   - usable 不信任落库值：按传入 α 重算，使 α 校准与"复算一致"验证在同一
//     函数上完成。grep 行有 file-level 数据（baseline_files_n > 0）时按
//     ADR-0008 §4.1 用 file_coverage 判定；view 与历史行保持行级 coverage。
//   - file-level 独立口径（FileDenominator / M1File / FilePrecision 等）与
//     answerable_rate 同批产出，供 ADR-0008 §8 的按通道报告使用。
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
// ADR-0008 §4.1 起 grep 行的主判据是 file-level：有 file-level 数据的行取
// file_coverage，历史行/未启用行回退行级 coverage（§6.2）。注意 §8.1 已定稿
// α = 0.8，本函数只作 Phase 2 复校准入口，当前不覆盖该定稿值。
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
		coverage := *rec.Coverage
		if strings.EqualFold(rec.Tool, "grep") {
			if fileCov, ok := fileCoverage(rec); ok {
				coverage = fileCov
			}
		}
		coverages = append(coverages, coverage)
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
		// ADR-0008 §4.1 file-level 复算累加器。
		fileCoverages   []float64
		fileCoverageSum float64
		fileUsableCount int
		overlapFileSum  int
		baselineFileSum int
		answerable      int
	)
	for _, rec := range records {
		if rec == nil {
			continue
		}
		out.Calls++
		if rec.CandidateN > 0 {
			answerable++
		}
		// ADR-0008 §4.1：file-level 覆盖只在 baseline_files_n > 0 时有定义；
		// 历史行/view 行（新列默认 0）不计入 file-level 分母。
		if cov, ok := fileCoverage(rec); ok {
			fileCoverages = append(fileCoverages, cov)
			fileCoverageSum += cov
			overlapFileSum += rec.OverlapFilesN
			baselineFileSum += rec.BaselineFilesN
			if attributionFileUsable(rec, alpha) {
				fileUsableCount++
			}
		}
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
	out.FileDenominator = len(fileCoverages)
	out.FileUsable = fileUsableCount
	if out.FileDenominator > 0 {
		out.M1File = float64(fileUsableCount) / float64(out.FileDenominator)
		out.FileCoverageMean = fileCoverageSum / float64(out.FileDenominator)
		out.FileCoverageP50 = percentileValue(fileCoverages, 0.5)
		out.FileCoverageP90 = percentileValue(fileCoverages, 0.9)
	}
	if baselineFileSum > 0 {
		out.FilePrecision = float64(overlapFileSum) / float64(baselineFileSum)
	}
	out.Answerable = answerable
	if out.Calls > 0 {
		out.AnswerableRate = float64(answerable) / float64(out.Calls)
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

// attributionUsable 复现 fillShadowMetrics / fillShadowFileUsable 的判定：
// grep 且有 file-level 数据（baseline_files_n > 0）→ 按 ADR-0008 §4.1 用
// file_coverage 判据；其余（view / 历史行 / 新列未启用）→ 行级 coverage 判据。
func attributionUsable(rec *entity.ExplorationAttribution, alpha float64) bool {
	if rec == nil {
		return false
	}
	if strings.EqualFold(rec.Tool, "grep") {
		if _, ok := fileCoverage(rec); ok {
			return attributionFileUsable(rec, alpha)
		}
	}
	return coverageUsable(rec.Coverage, rec.Economy, alpha)
}

// attributionFileUsable 是 grep 的 file-level 判定：
// (file_coverage >= α) AND (economy <= 1.0)；baseline_files_n == 0 时无定义。
func attributionFileUsable(rec *entity.ExplorationAttribution, alpha float64) bool {
	coverage, ok := fileCoverage(rec)
	if !ok {
		return false
	}
	return coverageUsable(&coverage, rec.Economy, alpha)
}

// coverageUsable 是 (coverage >= α) AND (economy <= 1.0) 的共用判定：
// coverage 为 NULL 视为不可用；economy 为 NULL（两侧零成本）视为通过。
func coverageUsable(coverage, economy *float64, alpha float64) bool {
	if coverage == nil || *coverage < alpha {
		return false
	}
	if economy != nil && *economy > 1.0 {
		return false
	}
	return true
}

// fileCoverage 返回 file-level 覆盖率与是否有定义（ADR-0008 §4.1：
// baseline_files_n > 0；历史行与 view 行默认 0 = 未定义）。
func fileCoverage(rec *entity.ExplorationAttribution) (float64, bool) {
	if rec == nil || rec.BaselineFilesN <= 0 {
		return 0, false
	}
	return float64(rec.OverlapFilesN) / float64(rec.BaselineFilesN), true
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

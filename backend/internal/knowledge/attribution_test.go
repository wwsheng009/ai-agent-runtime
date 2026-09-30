package knowledge

import (
	"reflect"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/model/entity"
)

func floatPtr(v float64) *float64 { return &v }

// attributionFixture 构造覆盖四种形态的归因样本：
//   - g1 覆盖满 + 经济性通过 → usable；
//   - g2 覆盖 0.5 + 经济性 2.0 → 经济性失败；
//   - g4 覆盖 0.6 + 经济性 0.8 → 随 α 变化（0.5 通过 / 0.7 失败）；
//   - v1 economy=NULL（baseline_tokens=0）→ 只由覆盖度判定；
//   - zero baseline_n=0 → 不进 M1 分母。
func attributionFixture() []*entity.ExplorationAttribution {
	t0 := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	return []*entity.ExplorationAttribution{
		{
			ID: "g1", Tool: "grep", Source: "heuristic", ProjectID: "ws-a",
			BaselineN: 4, CandidateN: 4, OverlapN: 4, BaselineTokens: 100, CandidateTokens: 50,
			Coverage: floatPtr(1.0), Economy: floatPtr(0.5), Usable: true, KnowledgeMode: "shadow",
			CreatedAt: t0,
		},
		{
			ID: "g2", Tool: "grep", Source: "heuristic", ProjectID: "ws-a",
			BaselineN: 10, CandidateN: 5, OverlapN: 5, BaselineTokens: 200, CandidateTokens: 400,
			Coverage: floatPtr(0.5), Economy: floatPtr(2.0), KnowledgeMode: "shadow",
			CreatedAt: t0.Add(time.Second),
		},
		{
			ID: "g4", Tool: "grep", Source: "heuristic", ProjectID: "ws-b",
			BaselineN: 10, CandidateN: 6, OverlapN: 6, BaselineTokens: 100, CandidateTokens: 80,
			Coverage: floatPtr(0.6), Economy: floatPtr(0.8), KnowledgeMode: "shadow",
			CreatedAt: t0.Add(2 * time.Second),
		},
		{
			ID: "v1", Tool: "view", Source: "heuristic", ProjectID: "ws-b",
			BaselineN: 2, CandidateN: 1, OverlapN: 2, BaselineTokens: 0, CandidateTokens: 10,
			Coverage: floatPtr(1.0), Economy: nil, KnowledgeMode: "shadow",
			CreatedAt: t0.Add(3 * time.Second),
		},
		{
			ID: "zero", Tool: "grep", Source: "heuristic", ProjectID: "ws-b",
			BaselineN: 0, CandidateN: 0, OverlapN: 0, BaselineTokens: 30, CandidateTokens: 0,
			Coverage: nil, Economy: nil, KnowledgeMode: "shadow",
			CreatedAt: t0.Add(4 * time.Second),
		},
	}
}

func TestSummarizeAttributionM1ToM4(t *testing.T) {
	records := attributionFixture()
	report := SummarizeAttribution(records, 0.5)

	if report.Calls != 5 || report.Denominator != 4 || report.ZeroBaseline != 1 {
		t.Fatalf("calls/denominator/zero = %d/%d/%d, want 5/4/1",
			report.Calls, report.Denominator, report.ZeroBaseline)
	}
	if report.Usable != 3 {
		t.Fatalf("usable = %d, want 3 (g1/g4/v1)", report.Usable)
	}
	assertClose(t, "M1", report.M1, 0.75)
	assertClose(t, "M2", report.M2, 0.775)
	assertClose(t, "M3", report.M3, 1.1)                   // (0.5+2.0+0.8)/3，NULL 不参与
	assertClose(t, "M4", report.M4, -0.35)                 // (50-200+20-10)/400
	assertClose(t, "CoverageP50", report.CoverageP50, 0.6) // 样本 [0.5,0.6,1.0,1.0] 的最近秩中位数
	assertClose(t, "CoverageP90", report.CoverageP90, 1.0)

	// α 校准需要"同一批数据换阈值重算"：0.7 时 g4 掉出。
	strict := SummarizeAttribution(records, 0.7)
	if strict.Usable != 2 {
		t.Fatalf("usable@0.7 = %d, want 2 (g1/v1)", strict.Usable)
	}
	assertClose(t, "M1@0.7", strict.M1, 0.5)
	assertClose(t, "M2@0.7", strict.M2, 0.775)

	// 定位能力（ADR-0003 §8）：按 tool/project 分组。
	grep := report.ByTool["grep"]
	if grep.Calls != 4 || grep.Denominator != 3 || grep.ZeroBaseline != 1 {
		t.Fatalf("by tool grep = %+v", grep)
	}
	assertClose(t, "grep.M1", grep.M1, 2.0/3.0)
	view := report.ByTool["view"]
	if view.Calls != 1 || view.Denominator != 1 {
		t.Fatalf("by tool view = %+v", view)
	}
	if len(report.ByProject) != 2 || report.ByProject["ws-b"].Calls != 3 {
		t.Fatalf("by project = %+v", report.ByProject)
	}
}

func TestSummarizeAttributionIsDeterministic(t *testing.T) {
	records := attributionFixture()
	first := SummarizeAttribution(records, 0.5)
	for i := 0; i < 3; i++ {
		again := SummarizeAttribution(records, 0.5)
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("recompute #%d differs:\nfirst=%+v\nagain=%+v", i, first, again)
		}
	}
}

func TestCalibrateShadowAlphaUsesMedian(t *testing.T) {
	records := attributionFixture()
	// 覆盖度样本 [1.0, 0.5, 0.6, 1.0] → 最近秩中位数 0.6。
	if got := CalibrateShadowAlpha(records); got != 0.6 {
		t.Fatalf("CalibrateShadowAlpha = %v, want 0.6", got)
	}
	if got := CalibrateShadowAlpha(nil); got != DefaultShadowAlpha {
		t.Fatalf("CalibrateShadowAlpha(nil) = %v, want DefaultShadowAlpha", got)
	}
	// 全部零结果：没有分母样本 → 保持联调初值，而不是产出 0。
	zeroOnly := []*entity.ExplorationAttribution{{BaselineN: 0}}
	if got := CalibrateShadowAlpha(zeroOnly); got != DefaultShadowAlpha {
		t.Fatalf("CalibrateShadowAlpha(zero-only) = %v, want %v", got, DefaultShadowAlpha)
	}
	// 低覆盖样本收敛到下限 0.1。
	low := []*entity.ExplorationAttribution{
		{BaselineN: 10, OverlapN: 0, Coverage: floatPtr(0.0)},
		{BaselineN: 10, OverlapN: 1, Coverage: floatPtr(0.1)},
	}
	if got := CalibrateShadowAlpha(low); got != 0.1 {
		t.Fatalf("CalibrateShadowAlpha(low) = %v, want 0.1", got)
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	const epsilon = 1e-9
	if diff := got - want; diff > epsilon || diff < -epsilon {
		t.Fatalf("%s = %v, want %v", name, got, want)
	}
}

package knowledge

import (
	"math"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// W3 测试（06 §4 Phase 2 W3 测试清单）：表驱动覆盖 source_weight / agreement /
// staleness / ambiguity、版本比较、阈值边界（== 阈值视为通过）、TTL 滞后兜底、
// config 归一化与 YAML 往返。

const confidenceEpsilon = 1e-9

func almostEqual(a, b float64) bool { return math.Abs(a-b) <= confidenceEpsilon }

// TestSourceWeightClosedSet 钉住 04 §4.4 的六个 source_weight 原值。
func TestSourceWeightClosedSet(t *testing.T) {
	cases := []struct {
		name string
		got  SourceWeight
		want float64
	}{
		{"lsp_resolved", SourceWeightLSPResolved, 0.95},
		{"runtime_evidence", SourceWeightRuntimeEvidence, 0.90},
		{"tree_sitter_resolved", SourceWeightTreeSitterResolved, 0.80},
		{"tree_sitter_heuristic", SourceWeightTreeSitterHeuristic, 0.65},
		{"regex_builtin", SourceWeightRegexBuiltin, 0.55},
		{"fts_lexical", SourceWeightFTSLexical, 0.40},
	}
	for _, tc := range cases {
		if float64(tc.got) != tc.want {
			t.Errorf("%s source_weight = %v, want %v", tc.name, float64(tc.got), tc.want)
		}
		if !tc.got.Valid() {
			t.Errorf("%s source_weight %v not reported valid", tc.name, float64(tc.got))
		}
	}
	if SourceWeight(0.99).Valid() {
		t.Error("source_weight 0.99 不在闭集内，Valid 必须为 false")
	}
}

// TestSourceWeightFromConfidenceParityWithScore 钉住新映射与 version.go 的
// Confidence.Score() 一致（refs.confidence 落库与 Reuse Gate 不得漂移）。
func TestSourceWeightFromConfidenceParityWithScore(t *testing.T) {
	for _, c := range []Confidence{ConfidenceHeuristic, ConfidenceSyntax, ConfidenceSemantic, ConfidenceUnknown} {
		got := float64(SourceWeightFromConfidence(c))
		if want := c.Score(); !almostEqual(got, want) {
			t.Errorf("SourceWeightFromConfidence(%s) = %v, Confidence.Score() = %v", c, got, want)
		}
	}
}

// TestComputeConfidenceSourceWeights 覆盖 source_weight 六个取值（其余分量中性）。
func TestComputeConfidenceSourceWeights(t *testing.T) {
	cases := []struct {
		weight SourceWeight
		want   float64
	}{
		{SourceWeightLSPResolved, 0.95},
		{SourceWeightRuntimeEvidence, 0.90},
		{SourceWeightTreeSitterResolved, 0.80},
		{SourceWeightTreeSitterHeuristic, 0.65},
		{SourceWeightRegexBuiltin, 0.55},
		{SourceWeightFTSLexical, 0.40},
	}
	for _, tc := range cases {
		got := ComputeConfidence(ConfidenceInput{
			SourceWeight: tc.weight,
			Agreement:    AgreementMultiSource,
			Staleness:    0,
			Ambiguity:    AmbiguityNone,
		})
		if !almostEqual(got, tc.want) {
			t.Errorf("confidence(weight=%v) = %v, want %v", float64(tc.weight), got, tc.want)
		}
	}
}

// TestComputeConfidenceAgreement 覆盖 agreement 三个取值（1.0/0.9/0.7）。
func TestComputeConfidenceAgreement(t *testing.T) {
	cases := []struct {
		agreement AgreementFactor
		want      float64 // 0.80 × agreement × (1-0.30) × (1-0.10)
	}{
		{AgreementMultiSource, 0.504},
		{AgreementSingleSource, 0.4536},
		{AgreementConflict, 0.3528},
	}
	for _, tc := range cases {
		got := ComputeConfidence(ConfidenceInput{
			SourceWeight: SourceWeightTreeSitterResolved,
			Agreement:    tc.agreement,
			Staleness:    StalenessAdapterVersionMismatch,
			Ambiguity:    AmbiguityResolved,
		})
		if !almostEqual(got, tc.want) {
			t.Errorf("confidence(agreement=%v) = %v, want %v", float64(tc.agreement), got, tc.want)
		}
	}
}

// TestComputeConfidenceStaleness 覆盖 staleness 四个信号（0.5/0.3/0.3/版本不匹配）。
func TestComputeConfidenceStaleness(t *testing.T) {
	cases := []struct {
		name      string
		staleness StalenessPenalty
		want      float64 // 0.95 × 0.90 × (1-staleness) × (1-0.10)
	}{
		{"none", 0, 0.7695},
		{"content_hash", StalenessContentHashMismatch, 0.38475},
		{"adapter_version", StalenessAdapterVersionMismatch, 0.53865},
		{"parser_version", StalenessParserVersionMismatch, 0.53865},
		{"knowledge_version", StalenessKnowledgeVersionMismatch, 0},
	}
	for _, tc := range cases {
		got := ComputeConfidence(ConfidenceInput{
			SourceWeight: SourceWeightLSPResolved,
			Agreement:    AgreementSingleSource,
			Staleness:    tc.staleness,
			Ambiguity:    AmbiguityResolved,
		})
		if !almostEqual(got, tc.want) {
			t.Errorf("confidence(staleness=%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestComputeConfidenceAmbiguity 覆盖 ambiguity（0.3/0.1/0）。
func TestComputeConfidenceAmbiguity(t *testing.T) {
	cases := []struct {
		ambiguity AmbiguityPenalty
		want      float64 // 0.95 × 0.90 × (1-0.30) × (1-ambiguity)
	}{
		{AmbiguityNone, 0.5985},
		{AmbiguityResolved, 0.53865},
		{AmbiguityUnresolved, 0.41895},
	}
	for _, tc := range cases {
		got := ComputeConfidence(ConfidenceInput{
			SourceWeight: SourceWeightLSPResolved,
			Agreement:    AgreementSingleSource,
			Staleness:    StalenessAdapterVersionMismatch,
			Ambiguity:    tc.ambiguity,
		})
		if !almostEqual(got, tc.want) {
			t.Errorf("confidence(ambiguity=%v) = %v, want %v", float64(tc.ambiguity), got, tc.want)
		}
	}
}

// TestComputeConfidenceDecayCurve 钉住"衰减曲线"：staleness 越大置信度越低，
// 版本不匹配直接归零。
func TestComputeConfidenceDecayCurve(t *testing.T) {
	staleness := []StalenessPenalty{0, StalenessAdapterVersionMismatch, StalenessContentHashMismatch, StalenessKnowledgeVersionMismatch}
	previous := math.Inf(1)
	for _, penalty := range staleness {
		got := ComputeConfidence(ConfidenceInput{
			SourceWeight: SourceWeightLSPResolved,
			Agreement:    AgreementSingleSource,
			Staleness:    penalty,
			Ambiguity:    AmbiguityResolved,
		})
		if got > previous {
			t.Fatalf("decay curve not monotonic: staleness=%v gives %v > previous %v", float64(penalty), got, previous)
		}
		previous = got
	}
	if previous != 0 {
		t.Fatalf("knowledge_version mismatch must decay to 0, got %v", previous)
	}
}

// TestStalenessSignalsPenalty 钉住多信号合并语义：取最大罚分（不相加）。
func TestStalenessSignalsPenalty(t *testing.T) {
	cases := []struct {
		name    string
		signals StalenessSignals
		want    StalenessPenalty
	}{
		{"none", StalenessSignals{}, 0},
		{"content_hash", StalenessSignals{ContentHashMismatch: true}, StalenessContentHashMismatch},
		{"adapter", StalenessSignals{AdapterVersionMismatch: true}, StalenessAdapterVersionMismatch},
		{"parser", StalenessSignals{ParserVersionMismatch: true}, StalenessParserVersionMismatch},
		{"adapter_and_parser_same_root", StalenessSignals{AdapterVersionMismatch: true, ParserVersionMismatch: true}, StalenessAdapterVersionMismatch},
		{"content_beats_versions", StalenessSignals{ContentHashMismatch: true, AdapterVersionMismatch: true}, StalenessContentHashMismatch},
		{"knowledge_version_beats_all", StalenessSignals{KnowledgeVersionMismatch: true, ContentHashMismatch: true, ParserVersionMismatch: true}, StalenessKnowledgeVersionMismatch},
	}
	for _, tc := range cases {
		if got := tc.signals.Penalty(); got != tc.want {
			t.Errorf("%s: penalty = %v, want %v", tc.name, float64(got), float64(tc.want))
		}
	}
}

// TestComputeConfidenceClampAndNaN 钉住兜底：越界输入不得产出 >1 的置信度，
// NaN 按 0（fail closed）。
func TestComputeConfidenceClampAndNaN(t *testing.T) {
	if got := ComputeConfidence(ConfidenceInput{
		SourceWeight: SourceWeight(5),
		Agreement:    AgreementMultiSource,
	}); got != 1 {
		t.Fatalf("out-of-range product = %v, want clamped 1", got)
	}
	if got := ComputeConfidence(ConfidenceInput{
		SourceWeight: SourceWeight(math.NaN()),
		Agreement:    AgreementMultiSource,
	}); got != 0 {
		t.Fatalf("NaN confidence = %v, want 0", got)
	}
}

// TestConfidenceInputValidate 覆盖闭集校验（外部输入必须显式失败）。
func TestConfidenceInputValidate(t *testing.T) {
	valid := ConfidenceInput{
		SourceWeight: SourceWeightLSPResolved,
		Agreement:    AgreementSingleSource,
		Staleness:    StalenessContentHashMismatch,
		Ambiguity:    AmbiguityUnresolved,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid input rejected: %v", err)
	}
	invalid := []ConfidenceInput{
		{SourceWeight: SourceWeight(0.99), Agreement: AgreementSingleSource},
		{SourceWeight: SourceWeightLSPResolved, Agreement: AgreementFactor(0.95)},
		{SourceWeight: SourceWeightLSPResolved, Agreement: AgreementSingleSource, Staleness: StalenessPenalty(1.5)},
		{SourceWeight: SourceWeightLSPResolved, Agreement: AgreementSingleSource, Ambiguity: AmbiguityPenalty(-0.1)},
	}
	for i, in := range invalid {
		if err := in.Validate(); err == nil {
			t.Errorf("invalid input #%d accepted", i)
		}
	}
}

// TestCompareKnowledgeVersion 覆盖版本比较：相等/不等/任一侧为空（fail closed）。
func TestCompareKnowledgeVersion(t *testing.T) {
	cases := []struct {
		name    string
		current string
		stored  string
		want    VersionStatus
	}{
		{"match", "wv1_abc", "wv1_abc", VersionStatusMatch},
		{"match_with_whitespace", " wv1_abc ", "wv1_abc", VersionStatusMatch},
		{"mismatch", "wv1_abc", "wv1_def", VersionStatusMismatch},
		{"stored_empty", "wv1_abc", "", VersionStatusUnknown},
		{"current_empty", "", "wv1_abc", VersionStatusUnknown},
		{"both_empty", "", "", VersionStatusUnknown},
	}
	for _, tc := range cases {
		if got := CompareKnowledgeVersion(tc.current, tc.stored); got != tc.want {
			t.Errorf("%s: CompareKnowledgeVersion(%q, %q) = %q, want %q", tc.name, tc.current, tc.stored, got, tc.want)
		}
	}
}

// TestReuseScopeNormalize 钉住未知作用域按跨任务（更保守）处理。
func TestReuseScopeNormalize(t *testing.T) {
	if got := ReuseScopeTask.Normalize(); got != ReuseScopeTask {
		t.Errorf("task scope normalized to %q", got)
	}
	for _, raw := range []ReuseScope{"", ReuseScopeCrossTask, "unknown"} {
		if got := raw.Normalize(); got != ReuseScopeCrossTask {
			t.Errorf("scope %q normalized to %q, want %q", raw, got, ReuseScopeCrossTask)
		}
	}
}

// TestReuseGateThresholdBoundaries 覆盖 04 §4.4 的阈值策略与边界
// （== 阈值视为通过；0.50–0.80 待验证；< 0.50 触发探索）。
func TestReuseGateThresholdBoundaries(t *testing.T) {
	const version = "wv1_abc"
	cases := []struct {
		name            string
		scope           ReuseScope
		write           bool
		confidence      float64
		wantDecision    ReuseDecision
		wantUsable      bool
		wantVerify      bool
		wantProvisional bool
		wantReason      string
	}{
		{"task_read_direct", ReuseScopeTask, false, 0.95, ReuseDecisionReuse, true, false, false, ReuseReasonOK},
		{"task_read_verify_read_below_boundary", ReuseScopeTask, false, 0.90, ReuseDecisionReuse, true, false, false, ReuseReasonOK},
		{"task_read_reuse_floor_boundary", ReuseScopeTask, false, 0.80, ReuseDecisionReuse, true, true, false, ReuseReasonVerifyReadBelow},
		{"task_read_provisional", ReuseScopeTask, false, 0.79, ReuseDecisionReuseVerify, true, true, true, ReuseReasonProvisional},
		{"task_read_explore_floor_boundary", ReuseScopeTask, false, 0.50, ReuseDecisionReuseVerify, true, true, true, ReuseReasonProvisional},
		{"task_read_below_explore_floor", ReuseScopeTask, false, 0.49, ReuseDecisionExplore, false, false, false, ReuseReasonBelowExploreFloor},
		{"task_write_verify", ReuseScopeTask, true, 0.95, ReuseDecisionReuse, true, true, false, ReuseReasonWriteVerify},
		{"task_write_floor_boundary", ReuseScopeTask, true, 0.90, ReuseDecisionReuse, true, true, false, ReuseReasonWriteVerify},
		{"task_write_provisional", ReuseScopeTask, true, 0.89, ReuseDecisionReuseVerify, true, true, true, ReuseReasonProvisional},
		{"cross_task_verify", ReuseScopeCrossTask, false, 0.95, ReuseDecisionReuse, true, true, false, ReuseReasonCrossTaskVerify},
		{"cross_task_floor_boundary", ReuseScopeCrossTask, false, 0.90, ReuseDecisionReuse, true, true, false, ReuseReasonCrossTaskVerify},
		{"cross_task_provisional", ReuseScopeCrossTask, false, 0.89, ReuseDecisionReuseVerify, true, true, true, ReuseReasonProvisional},
		{"unknown_scope_is_conservative_cross_task", "", false, 0.85, ReuseDecisionReuseVerify, true, true, true, ReuseReasonProvisional},
	}
	for _, tc := range cases {
		got := EvaluateReuseGate(ReuseGateInput{
			Confidence:    tc.confidence,
			Scope:         tc.scope,
			Write:         tc.write,
			StoredVersion: version,
			Current:       VersionObservation{Version: version},
		})
		if got.Decision != tc.wantDecision || got.Usable != tc.wantUsable ||
			got.Verify != tc.wantVerify || got.Provisional != tc.wantProvisional {
			t.Errorf("%s: got {decision=%s usable=%v verify=%v provisional=%v}, want {decision=%s usable=%v verify=%v provisional=%v}",
				tc.name, got.Decision, got.Usable, got.Verify, got.Provisional,
				tc.wantDecision, tc.wantUsable, tc.wantVerify, tc.wantProvisional)
		}
		if got.Reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, got.Reason, tc.wantReason)
		}
		if got.Stale {
			t.Errorf("%s: Stale = true, want false（版本匹配）", tc.name)
		}
		if got.Version != VersionStatusMatch {
			t.Errorf("%s: version = %q, want %q", tc.name, got.Version, VersionStatusMatch)
		}
		if !almostEqual(got.Confidence, tc.confidence) {
			t.Errorf("%s: confidence = %v, want %v", tc.name, got.Confidence, tc.confidence)
		}
	}
}

// TestReuseGateVersionUnavailable 钉住"版本不匹配/未知 → 直接不可用"（04 §4.4）。
func TestReuseGateVersionUnavailable(t *testing.T) {
	cases := []struct {
		name       string
		current    string
		stored     string
		wantReason string
		wantStatus VersionStatus
	}{
		{"mismatch", "wv1_new", "wv1_old", ReuseReasonVersionMismatch, VersionStatusMismatch},
		{"stored_unknown", "wv1_new", "", ReuseReasonVersionUnknown, VersionStatusUnknown},
		{"current_unknown", "", "wv1_old", ReuseReasonVersionUnknown, VersionStatusUnknown},
	}
	for _, tc := range cases {
		got := EvaluateReuseGate(ReuseGateInput{
			Confidence:    0.99,
			Scope:         ReuseScopeTask,
			StoredVersion: tc.stored,
			Current:       VersionObservation{Version: tc.current},
		})
		if got.Decision != ReuseDecisionExplore || got.Usable || !got.Stale || got.Verify || got.Provisional {
			t.Errorf("%s: got {decision=%s usable=%v stale=%v verify=%v provisional=%v}, want explore/不可用/Stale",
				tc.name, got.Decision, got.Usable, got.Stale, got.Verify, got.Provisional)
		}
		if got.Reason != tc.wantReason {
			t.Errorf("%s: reason = %q, want %q", tc.name, got.Reason, tc.wantReason)
		}
		if got.Version != tc.wantStatus {
			t.Errorf("%s: version = %q, want %q", tc.name, got.Version, tc.wantStatus)
		}
		if got.Confidence != 0 {
			t.Errorf("%s: confidence = %v, want 0（版本不可用时归零）", tc.name, got.Confidence)
		}
	}
}

// TestReuseGateNaNConfidenceFailsClosed 钉住坏置信度不得被当作可用。
func TestReuseGateNaNConfidenceFailsClosed(t *testing.T) {
	const version = "wv1_abc"
	got := EvaluateReuseGate(ReuseGateInput{
		Confidence:    math.NaN(),
		StoredVersion: version,
		Current:       VersionObservation{Version: version},
	})
	if got.Usable || got.Decision != ReuseDecisionExplore || got.Reason != ReuseReasonBelowExploreFloor {
		t.Fatalf("NaN confidence gate = %+v, want explore/不可用", got)
	}
	if got.Confidence != 0 {
		t.Fatalf("NaN confidence normalized to %v, want 0", got.Confidence)
	}
}

// TestReuseGateVersionObservationTTLLagFallback 钉住 stale 判定的 TTL 滞后兜底：
// 版本串相等但快照超过信任窗口（W2 Recorder 的 30s TTL）时，不得直接复用，
// 必须补一次验证读取；未跟踪观测时间（零值）时退化为纯版本比较。
func TestReuseGateVersionObservationTTLLagFallback(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	const version = "wv1_abc"
	base := ReuseGateInput{
		Confidence:    0.95,
		Scope:         ReuseScopeTask,
		StoredVersion: version,
		Current:       VersionObservation{Version: version, ObservedAt: now.Add(-29 * time.Second)},
		Now:           now,
	}

	if got := EvaluateReuseGate(base); got.Verify || got.Reason != ReuseReasonOK {
		t.Fatalf("fresh observation: got {verify=%v reason=%q}, want {false ok}", got.Verify, got.Reason)
	}

	lagging := base
	lagging.Current.ObservedAt = now.Add(-31 * time.Second)
	got := EvaluateReuseGate(lagging)
	if got.Decision != ReuseDecisionReuse || !got.Usable || !got.Verify || got.Provisional {
		t.Fatalf("lagging observation: got %+v, want reuse + 强制验证", got)
	}
	if got.Reason != ReuseReasonVersionObservationLag {
		t.Fatalf("lagging observation reason = %q, want %q", got.Reason, ReuseReasonVersionObservationLag)
	}

	boundary := base
	boundary.Current.ObservedAt = now.Add(-DefaultReuseVersionTTL)
	if got := EvaluateReuseGate(boundary); !got.Verify || got.Reason != ReuseReasonVersionObservationLag {
		t.Fatalf("TTL boundary: got {verify=%v reason=%q}, want 滞后兜底（age >= TTL）", got.Verify, got.Reason)
	}

	untracked := base
	untracked.Current.ObservedAt = time.Time{}
	if got := EvaluateReuseGate(untracked); got.Verify || got.Reason != ReuseReasonOK {
		t.Fatalf("untracked observation time: got {verify=%v reason=%q}, want 纯版本比较", got.Verify, got.Reason)
	}
}

// TestReuseGateConsumesRecorderNodeRow 钉住与 W2 采集数据的衔接：Gate 直接消费
// 已落库行（confidence + knowledge_version），不重算、不造数据。
func TestReuseGateConsumesRecorderNodeRow(t *testing.T) {
	const version = "wv1_abc"
	node := ExplorationNode{
		Confidence:       observedFileNodeConfidence,
		KnowledgeVersion: version,
	}
	input := node.GateInput(ReuseScopeTask, false, VersionObservation{Version: version})
	if input.Confidence != node.Confidence || input.StoredVersion != node.KnowledgeVersion ||
		input.Scope != ReuseScopeTask || input.Write {
		t.Fatalf("node gate input = %+v, want 字段搬运自节点", input)
	}
	if got := EvaluateReuseGate(input); got.Decision != ReuseDecisionReuse || got.Verify || got.Reason != ReuseReasonOK {
		t.Fatalf("file node gate = %+v, want reuse/ok", got)
	}

	queryNode := ExplorationNode{
		Confidence:       observedQueryNodeConfidence,
		KnowledgeVersion: version,
	}
	got := EvaluateReuseGate(queryNode.GateInput(ReuseScopeCrossTask, false, VersionObservation{Version: version}))
	if got.Decision != ReuseDecisionReuse || !got.Verify || got.Reason != ReuseReasonCrossTaskVerify {
		t.Fatalf("cross-task query node gate = %+v, want reuse + 强制验证", got)
	}

	stale := EvaluateReuseGate(node.GateInput(ReuseScopeTask, false, VersionObservation{Version: "wv1_next"}))
	if stale.Decision != ReuseDecisionExplore || stale.Usable || !stale.Stale || stale.Reason != ReuseReasonVersionMismatch {
		t.Fatalf("drifted version gate = %+v, want explore/不可用", stale)
	}

	// 低置信度走公式重算：runtime_evidence × 单来源 × adapter 不一致
	// = 0.90 × 0.90 × 0.70 = 0.567 → 0.50–0.80 待验证带。
	computed := ComputeConfidence(ConfidenceInput{
		SourceWeight: SourceWeightRuntimeEvidence,
		Agreement:    AgreementSingleSource,
		Staleness:    StalenessAdapterVersionMismatch,
		Ambiguity:    AmbiguityNone,
	})
	if !almostEqual(computed, 0.567) {
		t.Fatalf("computed confidence = %v, want 0.567", computed)
	}
	provisional := EvaluateReuseGate(ReuseGateInput{
		Confidence:    computed,
		StoredVersion: version,
		Current:       VersionObservation{Version: version},
	})
	if provisional.Decision != ReuseDecisionReuseVerify || !provisional.Provisional || !provisional.Verify {
		t.Fatalf("provisional gate = %+v, want reuse_verify + 待验证", provisional)
	}
}

// TestPlannerConfigDefaultsAndNormalize 覆盖 config 归一化：零值/部分配置/
// 显式非默认值都按预期落位。
func TestPlannerConfigDefaultsAndNormalize(t *testing.T) {
	want := PlannerConfig{
		MinReuseConfidence:   DefaultMinReuseConfidence,
		WriteReuseConfidence: DefaultWriteReuseConfidence,
		CrossTaskConfidence:  DefaultCrossTaskConfidence,
		VerifyReadBelow:      DefaultVerifyReadBelow,
		ExploreBelow:         DefaultExploreBelow,
	}
	if got := DefaultPlannerConfig(); got != want {
		t.Fatalf("DefaultPlannerConfig() = %+v, want %+v", got, want)
	}
	if got := (PlannerConfig{}).Normalize(); got != want {
		t.Fatalf("zero PlannerConfig normalized to %+v, want %+v", got, want)
	}
	partial := PlannerConfig{MinReuseConfidence: 0.85}.Normalize()
	if partial.MinReuseConfidence != 0.85 {
		t.Fatalf("explicit min_reuse_confidence = %v, want 0.85（不得被默认值覆盖）", partial.MinReuseConfidence)
	}
	if partial.WriteReuseConfidence != DefaultWriteReuseConfidence ||
		partial.CrossTaskConfidence != DefaultCrossTaskConfidence ||
		partial.VerifyReadBelow != DefaultVerifyReadBelow ||
		partial.ExploreBelow != DefaultExploreBelow {
		t.Fatalf("partial normalize = %+v, want 其余字段取默认值", partial)
	}
	if got := DefaultConfig().Planner; got != want {
		t.Fatalf("DefaultConfig().Planner = %+v, want %+v", got, want)
	}
	if got := (Config{}).Normalize().Planner; got != want {
		t.Fatalf("Config.Normalize().Planner = %+v, want %+v", got, want)
	}
}

// TestPlannerConfigValidate 覆盖阈值域与排序校验。
func TestPlannerConfigValidate(t *testing.T) {
	valid := DefaultPlannerConfig()
	valid.MinReuseConfidence = 0.85
	valid.ExploreBelow = 0.55
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid planner config rejected: %v", err)
	}
	invalid := []PlannerConfig{
		{MinReuseConfidence: 1.5},
		{WriteReuseConfidence: 1.2},
		{CrossTaskConfidence: 2},
		{VerifyReadBelow: 1.01},
		{ExploreBelow: 0.90, MinReuseConfidence: 0.80},
	}
	for i, cfg := range invalid {
		if err := cfg.Validate(); err == nil {
			t.Errorf("invalid planner config #%d accepted: %+v", i, cfg)
		}
	}
	if err := (Config{Mode: ModeOff, Planner: PlannerConfig{MinReuseConfidence: 1.5}}).Validate(); err == nil {
		t.Error("Config.Validate 必须冒泡 planner 校验错误")
	}
}

// TestPlannerConfigReuseFloor 覆盖各作用域/写语义的直接复用阈值。
func TestPlannerConfigReuseFloor(t *testing.T) {
	cfg := DefaultPlannerConfig()
	cases := []struct {
		scope ReuseScope
		write bool
		want  float64
	}{
		{ReuseScopeTask, false, 0.80},
		{ReuseScopeTask, true, 0.90},
		{ReuseScopeCrossTask, false, 0.90},
		{ReuseScopeCrossTask, true, 0.90},
		{"", false, 0.90},
	}
	for _, tc := range cases {
		if got := cfg.ReuseFloor(tc.scope, tc.write); got != tc.want {
			t.Errorf("ReuseFloor(scope=%q, write=%v) = %v, want %v", tc.scope, tc.write, got, tc.want)
		}
	}
	custom := PlannerConfig{CrossTaskConfidence: 0.95}
	if got := custom.ReuseFloor(ReuseScopeCrossTask, false); got != 0.95 {
		t.Errorf("custom cross-task floor = %v, want 0.95", got)
	}
}

type knowledgeYAMLFixture struct {
	Knowledge Config `yaml:"knowledge"`
}

// TestPlannerConfigYAMLRoundTrip 覆盖 `knowledge.planner.*` 的解析与序列化往返。
func TestPlannerConfigYAMLRoundTrip(t *testing.T) {
	var parsed knowledgeYAMLFixture
	if err := yaml.Unmarshal([]byte(`
knowledge:
  mode: shadow
  planner:
    min_reuse_confidence: 0.85
    write_reuse_confidence: 0.95
    cross_task_confidence: 0.92
    verify_read_below: 0.88
    explore_below: 0.55
`), &parsed); err != nil {
		t.Fatalf("unmarshal planner config: %v", err)
	}
	want := PlannerConfig{
		MinReuseConfidence:   0.85,
		WriteReuseConfidence: 0.95,
		CrossTaskConfidence:  0.92,
		VerifyReadBelow:      0.88,
		ExploreBelow:         0.55,
	}
	if parsed.Knowledge.Planner != want {
		t.Fatalf("parsed planner = %+v, want %+v", parsed.Knowledge.Planner, want)
	}
	if err := parsed.Knowledge.WithWorkspace("ws").Validate(); err != nil {
		t.Fatalf("parsed planner config rejected: %v", err)
	}

	var absent knowledgeYAMLFixture
	if err := yaml.Unmarshal([]byte("knowledge:\n  mode: off\n"), &absent); err != nil {
		t.Fatalf("unmarshal without planner: %v", err)
	}
	if absent.Knowledge.Planner != (PlannerConfig{}) {
		t.Fatalf("absent planner = %+v, want zero value", absent.Knowledge.Planner)
	}
	if got := absent.Knowledge.Normalize().Planner; got != DefaultPlannerConfig() {
		t.Fatalf("absent planner normalized = %+v, want defaults", got)
	}

	data, err := yaml.Marshal(knowledgeYAMLFixture{Knowledge: Config{
		Mode:    ModeOn,
		Planner: PlannerConfig{MinReuseConfidence: 0.85},
	}})
	if err != nil {
		t.Fatalf("marshal planner config: %v", err)
	}
	if !strings.Contains(string(data), "min_reuse_confidence: 0.85") {
		t.Fatalf("marshaled YAML missing planner key:\n%s", string(data))
	}
	var round knowledgeYAMLFixture
	if err := yaml.Unmarshal(data, &round); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	if round.Knowledge.Planner.MinReuseConfidence != 0.85 {
		t.Fatalf("round-trip min_reuse_confidence = %v, want 0.85", round.Knowledge.Planner.MinReuseConfidence)
	}
}

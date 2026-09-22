package agent

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
)

func mainAgentRouteTestConfig() *LoopReActConfig {
	return &LoopReActConfig{
		MaxSteps:        10,
		EnableToolCalls: true,
		Provider:        "anthropic",
		Model:           "claude-baseline",
		ReasoningEffort: "medium",
		MainAgentRouting: &agentconfig.AICLIMainAgentRoutingConfig{
			Enabled:                      true,
			Levels:                       []string{"easy", "normal", "hard"},
			DefaultDifficulty:            "normal",
			DowngradeConfirmSteps:        3,
			MinDwellSteps:                2,
			MaxInvalidReportsPerTurn:     3,
			CostGuardMode:                agentconfig.MainAgentCostGuardModeSoft,
			ExpensiveLevels:              []string{"hard"},
			MaxConsecutiveExpensiveSteps: 4,
			Profiles: map[string]agentconfig.AICLISubagentRouteProfile{
				"easy": {Model: "claude-easy", ReasoningEffort: "low"},
				"hard": {Model: "claude-hard", ReasoningEffort: "high"},
			},
		},
	}
}

func mainAgentRouteTestLoop(t *testing.T) (*ReActLoop, *LoopReActConfig) {
	t.Helper()
	host := mainAgentRouteTestConfig()
	loop := NewReActLoop(nil, nil, host)
	if loop.config == host {
		t.Fatal("NewReActLoop must not keep the caller's config pointer")
	}
	loop.beginTurnRoute("session_test", "trace_test")
	t.Cleanup(func() { loop.endTurnRoute("session_test", "trace_test") })
	return loop, host
}

func reportDifficulty(t *testing.T, loop *ReActLoop, difficulty string) (string, error) {
	t.Helper()
	return loop.reportPredictedDifficulty("session_test", "trace_test", 1, map[string]interface{}{"difficulty": difficulty})
}

// TestNewReActLoopDeepClonesRoutingConfig 覆盖 §5.2：值拷贝 + 嵌套深拷贝。
// 直接改宿主对象会让 route 跨 turn 泄漏，也会污染子 Agent 复用的父配置。
func TestNewReActLoopDeepClonesRoutingConfig(t *testing.T) {
	host := mainAgentRouteTestConfig()
	loop := NewReActLoop(nil, nil, host)

	if loop.config == host {
		t.Fatal("loop.config must be a copy")
	}
	if loop.config.MainAgentRouting == host.MainAgentRouting {
		t.Fatal("MainAgentRouting must be deep-cloned")
	}
	loop.config.MainAgentRouting.Profiles["hard"] = agentconfig.AICLISubagentRouteProfile{Model: "mutated"}
	loop.config.MainAgentRouting.Levels = append(loop.config.MainAgentRouting.Levels, "expert")
	loop.config.Provider = "mutated"

	if got := host.MainAgentRouting.Profiles["hard"].Model; got != "claude-hard" {
		t.Fatalf("host profile mutated: %q", got)
	}
	if len(host.MainAgentRouting.Levels) != 3 {
		t.Fatalf("host levels mutated: %v", host.MainAgentRouting.Levels)
	}
	if host.Provider != "anthropic" {
		t.Fatalf("host provider mutated: %q", host.Provider)
	}
}

// TestMainAgentRouteInertWhenDisabled 覆盖「默认关闭时零行为变化」。
func TestMainAgentRouteInertWhenDisabled(t *testing.T) {
	for name, routing := range map[string]*agentconfig.AICLIMainAgentRoutingConfig{
		"nil":      nil,
		"disabled": {Enabled: false, Levels: []string{"normal"}},
	} {
		cfg := mainAgentRouteTestConfig()
		cfg.MainAgentRouting = routing
		loop := NewReActLoop(nil, nil, cfg)

		if _, ok := loop.mainAgentRoutingToolDefinition(); ok {
			t.Fatalf("%s: tool must not be registered", name)
		}
		loop.beginTurnRoute("s", "t")
		if loop.config.Provider != "anthropic" || loop.config.Model != "claude-baseline" {
			t.Fatalf("%s: route must stay at baseline, got %s/%s", name, loop.config.Provider, loop.config.Model)
		}
		if _, err := reportDifficulty(t, loop, "hard"); err == nil {
			t.Fatalf("%s: reporting must be rejected while routing is disabled", name)
		}
		loop.endTurnRoute("s", "t")
	}
}

// TestTurnFloorIsLatchedAndBaselineRestored 覆盖 §5.2/§5.10：turn 入口冻结下限，
// 任何退出路径都写回基线（MG3）。
func TestTurnFloorIsLatchedAndBaselineRestored(t *testing.T) {
	loop, host := mainAgentRouteTestLoop(t)

	if loop.config.Model != "claude-baseline" {
		t.Fatalf("floor for default difficulty must be the baseline, got %q", loop.config.Model)
	}
	if got := loop.mainAgentRoute.floorSource; got != mainAgentRouteSourceBaseline {
		t.Fatalf("floor source = %q, want %q", got, mainAgentRouteSourceBaseline)
	}

	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("report hard: %v", err)
	}
	if loop.config.Model != "claude-hard" || loop.config.ReasoningEffort != "high" {
		t.Fatalf("route not applied: %s/%s", loop.config.Model, loop.config.ReasoningEffort)
	}

	loop.endTurnRoute("session_test", "trace_test")
	if loop.config.Model != "claude-baseline" || loop.config.ReasoningEffort != "medium" {
		t.Fatalf("baseline not restored: %s/%s", loop.config.Model, loop.config.ReasoningEffort)
	}
	if host.Model != "claude-baseline" || host.Provider != "anthropic" {
		t.Fatalf("host config polluted: %s/%s", host.Provider, host.Model)
	}
	if loop.mainAgentRoute.baselineCaptured {
		t.Fatal("route state must be cleared after the turn")
	}
}

// TestEscalationAppliesImmediately 覆盖 §5.5：升级不需要连续确认。
func TestEscalationAppliesImmediately(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)

	out, err := reportDifficulty(t, loop, "hard")
	if err != nil {
		t.Fatalf("report: %v", err)
	}
	if out != "[route reported: hard]" {
		t.Fatalf("placeholder = %q", out)
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("escalation must apply on the first report, got %q", loop.config.Model)
	}
	if loop.config.Provider != "anthropic" {
		t.Fatalf("provider must stay inherited, got %q", loop.config.Provider)
	}
	if loop.mainAgentRoute.activeDifficulty != "hard" {
		t.Fatalf("active difficulty = %q", loop.mainAgentRoute.activeDifficulty)
	}
}

// TestDowngradeRequiresConfirmAndDwell 覆盖 §5.5：降级同时受连续确认步数与最小
// 驻留步数约束，升级可立即生效。
func TestDowngradeRequiresConfirmAndDwell(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("escalate: %v", err)
	}

	loop.onStepBoundary("session_test", "trace_test", 2)
	if _, err := reportDifficulty(t, loop, "easy"); err != nil {
		t.Fatalf("downgrade #1: %v", err)
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("downgrade must not apply on the first report, got %q", loop.config.Model)
	}

	loop.onStepBoundary("session_test", "trace_test", 3)
	if _, err := reportDifficulty(t, loop, "easy"); err != nil {
		t.Fatalf("downgrade #2: %v", err)
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("downgrade must wait for %d confirmations, got %q", loop.config.MainAgentRouting.DowngradeConfirmSteps, loop.config.Model)
	}

	loop.onStepBoundary("session_test", "trace_test", 4)
	if _, err := reportDifficulty(t, loop, "easy"); err != nil {
		t.Fatalf("downgrade #3: %v", err)
	}
	if loop.config.Model != "claude-easy" {
		t.Fatalf("confirmed downgrade must apply, got %q", loop.config.Model)
	}
}

// TestDowngradeBlockedByMinDwell 覆盖 §5.5：连续确认满足但驻留不足时仍不生效。
func TestDowngradeBlockedByMinDwell(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("escalate: %v", err)
	}

	// 同一 step 内连续三次上报：确认数够，但驻留步数为 0。
	for i := 0; i < 3; i++ {
		if _, err := reportDifficulty(t, loop, "easy"); err != nil {
			t.Fatalf("downgrade #%d: %v", i+1, err)
		}
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("min_dwell_steps must gate the downgrade, got %q", loop.config.Model)
	}
}

// TestCostGuardResetsRouteAndAllowsLaterEscalation 覆盖 §5.7。
func TestCostGuardResetsRouteAndAllowsLaterEscalation(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("escalate: %v", err)
	}

	for step := 2; step <= 4; step++ {
		loop.onStepBoundary("session_test", "trace_test", step)
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("guard must not trip before the limit, got %q", loop.config.Model)
	}

	// 第 4 个昂贵 step 达到 limit=4：还原基线。
	loop.onStepBoundary("session_test", "trace_test", 5)
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("cost guard must restore the baseline, got %q", loop.config.Model)
	}
	if loop.mainAgentRoute.costGuardTrips != 1 {
		t.Fatalf("cost guard trips = %d, want 1", loop.mainAgentRoute.costGuardTrips)
	}
	if loop.mainAgentRoute.consecutiveExpensiveSteps != 0 {
		t.Fatalf("counter must restart after a trip, got %d", loop.mainAgentRoute.consecutiveExpensiveSteps)
	}

	// soft 模式：还原后允许再次升级。
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("re-escalate: %v", err)
	}
	if loop.config.Model != "claude-hard" {
		t.Fatalf("soft guard must allow re-escalation, got %q", loop.config.Model)
	}
}

// TestHardCostGuardBlocksFurtherEscalation 覆盖 §5.7 的 hard 模式。
func TestHardCostGuardBlocksFurtherEscalation(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	loop.config.MainAgentRouting.CostGuardMode = agentconfig.MainAgentCostGuardModeHard
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("escalate: %v", err)
	}
	for step := 2; step <= 5; step++ {
		loop.onStepBoundary("session_test", "trace_test", step)
	}
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("hard guard must restore the baseline, got %q", loop.config.Model)
	}

	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("re-escalate: %v", err)
	}
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("hard guard must reject further escalation, got %q", loop.config.Model)
	}
}

// TestInvalidReportsDisableTurnRouting 覆盖 §6.2 的连续非法上报熔断。
func TestInvalidReportsDisableTurnRouting(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)

	for i := 0; i < 2; i++ {
		if _, err := reportDifficulty(t, loop, "impossible"); err == nil {
			t.Fatalf("report #%d must be rejected", i+1)
		}
	}
	if loop.mainAgentRoute.disabledForTurn {
		t.Fatal("routing must stay enabled below the limit")
	}
	if _, err := reportDifficulty(t, loop, "impossible"); err == nil {
		t.Fatal("third invalid report must be rejected")
	}
	if !loop.mainAgentRoute.disabledForTurn {
		t.Fatal("routing must be disabled once the invalid limit is reached")
	}

	// 熔断后合法上报也只回占位行，不再改道。
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("post-limit report: %v", err)
	}
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("disabled turn must not reroute, got %q", loop.config.Model)
	}
}

// TestExpertRequiresOptIn 覆盖 §5.9：expert 未开启时按非法上报处理，而不是静默降级。
func TestExpertRequiresOptIn(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	loop.config.MainAgentRouting.Levels = append(loop.config.MainAgentRouting.Levels, "expert")
	loop.config.MainAgentRouting.Profiles["expert"] = agentconfig.AICLISubagentRouteProfile{Model: "claude-expert"}

	if _, err := reportDifficulty(t, loop, "expert"); err == nil {
		t.Fatal("expert must be rejected while allow_expert=false")
	}
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("rejected expert must not reroute, got %q", loop.config.Model)
	}

	loop.config.MainAgentRouting.AllowExpert = true
	if _, err := reportDifficulty(t, loop, "expert"); err != nil {
		t.Fatalf("opt-in expert: %v", err)
	}
	if loop.config.Model != "claude-expert" {
		t.Fatalf("opted-in expert must reroute, got %q", loop.config.Model)
	}
}

// TestUnlistedDifficultyIsRejected 覆盖 §5.8/§6.1：levels 是白名单。
func TestUnlistedDifficultyIsRejected(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	loop.config.MainAgentRouting.Levels = []string{"normal", "hard"}
	if _, err := reportDifficulty(t, loop, "easy"); err == nil {
		t.Fatal("difficulty outside levels must be rejected")
	}
}

// TestSameDifficultyReportIsNoop 覆盖 R5：重复上报同一档位不产生新 route。
func TestSameDifficultyReportIsNoop(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	if _, err := reportDifficulty(t, loop, "normal"); err != nil {
		t.Fatalf("report normal: %v", err)
	}
	if loop.config.Model != "claude-baseline" {
		t.Fatalf("default difficulty must not reroute, got %q", loop.config.Model)
	}
	if loop.mainAgentRoute.stepsWithOverride != 0 {
		t.Fatalf("no-op report must not count as an override: %d", loop.mainAgentRoute.stepsWithOverride)
	}
}

// TestPredictToolDefinitionIsStableAndHidesBackends 覆盖 INV-2/INV-3 与 §5.4 的
// 「删除因果披露」要求。
func TestPredictToolDefinitionIsStableAndHidesBackends(t *testing.T) {
	cfg := mainAgentRouteTestConfig().MainAgentRouting
	first := predictTaskDifficultyToolDefinition(cfg)
	second := predictTaskDifficultyToolDefinition(cfg)

	if !reflect.DeepEqual(first, second) {
		t.Fatal("tool definition must be byte-stable for the same config")
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatal("tool definition JSON must be byte-stable")
	}

	lower := strings.ToLower(first.Description)
	for _, forbidden := range []string{"provider", "model", "next step", "next-step"} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("description must not disclose %q: %s", forbidden, first.Description)
		}
	}

	properties, ok := first.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing: %v", first.Parameters)
	}
	difficulty, ok := properties["difficulty"].(map[string]interface{})
	if !ok {
		t.Fatalf("difficulty property missing: %v", properties)
	}
	enum, ok := difficulty["enum"].([]string)
	if !ok {
		t.Fatalf("enum missing: %v", difficulty)
	}
	if !reflect.DeepEqual(enum, []string{"easy", "hard", "normal"}) {
		t.Fatalf("enum must be sorted for stability, got %v", enum)
	}
	if _, ok := properties["rationale"]; !ok {
		t.Fatal("rationale must stay optional and available")
	}
	if required, ok := first.Parameters["required"].([]string); !ok || len(required) != 1 || required[0] != "difficulty" {
		t.Fatalf("required must contain only difficulty: %v", first.Parameters["required"])
	}
}

// TestRouteSourceEnumIsClosed 锁定 §5.10 规则 3 的四值闭集。
func TestRouteSourceEnumIsClosed(t *testing.T) {
	got := []string{
		mainAgentRouteSourcePredicted,
		mainAgentRouteSourceBaseline,
		mainAgentRouteSourceFailoverCandidate,
		mainAgentRouteSourceHealthExhaustedBaseline,
	}
	want := []string{"predicted", "baseline", "failover_candidate", "health_exhausted_baseline"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("route source enum drifted: %v", got)
	}
	if modelrouting.SourceFailoverCandidate != mainAgentRouteSourceFailoverCandidate {
		t.Fatalf("failover source must reuse the modelrouting value, got %q", modelrouting.SourceFailoverCandidate)
	}
}

// --- §5.10 健康维度闩锁（U15/U17 的本地口径） ---

// markMainAgentTargetUnhealthy 把 (provider, model) 打到熔断「打开」，模拟上游故障。
func markMainAgentTargetUnhealthy(t *testing.T, provider, model string) {
	t.Helper()
	registry := providerhealth.Default()
	now := time.Now()
	for i := 0; i < providerhealth.DefaultFailureThreshold; i++ {
		if result := registry.Observe(provider, model, llm.FailureCategoryProviderError, now); result.Opened {
			return
		}
	}
	t.Fatalf("provider %s/%s did not open after %d failures", provider, model, providerhealth.DefaultFailureThreshold)
}

func mainAgentHealthGateTestLoop(t *testing.T, provider string, mutate func(cfg *LoopReActConfig)) *ReActLoop {
	t.Helper()
	host := mainAgentRouteTestConfig()
	host.Provider = provider
	host.MainAgentRouting.HealthGate = agentconfig.AICLIMainAgentHealthGateConfig{RespectProviderHealth: true}
	if mutate != nil {
		mutate(host)
	}
	providerhealth.Default().Reset()
	loop := NewReActLoop(nil, nil, host)
	t.Cleanup(func() {
		loop.endTurnRoute("session_test", "trace_test")
		providerhealth.Default().Reset()
	})
	return loop
}

// TestTurnFloorConsultsProviderHealth 是 U15 的**正向控制**：turn 入口确实使用健康
// 门禁。若这里不生效，下面的「turn 内闩锁」用例就会因为健康门禁整体失效而假绿。
func TestTurnFloorConsultsProviderHealth(t *testing.T) {
	const provider = "route-floor-health-test"
	loop := mainAgentHealthGateTestLoop(t, provider, func(cfg *LoopReActConfig) {
		// 让 floor 解析的目标本身就是候选档位（hard profile ⇒ claude-hard）。
		cfg.MainAgentRouting.DefaultDifficulty = "hard"
	})
	markMainAgentTargetUnhealthy(t, provider, "claude-hard")

	loop.beginTurnRoute("session_test", "trace_test")

	// 候选链被健康门禁耗尽 ⇒ 显式降级到 turn 基线 + 独立 source（§5.10 规则 3/4）。
	if got := loop.config.Model; got != "claude-baseline" {
		t.Fatalf("health-exhausted floor must degrade to baseline, got model %q", got)
	}
	if got := loop.mainAgentRoute.floorSource; got != mainAgentRouteSourceHealthExhaustedBaseline {
		t.Fatalf("floor source = %q, want %q", got, mainAgentRouteSourceHealthExhaustedBaseline)
	}
	if got := loop.mainAgentRoute.floor.Model; got != "claude-baseline" {
		t.Fatalf("floor model = %q, want baseline", got)
	}
}

// TestInTurnReportLatchesProviderHealth 覆盖 §5.10 规则 1 / U15：健康维度在 turn 内
// 冻结。turn 中途把 provider 打成 open，难度上报仍必须按配置解析——若 turn 内重新
// 查询 providerhealth，这里会得到基线（候选被摘除），MG5 的双源振荡就此复发。
func TestInTurnReportLatchesProviderHealth(t *testing.T) {
	const provider = "route-latch-health-test"
	loop := mainAgentHealthGateTestLoop(t, provider, nil)
	loop.beginTurnRoute("session_test", "trace_test")

	if got := loop.config.Model; got != "claude-baseline" {
		t.Fatalf("healthy turn floor = %q, want baseline", got)
	}

	// turn 内翻转健康状态：half-open 探测正是靠「放行请求」判定恢复，逐 step 重解析
	// 会让探测永远无法成功（§5.10 的观测反噬）。
	markMainAgentTargetUnhealthy(t, provider, "claude-hard")

	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("report hard: %v", err)
	}
	if got := loop.config.Model; got != "claude-hard" {
		t.Fatalf("turn 内难度上报不得重新查询 providerhealth：model = %q, want %q", got, "claude-hard")
	}
	// steps_with_override 与 steps_total 同口径（逐步计数）：上报在 step 1 的 act
	// 阶段生效，真正使用该 route 的是 step 2，因此计数落在下一个 step 边界上
	// （缺陷 A 修复：计数从「上报次数」改为「偏离基线的 step 数」）。
	loop.onStepBoundary("session_test", "trace_test", 2)
	if got := loop.mainAgentRoute.stepsWithOverride; got != 1 {
		t.Fatalf("stepsWithOverride = %d, want 1", got)
	}
}

// TestMainAgentHealthSourceHasSingleGuardedCallSite 是 A13 建议的机械检查：把
// 「turn 内不得查询健康」钉成结构约束，避免后续重构悄悄破坏闩锁。
func TestMainAgentHealthSourceHasSingleGuardedCallSite(t *testing.T) {
	raw, err := os.ReadFile("main_agent_route.go")
	if err != nil {
		t.Fatalf("read main_agent_route.go: %v", err)
	}
	source := string(raw)

	// 恰好两处：mainAgentRouteHealthSource 的返回类型 + providerhealth.Default()。
	if got := strings.Count(source, "providerhealth."); got != 2 {
		t.Fatalf("providerhealth 引用点 = %d, want 2（只允许 mainAgentRouteHealthSource 内一处）；"+
			"新增引用点会破坏 turn 内健康闩锁（§5.10 规则 1）", got)
	}
	if got := strings.Count(source, "mainAgentRouteHealthSource("); got != 2 {
		t.Fatalf("mainAgentRouteHealthSource 出现 %d 次, want 2（1 处定义 + 1 处调用）", got)
	}

	call := strings.Index(source, "if health := mainAgentRouteHealthSource(")
	if call < 0 {
		t.Fatal("mainAgentRouteHealthSource 必须在 `if consultHealth` 守卫内被调用")
	}
	guard := strings.LastIndex(source[:call], "if consultHealth {")
	if guard < 0 {
		t.Fatal("mainAgentRouteHealthSource 的调用点必须位于 `if consultHealth {` 之内")
	}
	// 守卫与调用之间不得出现函数边界：否则说明调用漂到了别的函数里。
	if between := source[guard:call]; strings.Contains(between, "\nfunc ") {
		t.Fatalf("mainAgentRouteHealthSource 调用点漂出了守卫函数：%q", between)
	}
}

// TestTurnEndRestoresBaselineAfterHealthDegradedFloor 覆盖 I4 的本地口径：即使 turn
// 入口因健康降级而改写了 loop.config，turn 结束也必须还原**构造时的基线**，
// 而不是「本 turn 的 floor」——否则健康降级会跨 turn 泄漏（MG3）。
func TestTurnEndRestoresBaselineAfterHealthDegradedFloor(t *testing.T) {
	const provider = "route-restore-health-test"
	loop := mainAgentHealthGateTestLoop(t, provider, nil)
	loop.beginTurnRoute("session_test", "trace_test")
	markMainAgentTargetUnhealthy(t, provider, "claude-hard")
	if _, err := reportDifficulty(t, loop, "hard"); err != nil {
		t.Fatalf("report hard: %v", err)
	}
	if got := loop.config.Model; got != "claude-hard" {
		t.Fatalf("model = %q, want claude-hard", got)
	}

	loop.endTurnRoute("session_test", "trace_test")

	if got := loop.config.Model; got != "claude-baseline" {
		t.Fatalf("turn 结束必须还原基线，got %q", got)
	}
	if got := loop.config.Provider; got != provider {
		t.Fatalf("turn 结束必须还原基线 provider，got %q", got)
	}
	if got := loop.config.ReasoningEffort; got != "medium" {
		t.Fatalf("turn 结束必须还原基线 reasoning_effort，got %q", got)
	}
}

// --- 缺陷修复回归（plan B 真实在线实测发现，契约来源见方案 §6.2） ---

// captureMainAgentRouteEvents 把 agent 的 runtime 事件挂到独立 bus 上，按类型收集
// payload 副本。emitRuntimeEvent 走 Agent.emitRuntimeEvent → Bus.Publish 同步派发，
// 因此返回后可直接断言。
func captureMainAgentRouteEvents(t *testing.T, agent *Agent) map[string][]map[string]interface{} {
	t.Helper()
	bus := runtimeevents.NewBus()
	collected := map[string][]map[string]interface{}{}
	for _, eventType := range []string{
		runtimeevents.EventMainAgentRouteApplied,
		runtimeevents.EventMainAgentRouteCleared,
	} {
		eventType := eventType
		bus.Subscribe(eventType, func(event runtimeevents.Event) {
			payload := make(map[string]interface{}, len(event.Payload))
			for key, value := range event.Payload {
				payload[key] = value
			}
			collected[eventType] = append(collected[eventType], payload)
		})
	}
	agent.SetEventBus(bus)
	return collected
}

// TestTurnFloorOverrideIsCountedInStepAccounting 是缺陷 A 的回归。
//
// 实测（plan B，2026-09-22）：一个 turn 的 step 全程走非基线 route，route_cleared
// 却报 steps_total=1 / steps_with_override=0。根因是 stepsWithOverride 只在
// applyPredictedDifficulty 自增，turn floor 与成本护栏两条改道路径从不计数；而
// default_difficulty 映射到 ≠ 用户基线的档位是常态（实测 normal → opencode.ai/
// mimo-v2.5-pro，会话基线是 hanhe/deepseek-v4.1-flash），于是该字段系统性少报。
func TestTurnFloorOverrideIsCountedInStepAccounting(t *testing.T) {
	host := mainAgentRouteTestConfig()
	// hard 有 profile ⇒ turn floor 解析到 claude-hard，偏离基线 claude-baseline。
	host.MainAgentRouting.DefaultDifficulty = "hard"
	loop := NewReActLoop(nil, nil, host)
	loop.beginTurnRoute("session_test", "trace_test")
	t.Cleanup(func() { loop.endTurnRoute("session_test", "trace_test") })

	if got := loop.config.Model; got != "claude-hard" {
		t.Fatalf("前置条件不成立：turn floor = %q, want claude-hard", got)
	}

	loop.onStepBoundary("session_test", "trace_test", 1)

	if got := loop.mainAgentRoute.stepsTotal; got != 1 {
		t.Fatalf("stepsTotal = %d, want 1", got)
	}
	if got := loop.mainAgentRoute.stepsWithOverride; got != 1 {
		t.Fatalf("turn floor 改道必须计入 steps_with_override：got %d, want 1（缺陷 A）", got)
	}
}

// TestBaselineTurnCountsNoOverride 是缺陷 A 的负向控制：floor 等于基线时不得计数。
// 缺了它，上一个用例可能因为「step 边界无条件自增」而假绿。
func TestBaselineTurnCountsNoOverride(t *testing.T) {
	loop, _ := mainAgentRouteTestLoop(t)
	for step := 1; step <= 3; step++ {
		loop.onStepBoundary("session_test", "trace_test", step)
	}
	if got := loop.mainAgentRoute.stepsTotal; got != 3 {
		t.Fatalf("stepsTotal = %d, want 3", got)
	}
	if got := loop.mainAgentRoute.stepsWithOverride; got != 0 {
		t.Fatalf("基线 turn 不得计入 steps_with_override：got %d, want 0", got)
	}
}

// TestRouteAppliedPayloadCarriesContractFields 是缺陷 B 的回归：§6.2 声明的
// route_applied 字段必须齐备。
//
// 实测（plan B）：payload 只有 difficulty/model/provider/reason/reasoning_effort/
// route_changed/source/step/trace_id/turn_id，缺 baseline_provider/baseline_model/
// rationale/candidates/input_tokens/output_tokens；DB 旁证是 usage_routes 的
// candidates_json 与 warnings_json 两列长度均为 0——而 §6.2 说 candidates 是
// 「为什么没选某个候选」的唯一证据来源。
//
// 缺陷 D（同族，本轮补齐）：payload 从**任何**路径都不发射 route_warnings，而
// ingest 层（usageanalytics/ingest_routes.go:311）却按 payload["route_warnings"]
// 读它——于是 usage_routes.warnings_json 是结构性恒空列：即使决策真的带告警
// （route_health_exhausted_parent / difficulty_promoted_by_keyword:* 等），
// 这些降级在审计中也完全无痕。子代理侧一直有该字段（child_factory.go:249）。
func TestRouteAppliedPayloadCarriesContractFields(t *testing.T) {
	host := mainAgentRouteTestConfig()
	host.MainAgentRouting.DefaultDifficulty = "hard"
	agent := &Agent{config: &Config{Name: "route-payload-agent"}}
	loop := NewReActLoop(agent, nil, host)
	collected := captureMainAgentRouteEvents(t, agent)

	loop.beginTurnRoute("session_test", "trace_test")
	loop.endTurnRoute("session_test", "trace_test")

	applied := collected[runtimeevents.EventMainAgentRouteApplied]
	if len(applied) != 1 {
		t.Fatalf("route_applied 事件数 = %d, want 1", len(applied))
	}
	for _, key := range []string{
		"step", "difficulty", "source", "provider", "model",
		"baseline_provider", "baseline_model", "rationale", "candidates",
		"route_warnings", "input_tokens", "output_tokens",
	} {
		if _, ok := applied[0][key]; !ok {
			t.Fatalf("route_applied 缺少 §6.2 契约字段 %q；实际 payload = %v", key, applied[0])
		}
	}
	// token 字段允许为 0，但类型必须是数值，下游才能统一解析。
	for _, key := range []string{"input_tokens", "output_tokens"} {
		if _, ok := applied[0][key].(int); !ok {
			t.Fatalf("%s 必须是数值（§6.2 允许为 0），got %T", key, applied[0][key])
		}
	}
	// route_warnings 必须非 nil：typed-nil 切片序列化成 JSON null，ingest 的
	// `raw == nil` 分支会把它当「字段缺失」，恒空列缺陷就会静默复发。空切片
	// 序列化成 []，下游才能把「评估过、无告警」与「生产者不上报」区分开。
	switch value := applied[0]["route_warnings"].(type) {
	case []string:
		if value == nil {
			t.Fatalf("route_warnings 不得为 nil 切片（会序列化成 null）：%v", applied[0])
		}
	case []interface{}:
		if value == nil {
			t.Fatalf("route_warnings 不得为 nil 切片（会序列化成 null）：%v", applied[0])
		}
	default:
		t.Fatalf("route_warnings 类型 = %T, want []string", applied[0]["route_warnings"])
	}
}

// TestRestoredRouteReportsEffectiveBaseline 是缺陷 C 的回归。
//
// 会话路径的真实形状：buildLocalChatLoopConfig 只填 ReasoningEffort
// （chat_actor_host.go:2206-2243），provider/model 留在 agent.config，由
// requestProvider/requestModel 的回退链提供（loop.go:308/320）。修复前基线只快照
// loop.config，于是 route_cleared 的 restored_provider/restored_model 恒为空串——
// 而 §6.2 说这两个字段存在的唯一目的就是证明「确实还原到了正确基线」。
func TestRestoredRouteReportsEffectiveBaseline(t *testing.T) {
	host := mainAgentRouteTestConfig()
	// 模拟会话路径：loop.config 不带 provider/model，真实值在 agent.config。
	host.Provider = ""
	host.Model = ""
	agent := &Agent{config: &Config{Name: "chat-agent", Provider: "hanhe", Model: "deepseek-v4.1-flash"}}
	loop := NewReActLoop(agent, nil, host)
	collected := captureMainAgentRouteEvents(t, agent)

	loop.beginTurnRoute("session_test", "trace_test")

	applied := collected[runtimeevents.EventMainAgentRouteApplied]
	if len(applied) != 1 {
		t.Fatalf("route_applied 事件数 = %d, want 1", len(applied))
	}
	if got := applied[0]["baseline_provider"]; got != "hanhe" {
		t.Fatalf("baseline_provider = %v, want hanhe（必须走 requestProvider 回退链）", got)
	}
	if got := applied[0]["baseline_model"]; got != "deepseek-v4.1-flash" {
		t.Fatalf("baseline_model = %v, want deepseek-v4.1-flash", got)
	}

	loop.endTurnRoute("session_test", "trace_test")

	cleared := collected[runtimeevents.EventMainAgentRouteCleared]
	if len(cleared) != 1 {
		t.Fatalf("route_cleared 事件数 = %d, want 1", len(cleared))
	}
	if got := cleared[0]["restored_provider"]; got != "hanhe" {
		t.Fatalf("restored_provider = %v, want hanhe（缺陷 C）", got)
	}
	if got := cleared[0]["restored_model"]; got != "deepseek-v4.1-flash" {
		t.Fatalf("restored_model = %v, want deepseek-v4.1-flash（缺陷 C）", got)
	}
	if got := cleared[0]["restored_effort"]; got != "medium" {
		t.Fatalf("restored_effort = %v, want medium", got)
	}
	// 写回必须保持精确：loop.config 的空值不能被固化成显式覆盖（那会改变 config
	// 语义，让「未指定」变成「显式指定为会话默认值」）。
	if loop.config.Provider != "" || loop.config.Model != "" {
		t.Fatalf("回写必须保持空值语义，got %q/%q", loop.config.Provider, loop.config.Model)
	}
	// 还原后请求路径的有效 route 必须回到基线。
	if got := loop.requestProvider(); got != "hanhe" {
		t.Fatalf("还原后 requestProvider = %q, want hanhe", got)
	}
	if got := loop.requestModel(); got != "deepseek-v4.1-flash" {
		t.Fatalf("还原后 requestModel = %q, want deepseek-v4.1-flash", got)
	}
}

// TestMainAgentRouteWarningsNormalization 钉住 mainAgentRouteWarnings 的两条不变量。
//
// ① nil 必须归一成**非 nil** 空切片：typed-nil 切片序列化成 JSON null，而 ingest
// （usageanalytics/ingest_routes.go:334）把 `raw == nil` 当「字段缺失」处理，于是
// 缺陷 D 的恒空列会静默复发——这条断言是那个缺陷的防回退闸门。
// ② 必须返回副本：payload 在发射后仍被下游异步读取，若与 decision 共享底层数组，
// 后续对 decision 的写入会污染已发射的事件。
func TestMainAgentRouteWarningsNormalization(t *testing.T) {
	if got := mainAgentRouteWarnings(modelrouting.RouteDecision{}); got == nil || len(got) != 0 {
		t.Fatalf("空决策必须归一为非 nil 空切片，got %#v", got)
	}
	if got := mainAgentRouteWarnings(modelrouting.RouteDecision{Warnings: []string{}}); got == nil {
		t.Fatalf("空告警列表必须归一为非 nil 空切片，got %#v", got)
	}

	decision := modelrouting.RouteDecision{Warnings: []string{"provider_fallback_parent"}}
	got := mainAgentRouteWarnings(decision)
	if len(got) != 1 || got[0] != "provider_fallback_parent" {
		t.Fatalf("告警必须原样透传，got %#v", got)
	}
	decision.Warnings[0] = "mutated_after_emit"
	if got[0] != "provider_fallback_parent" {
		t.Fatalf("返回值必须是副本，不得与 decision 共享底层数组，got %#v", got)
	}
}

package agent

// iterate_goal_test.go — iterate_goal 状态机与进展门控测试（P2-2'）。
//
// 覆盖（对齐 v2 方案 §4 P2-2' 测试计划 + DSH tool-ralph.spec.ts 矩阵）：
//   - 首轮 complete/blocked 直接返回；
//   - continue 链推进；轮数耗尽 budget-limited 且 rounds_started == maxRounds；
//   - 进展门控：连续 no-progress 达阈值 → stalled 提前止损（G6）；
//   - 超部署上限的 max_rounds 拒绝（含实测值）；
//   - 非 JSON 输出的连续熔断（过渡期形态识别）；
//   - 内建分类器的复述检测。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// scriptedScheduler 用注入的每轮脚本驱动 RunIterateGoal：
// script[i] 是第 i+1 轮子级的最终文本输出。实现 SubagentRoundRunner 接口，
// 使编排逻辑（RunIterateGoal）与具体调度器解耦（G2 依赖倒置）。
type scriptedScheduler struct {
	script []string
	calls  int
}

func (s *scriptedScheduler) RunChildren(_ context.Context, _ SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	output := "(no script)"
	if s.calls < len(s.script) {
		output = s.script[s.calls]
	}
	s.calls++
	childID := buildSubagentSessionID(tasks[0].ID)
	return []SubagentResult{{
		ID:        tasks[0].ID,
		SessionID: childID,
		Success:   true,
		Summary:   output,
		Usage:     &types.TokenUsage{},
	}}, nil
}

func handoffJSON(t *testing.T, h Handoff) string {
	t.Helper()
	encoded, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

func TestIterateGoalCompletesFirstRound(t *testing.T) {
	sched := &scriptedScheduler{script: []string{
		handoffJSON(t, Handoff{Status: HandoffComplete, Summary: "done", Evidence: []string{"tests pass"}}),
	}}
	outcome, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "polish doc", 0, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalComplete || outcome.RoundsStarted != 1 {
		t.Fatalf("expected complete in 1 round, got %+v", outcome)
	}
	if outcome.Report == nil || outcome.Report.Status != HandoffComplete {
		t.Fatalf("terminal report mismatch: %+v", outcome.Report)
	}
	if sched.calls != 1 {
		t.Fatalf("expected exactly one round run, got %d", sched.calls)
	}
}

func TestIterateGoalBlocksImmediately(t *testing.T) {
	sched := &scriptedScheduler{script: []string{
		handoffJSON(t, Handoff{Status: HandoffBlocked, Summary: "need key", Blocker: "missing API key"}),
	}}
	outcome, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "obj", 0, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalBlocked || outcome.RoundsStarted != 1 {
		t.Fatalf("expected blocked in 1 round, got %+v", outcome)
	}
}

func TestIterateGoalContinueChainThenBudgetLimited(t *testing.T) {
	// 每轮 continue 且 nextSteps 递进（有进展），直到轮数耗尽。
	script := make([]string, 0, 3)
	for round := 1; round <= 3; round++ {
		script = append(script, handoffJSON(t, Handoff{
			Status:    HandoffContinue,
			Summary:   "progressed",
			NextSteps: []string{strings.Repeat("step", round)},
		}))
	}
	sched := &scriptedScheduler{script: script}
	outcome, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "obj", 3, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalBudgetLimited || outcome.RoundsStarted != 3 {
		t.Fatalf("expected budget-limited at 3 rounds, got %+v", outcome)
	}
	if outcome.Report == nil || outcome.Report.Status != HandoffContinue {
		t.Fatalf("budget-limited must carry the last continue report, got %+v", outcome.Report)
	}
	if sched.calls != 3 {
		t.Fatalf("expected 3 rounds run, got %d", sched.calls)
	}
}

func TestIterateGoalStallGateStopsEarly(t *testing.T) {
	// 首轮建立基线 + 连续 3 轮完全相同的 nextSteps（内建分类器判 no-progress）
	// → 第 4 轮触发 stalled，不烧满 16 轮预算（G6）。
	steps := []string{"same next step"}
	script := make([]string, 0, 4)
	for round := 0; round < 4; round++ {
		script = append(script, handoffJSON(t, Handoff{
			Status:    HandoffContinue,
			Summary:   "restate",
			NextSteps: steps,
		}))
	}
	sched := &scriptedScheduler{script: script}
	outcome, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "obj", 16, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalStalled {
		t.Fatalf("expected stalled at strike limit, got %+v", outcome)
	}
	if outcome.RoundsStarted != IterateGoalNoProgressStrikeLimit+1 {
		t.Fatalf("expected %d rounds before stall, got %d", IterateGoalNoProgressStrikeLimit+1, outcome.RoundsStarted)
	}
	if sched.calls != 4 {
		t.Fatalf("expected 4 rounds run, got %d", sched.calls)
	}
}

func TestIterateGoalUnstructuredOutputFusesAfterStrikes(t *testing.T) {
	sched := &scriptedScheduler{script: []string{
		"plain text answer one",
		"plain text answer two",
		"plain text answer three",
	}}
	outcome, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "obj", 16, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalStalled || outcome.RoundsStarted != IterateGoalNoProgressStrikeLimit {
		t.Fatalf("unstructured rounds must fuse at strike limit, got %+v", outcome)
	}
}

func TestIterateGoalRejectsRoundsAboveCeiling(t *testing.T) {
	sched := &scriptedScheduler{}
	_, err := RunIterateGoal(context.Background(), sched, SubagentRunOptions{}, "obj", MaxIterateGoalRounds+1, IterateGoalConfig{})
	if err == nil || !strings.Contains(err.Error(), "exceeds the deployment ceiling") {
		t.Fatalf("max_rounds above ceiling must be rejected, got %v", err)
	}
	if sched.calls != 0 {
		t.Fatalf("rejected request must not start any round, got %d", sched.calls)
	}
}

func TestIterateGoalRequiresObjective(t *testing.T) {
	if _, err := RunIterateGoal(context.Background(), &scriptedScheduler{}, SubagentRunOptions{}, "  ", 0, IterateGoalConfig{}); err == nil {
		t.Fatal("empty objective must fail")
	}
	if _, err := RunIterateGoal(context.Background(), nil, SubagentRunOptions{}, "obj", 0, IterateGoalConfig{}); err == nil {
		t.Fatal("nil runner must fail")
	}
}

func TestDefaultProgressClassifierDetectsRestatement(t *testing.T) {
	prev := Handoff{Status: HandoffContinue, Summary: "s", NextSteps: []string{"a", "b"}}
	same := Handoff{Status: HandoffContinue, Summary: "again", NextSteps: []string{"a", "b"}}
	if DefaultIterateGoalProgressClassifier(prev, same) {
		t.Fatal("identical next_steps must classify as no-progress")
	}
	progressed := Handoff{Status: HandoffContinue, Summary: "s", NextSteps: []string{"a", "c"}}
	if !DefaultIterateGoalProgressClassifier(prev, progressed) {
		t.Fatal("changed next_steps must classify as progress")
	}
	done := Handoff{Status: HandoffComplete, Summary: "s", Evidence: []string{"e"}}
	if !DefaultIterateGoalProgressClassifier(prev, done) {
		t.Fatal("terminal statuses always count as progress")
	}
}

func TestIterateGoalPrefersContractHandoff(t *testing.T) {
	// submit_report 契约路径优先：轮次结果带 HandoffOut 时直接采信（不再做
	// 文本识别），且任务包携带部署自有 round schema（ReportSchemaGranted）。
	var capturedTask SubagentTask
	runner := &capturingRoundRunner{
		run: func(_ context.Context, _ SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
			capturedTask = tasks[0]
			return []SubagentResult{{
				ID:      tasks[0].ID,
				Success: true,
				Summary: "text summary without JSON",
				HandoffOut: &Handoff{
					Status:   HandoffComplete,
					Summary:  "via contract",
					Evidence: []string{"validated at submit"},
				},
			}}, nil
		},
	}
	outcome, err := RunIterateGoal(context.Background(), runner, SubagentRunOptions{}, "obj", 4, IterateGoalConfig{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if outcome.Status != IterateGoalComplete || outcome.RoundsStarted != 1 {
		t.Fatalf("contract handoff must drive the terminal status, got %+v", outcome)
	}
	if outcome.Report == nil || outcome.Report.Summary != "via contract" {
		t.Fatalf("terminal report must come from the contract handoff, got %+v", outcome.Report)
	}
	// 任务包契约面：部署自有 schema 已授予、报告经 submit_report 交付。
	if capturedTask.ReportSchema == nil || !capturedTask.ReportSchemaGranted {
		t.Fatalf("round task must carry a granted deployment-owned report schema, got %+v", capturedTask.ReportSchema)
	}
}

// capturingRoundRunner 让测试观察 RunIterateGoal 构造的任务包。
type capturingRoundRunner struct {
	run func(context.Context, SubagentRunOptions, []SubagentTask) ([]SubagentResult, error)
}

func (r *capturingRoundRunner) RunChildren(ctx context.Context, options SubagentRunOptions, tasks []SubagentTask) ([]SubagentResult, error) {
	return r.run(ctx, options, tasks)
}

func TestIterateGoalToolDefinitionShape(t *testing.T) {
	def := IterateGoalToolDefinition()
	if def.Name != "iterate_goal" {
		t.Fatalf("tool name mismatch: %q", def.Name)
	}
	params, ok := def.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatal("parameters.properties missing")
	}
	if _, ok := params["objective"]; !ok {
		t.Fatal("objective parameter missing")
	}
	// deployment-owned：schema 不得暴露 progress classifier / handoff 校验。
	for key := range params {
		if key != "objective" && key != "max_rounds" {
			t.Fatalf("unexpected model-facing parameter %q (deployment owns the loop)", key)
		}
	}
}

func TestIterateGoalToolSurfaceDefaultsOff(t *testing.T) {
	// G4：默认关。懒初始化 scheduler 不能当启用信号（预算敏感场景的工具面
	// 不得被额外 schema 推高，见 AutoCompactionRecovery 回归）。
	parent := NewAgent(&Config{Name: "surface"}, nil)
	if parent.iterateGoalToolEnabled() {
		t.Fatal("iterate_goal must default to disabled")
	}
	// 显式开启后可暴露。
	parent.SetIterateGoalEnabled(true)
	if !parent.iterateGoalToolEnabled() {
		t.Fatal("SetIterateGoalEnabled(true) must enable the surface")
	}
	parent.SetIterateGoalEnabled(false)
	if parent.iterateGoalToolEnabled() {
		t.Fatal("SetIterateGoalEnabled(false) must disable the surface")
	}
}

func TestSpawnSubagentsSchemaCapabilityGating(t *testing.T) {
	// G7 第三层：未授予的能力位不进 schema（schema 零增量）。
	defaults := spawnSubagentsToolDefinition(true, "", DefaultCapabilities())
	items := defaults.Parameters["properties"].(map[string]interface{})["agents"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})
	if _, ok := items["context"]; ok {
		t.Fatal("context must stay out of the schema while fork_context is not granted")
	}
	if _, ok := items["report_schema"]; ok {
		t.Fatal("report_schema must stay out of the schema while output_schema is not granted")
	}
	if _, ok := items["handoff_in"]; ok {
		t.Fatal("handoff_in rides the structured-output capability and must stay out too")
	}
	// 授予后字段出现。
	granted := SubagentCapabilities{ForkContext: true, OutputSchema: true, ToolFilter: true, AgentOptions: true, Persona: true}
	grantedDef := spawnSubagentsToolDefinition(true, "", granted)
	items = grantedDef.Parameters["properties"].(map[string]interface{})["agents"].(map[string]interface{})["items"].(map[string]interface{})["properties"].(map[string]interface{})
	for _, key := range []string{"context", "report_schema", "handoff_in"} {
		if _, ok := items[key]; !ok {
			t.Fatalf("granted capability %q must appear in the schema", key)
		}
	}
}

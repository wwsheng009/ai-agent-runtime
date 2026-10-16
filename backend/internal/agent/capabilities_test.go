package agent

// capabilities_test.go — spawn 准入期能力协商测试（P1-1'）。
//
// 覆盖矩阵（对齐 v2 方案 §4 P1-1' 测试计划）：
//   - 每能力位 × 请求/不请求 × 部署开/关；
//   - fork + 路由覆盖的组合拒绝（KV Cache 保持约束）；
//   - refusal 错误消息含字段名与 next_action；
//   - 默认能力位覆盖现状全部已支持项（零行为变化基线）。

import (
	"strings"
	"testing"
)

func TestDefaultCapabilitiesCoverCurrentBehavior(t *testing.T) {
	caps := DefaultCapabilities()
	if !caps.AgentOptions {
		t.Fatal("agent_options must be granted by default: route overrides are supported today")
	}
	if !caps.ToolFilter {
		t.Fatal("tool_filter must be granted by default: tools_whitelist is supported today")
	}
	if !caps.Persona {
		t.Fatal("persona must be granted by default: agentdef persona is supported today")
	}
	if caps.ForkContext {
		t.Fatal("fork_context must stay denied until P2-1' lands")
	}
	if caps.OutputSchema {
		t.Fatal("output_schema must stay denied until the submit_report contract is wired")
	}
}

func TestNegotiatePlainTaskPasses(t *testing.T) {
	// 最普通的任务（goal only）必须零 refusal：现状行为不能被能力位打破。
	task := SubagentTask{ID: "plain", Goal: "do the thing"}
	negotiation := Negotiate(nil, task)
	if !negotiation.OK() {
		t.Fatalf("plain task must pass admission, got refusals: %+v", negotiation.Refusals)
	}
}

func TestNegotiateForkDeniedByDefault(t *testing.T) {
	task := SubagentTask{ID: "forker", Goal: "g", Context: "fork"}
	negotiation := Negotiate(nil, task)
	if negotiation.OK() {
		t.Fatal("fork request must be refused while ForkContext is not granted")
	}
	refusal := negotiation.Refusals[0]
	if refusal.Field != "fork_context" {
		t.Fatalf("expected fork_context refusal, got %q", refusal.Field)
	}
	if refusal.Code != "SUBAGENT_CAPABILITY_UNSUPPORTED" {
		t.Fatalf("expected SUBAGENT_CAPABILITY_UNSUPPORTED, got %q", refusal.Code)
	}
	if !strings.Contains(refusal.Reason, "next_action=use_fresh_context") {
		t.Fatalf("refusal reason must carry an actionable next_action, got %q", refusal.Reason)
	}
	err := negotiation.Err(task.ID)
	if err == nil || !strings.Contains(err.Error(), "forker") {
		t.Fatalf("Err must name the task, got %v", err)
	}
}

func TestNegotiateForkGranted(t *testing.T) {
	caps := DefaultCapabilities()
	caps.ForkContext = true
	plainFork := SubagentTask{ID: "forker", Goal: "g", Context: "fork"}
	if negotiation := Negotiate(&caps, plainFork); !negotiation.OK() {
		t.Fatalf("granted fork must pass, got %+v", negotiation.Refusals)
	}
	// fresh 请求不受 fork 能力位影响。
	fresh := SubagentTask{ID: "fresh", Goal: "g"}
	if negotiation := Negotiate(&caps, fresh); !negotiation.OK() {
		t.Fatalf("fresh task must pass, got %+v", negotiation.Refusals)
	}
}

func TestNegotiateForkRejectsRouteOverrides(t *testing.T) {
	caps := DefaultCapabilities()
	caps.ForkContext = true
	task := SubagentTask{
		ID:       "forker",
		Goal:     "g",
		Context:  "fork",
		Provider: "other-provider",
		Model:    "other-model",
	}
	negotiation := Negotiate(&caps, task)
	if negotiation.OK() {
		t.Fatal("fork + route overrides must be refused: fork children inherit the parent route")
	}
	found := false
	for _, refusal := range negotiation.Refusals {
		if refusal.Field == "provider_override" &&
			strings.Contains(refusal.Reason, "KV-cache") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected provider_override refusal citing KV-cache reuse, got %+v", negotiation.Refusals)
	}
}

func TestNegotiateReportSchemaDeniedByDefault(t *testing.T) {
	task := SubagentTask{
		ID:           "reporter",
		Goal:         "g",
		ReportSchema: map[string]interface{}{"type": "object"},
	}
	negotiation := Negotiate(nil, task)
	if negotiation.OK() {
		t.Fatal("report_schema request must be refused while OutputSchema is not granted")
	}
	if negotiation.Refusals[0].Field != "report_schema" {
		t.Fatalf("expected report_schema refusal, got %q", negotiation.Refusals[0].Field)
	}
}

func TestNegotiateReportSchemaGrantedBypassesAdmission(t *testing.T) {
	// 部署自有契约（ReportSchemaGranted，如 iterate_goal 的 round schema）：
	// 不是模型请求的字段，能力协商不得拒绝——契约校验仍在 submit_report 收口。
	task := SubagentTask{
		ID:                  "iterate_1",
		Goal:                "g",
		ReportSchema:        handoffReportSchema(),
		ReportSchemaGranted: true,
	}
	if negotiation := Negotiate(nil, task); !negotiation.OK() {
		t.Fatalf("granted deployment-owned schema must pass admission, got %+v", negotiation.Refusals)
	}
	if wants := SubagentRequestWants(task); wants.OutputSchema {
		t.Fatal("a granted deployment-owned schema must not request the model-facing capability")
	}
}

func TestHandoffReportSchemaShape(t *testing.T) {
	schema := handoffReportSchema()
	if schema["type"] != "object" {
		t.Fatalf("handoff schema must be object-rooted, got %v", schema["type"])
	}
	required, err := stringListValue(schema["required"])
	if err != nil {
		t.Fatalf("required must decode: %v", err)
	}
	if len(required) != 5 {
		t.Fatalf("handoff schema must require all five keys, got %v", required)
	}
	// 契约校验接受一份合法报告、拒绝缺键报告（与 submit_report 收口一致）。
	valid := map[string]interface{}{
		"status":     "continue",
		"summary":    "s",
		"evidence":   []interface{}{},
		"next_steps": []interface{}{"a"},
		"blocker":    "",
	}
	if err := validateStructuredReport(schema, valid); err != nil {
		t.Fatalf("valid handoff report must pass schema validation: %v", err)
	}
	missing := map[string]interface{}{"status": "continue", "summary": "s"}
	if err := validateStructuredReport(schema, missing); err == nil {
		t.Fatal("missing required keys must be refused")
	}
}

func TestNegotiateToolFilterToggle(t *testing.T) {
	task := SubagentTask{ID: "t", Goal: "g", ToolsWhitelist: []string{"read"}}
	// 默认 granted。
	if negotiation := Negotiate(nil, task); !negotiation.OK() {
		t.Fatalf("tool filter is supported today, got %+v", negotiation.Refusals)
	}
	// 宿主显式降级后拒绝。
	caps := DefaultCapabilities()
	caps.ToolFilter = false
	negotiation := Negotiate(&caps, task)
	if negotiation.OK() || negotiation.Refusals[0].Field != "tools_whitelist" {
		t.Fatalf("downgraded tool_filter must refuse tools_whitelist, got %+v", negotiation.Refusals)
	}
}

func TestNegotiateRouteOverrideDeniedWhenDowngraded(t *testing.T) {
	task := SubagentTask{ID: "t", Goal: "g", Provider: "p", Model: "m"}
	caps := DefaultCapabilities()
	caps.AgentOptions = false
	negotiation := Negotiate(&caps, task)
	if negotiation.OK() || negotiation.Refusals[0].Field != "provider_override" {
		t.Fatalf("downgraded agent_options must refuse route overrides, got %+v", negotiation.Refusals)
	}
}

func TestSubagentRequestWantsDetection(t *testing.T) {
	plain := SubagentTask{ID: "t", Goal: "g"}
	wants := SubagentRequestWants(plain)
	if wants.AgentOptions || wants.ToolFilter || wants.ForkContext || wants.OutputSchema {
		t.Fatalf("plain task must request nothing, got %+v", wants)
	}
	full := SubagentTask{
		ID:           "t",
		Goal:         "g",
		Provider:     "p",
		Model:        "m",
		ReasoningEffort: "high",
		ToolsWhitelist:  []string{"read"},
		Context:         "fork",
		ReportSchema:    map[string]interface{}{"type": "object"},
	}
	wants = SubagentRequestWants(full)
	if !wants.AgentOptions || !wants.ToolFilter || !wants.ForkContext || !wants.OutputSchema {
		t.Fatalf("full task must request all four bits, got %+v", wants)
	}
	// 空白 provider 不算请求。
	blank := SubagentTask{ID: "t", Goal: "g", Provider: "   "}
	if SubagentRequestWants(blank).AgentOptions {
		t.Fatal("whitespace-only provider must not count as a route override request")
	}
}

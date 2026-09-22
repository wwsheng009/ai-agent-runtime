package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/modelrouting"
)

// 本文件锁定方案 docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md
// §5.1（G1）的载荷契约：路由审计字段必须完整非空，自由文本必须有界（字符上限 +
// 整行字节预算）。这是验收 A1 的**单元级**证据；A1 的端到端取证仍需一次真实受控
// 批次运行（见方案 §10）。

func TestBuildSubagentRouteResolvedPayloadCarriesRouteAuditFields(t *testing.T) {
	decision := modelrouting.RouteDecision{
		Difficulty:          "hard",
		DifficultySource:    "explicit",
		DifficultyRationale: "跨系统一致性要求",
		Provider:            "anthropic",
		Model:               "claude-sonnet-4-5",
		ReasoningEffort:     "high",
		Source:              "role_profile",
		Warnings:            []string{"alias key collision: normal/medium"},
		FallbackUsed:        true,
		FallbackReason:      "provider unhealthy",
	}
	base := map[string]interface{}{
		"subagent_id":       "sa-1",
		"parent_session_id": "sess-parent",
		"child_session_id":  "sess-child",
		"attempt":           2,
		"max_attempts":      3,
		"goal":              "实现迁移",
	}
	payload := buildSubagentRouteResolvedPayload(base, decision)

	// A1：route_model / route_provider / route_source 必须非空且与决策一致。
	for key, want := range map[string]string{
		"route_model":       decision.Model,
		"route_provider":    decision.Provider,
		"route_source":      decision.Source,
		"difficulty":        decision.Difficulty,
		"difficulty_source": decision.DifficultySource,
	} {
		got, _ := payload[key].(string)
		if got == "" {
			t.Fatalf("payload[%q] 为空，违反 A1 的非空要求（payload=%v）", key, payload)
		}
		if got != want {
			t.Fatalf("payload[%q] = %q, want %q", key, got, want)
		}
	}
	if _, ok := payload["route_warnings"]; !ok {
		t.Fatalf("route_warnings 丢失：%v", payload)
	}
	if payload["fallback_used"] != true || payload["fallback_reason"] != "provider unhealthy" {
		t.Fatalf("fallback 审计字段丢失：%v", payload)
	}
	// base 里的归属与尝试序号必须保留（父会话反查的前提）。
	if payload["parent_session_id"] != "sess-parent" || payload["child_session_id"] != "sess-child" {
		t.Fatalf("会话归属字段被覆盖：%v", payload)
	}
	if payload["attempt"] != 2 || payload["max_attempts"] != 3 {
		t.Fatalf("attempt 字段被覆盖：%v", payload)
	}
	if _, err := json.Marshal(payload); err != nil {
		t.Fatalf("载荷不可序列化: %v", err)
	}
}

func TestBuildSubagentRouteResolvedPayloadBoundsFreeText(t *testing.T) {
	huge := strings.Repeat("迁移", 4000) // 24 KB（中文 3 字节/字符）
	base := map[string]interface{}{
		"subagent_id":       "sa-2",
		"parent_session_id": "sess-parent",
		"goal":              huge,
	}
	decision := modelrouting.RouteDecision{
		DifficultyRationale: huge,
		Provider:            "openai",
		Model:               "gpt-5.2-codex",
		Source:              "default",
	}
	payload := buildSubagentRouteResolvedPayload(base, decision)

	goal, _ := payload["goal"].(string)
	if len(goal) > subagentRouteAuditTextLimit {
		t.Fatalf("goal 未按字符上限截断: %d 字节 > %d", len(goal), subagentRouteAuditTextLimit)
	}
	if !strings.Contains(goal, "(truncated)") {
		t.Fatalf("超长 goal 应带截断标记: %q", goal)
	}
	if rationale, _ := payload["difficulty_rationale"].(string); len(rationale) > subagentRouteAuditTextLimit {
		t.Fatalf("difficulty_rationale 未按字符上限截断: %d 字节", len(rationale))
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("载荷不可序列化: %v", err)
	}
	if len(encoded) > subagentRouteAuditPayloadByteLimit {
		t.Fatalf("整行载荷超出字节预算: %d > %d", len(encoded), subagentRouteAuditPayloadByteLimit)
	}
	// 审计字段永不因字节收敛而被裁。
	if payload["route_model"] != "gpt-5.2-codex" || payload["route_provider"] != "openai" {
		t.Fatalf("审计字段被字节收敛裁掉：%v", payload)
	}
	if payload["parent_session_id"] != "sess-parent" {
		t.Fatalf("会话归属字段被字节收敛裁掉：%v", payload)
	}
}

func TestBoundSubagentRouteAuditPayloadShrinksOnlyFreeText(t *testing.T) {
	const limit = 512
	payload := map[string]interface{}{
		"parent_session_id":    "sess-parent",
		"child_session_id":     "sess-child",
		"route_model":          "claude-opus-4-1",
		"goal":                 strings.Repeat("跨系统", 2000),
		"difficulty_rationale": strings.Repeat("权限", 2000),
	}
	bounded := boundSubagentRouteAuditPayload(payload, limit)

	encoded, err := json.Marshal(bounded)
	if err != nil {
		t.Fatalf("载荷不可序列化: %v", err)
	}
	if len(encoded) > limit {
		t.Fatalf("未收敛到字节预算内: %d > %d", len(encoded), limit)
	}
	if bounded["route_model"] != "claude-opus-4-1" {
		t.Fatalf("审计字段被裁: %v", bounded["route_model"])
	}
	if bounded["parent_session_id"] != "sess-parent" || bounded["child_session_id"] != "sess-child" {
		t.Fatalf("会话归属字段被裁: %v", bounded)
	}
}

func TestBoundSubagentRouteAuditPayloadNilSafe(t *testing.T) {
	if got := boundSubagentRouteAuditPayload(nil, 512); got != nil {
		t.Fatalf("nil 载荷应原样返回 nil，得到 %v", got)
	}
}

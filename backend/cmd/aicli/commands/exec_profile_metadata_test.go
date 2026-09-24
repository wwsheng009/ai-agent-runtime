package commands

import (
	"encoding/json"
	"strings"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeprofileinput "github.com/wwsheng009/ai-agent-runtime/internal/profileinput"
)

// TestExecProfileMetadataOmittedWithoutProfile：未启用 profile 时元数据为 nil，
// JSON 中不存在 profile 字段（无 profile 输出形状零变化，NFR-1）。
func TestExecProfileMetadataOmittedWithoutProfile(t *testing.T) {
	if meta := execProfileMetadata(nil); meta != nil {
		t.Fatalf("nil session must yield nil metadata, got %+v", meta)
	}
	if meta := execProfileMetadata(&ChatSession{}); meta != nil {
		t.Fatalf("session without profile must yield nil metadata, got %+v", meta)
	}

	raw, err := json.Marshal(ExecFinalResult{Status: "completed", Message: "ok"})
	if err != nil {
		t.Fatalf("marshal final result: %v", err)
	}
	if strings.Contains(string(raw), "\"profile\"") {
		t.Fatalf("no-profile final result must not carry a profile field: %s", raw)
	}
}

// TestExecProfileMetadataProjectsSurface：profile 生效时投影 ref/name/agent 与
// 裁剪计数，并固定 JSON 契约字段名（D9/FR-10，供 CI 断言）。
func TestExecProfileMetadataProjectsSurface(t *testing.T) {
	session := &ChatSession{
		ProfileReference: "coding",
		ProfileName:      "coding",
		ProfileAgent:     "explore",
		ToolPolicy:       runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep", "glob"}, false),
	}
	meta := execProfileMetadata(session)
	if meta == nil {
		t.Fatal("expected metadata for active profile")
	}
	if meta.Reference != "coding" || meta.Name != "coding" || meta.Agent != "explore" {
		t.Fatalf("unexpected identity fields: %+v", meta)
	}
	if meta.ToolCount != 3 {
		t.Fatalf("expected tool_count 3 (allowlist 生效条目), got %d", meta.ToolCount)
	}
	if meta.SkillCount != 0 {
		t.Fatalf("expected skill_count 0 without skills binding, got %d", meta.SkillCount)
	}

	raw, err := json.Marshal(ExecFinalResult{Status: "completed", Profile: meta})
	if err != nil {
		t.Fatalf("marshal final result: %v", err)
	}
	for _, want := range []string{`"profile"`, `"ref":"coding"`, `"agent":"explore"`, `"tool_count":3`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("expected %s in %s", want, raw)
		}
	}
}

// TestExecProfileMetadataToolCountUnrestricted：未声明 allowlist（不限制）时
// tool_count 为 0——语义是"未收窄"，而不是"零工具"。
func TestExecProfileMetadataToolCountUnrestricted(t *testing.T) {
	session := &ChatSession{
		ProfileName: "coding",
		ToolPolicy:  runtimepolicy.NewToolExecutionPolicy(nil, false),
	}
	meta := execProfileMetadata(session)
	if meta == nil || meta.ToolCount != 0 {
		t.Fatalf("expected unrestricted tool_count 0, got %+v", meta)
	}
}

// TestChatProfileSurfaceRowsEstimatesPromptTokens：启动摘要行包含选择计数，且
// prompt token 走 internal/profile 的估算单点（8 B → 2 tokens），带"估算"标注。
func TestChatProfileSurfaceRowsEstimatesPromptTokens(t *testing.T) {
	session := &ChatSession{
		ProfileName:       "coding",
		ProfileAgent:      "explore",
		ProfilePromptMode: "replace",
		SystemPromptText:  "12345678",
		ToolPolicy:        runtimepolicy.NewToolExecutionPolicy([]string{"view", "grep"}, true),
		ProfileSkillSelection: runtimeprofileinput.ResolvedSkillSelection{
			Allowlist: []string{"aicli"},
			Denylist:  []string{"skill-creator"},
		},
		ProfileMCPSelection: runtimeprofileinput.ResolvedMCPSelection{
			UseServers: []string{"docs"},
		},
	}
	rows := chatProfileSurfaceRows(session)
	joined := make(map[string]string, len(rows))
	for _, row := range rows {
		joined[row.label] = row.value
	}
	if got := joined["Profile Tools:"]; !strings.Contains(got, "allowlist 2 / denylist 0") || !strings.Contains(got, "read_only") {
		t.Fatalf("unexpected tools row: %q", got)
	}
	if got := joined["Profile Skills:"]; got != "allow 1 / deny 1" {
		t.Fatalf("unexpected skills row: %q", got)
	}
	if got := joined["Profile MCP:"]; got != "use 1 / exclude 0" {
		t.Fatalf("unexpected mcp row: %q", got)
	}
	if got := joined["Profile Prompt:"]; got != "replace · 8 B ≈ 2 tokens（估算）" {
		t.Fatalf("unexpected prompt row: %q", got)
	}
	if rows := chatProfileSurfaceRows(nil); rows != nil {
		t.Fatalf("nil session must yield no rows, got %+v", rows)
	}
}

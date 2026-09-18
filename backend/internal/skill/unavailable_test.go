package skill

import (
	"encoding/json"
	"testing"
)

// TestRegisterSkills_RecordsUnavailableSkillsWithMissingTools 验证 SK-4：
// 工具缺失的技能保持既有的软跳过（不注册、不阻断），同时把完整的缺失依赖
// 写入 registry 的 unavailable 记录，而不是只留一行日志。
func TestRegisterSkills_RecordsUnavailableSkillsWithMissingTools(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{Name: "fetch_page", Description: "fetch a url", Tools: []string{"fetch"}},
		{
			Name:        "run_shell",
			Description: "run a command",
			Tools:       []string{"bash", "view", "bash"},
			Source:      &SkillSource{Path: "C:/skills/shell/skill.yaml", Layer: SkillSourceLayerExternal},
		},
	}

	if err := loader.registerSkills(skills, registry); err != nil {
		t.Fatalf("registerSkills should soft-skip missing tools, got error: %v", err)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 skill registered (existing semantics unchanged), got %d", got)
	}
	if _, ok := registry.Get("run_shell"); ok {
		t.Fatalf("skill with missing tools must stay out of the executable registry")
	}

	items := registry.UnavailableSkills()
	if len(items) != 1 {
		t.Fatalf("expected 1 unavailable record, got %d: %+v", len(items), items)
	}
	item := items[0]
	if item.Name != "run_shell" {
		t.Fatalf("expected run_shell unavailable, got %q", item.Name)
	}
	if item.Path != "C:/skills/shell/skill.yaml" {
		t.Fatalf("expected source path preserved, got %q", item.Path)
	}
	if item.Scope != SkillSourceLayerExternal {
		t.Fatalf("expected scope external, got %q", item.Scope)
	}
	if item.Reason != UnavailableReasonMissingTools {
		t.Fatalf("expected reason %q, got %q", UnavailableReasonMissingTools, item.Reason)
	}
	if len(item.MissingTools) != 2 || item.MissingTools[0] != "bash" || item.MissingTools[1] != "view" {
		t.Fatalf("expected deduped/sorted missing tools [bash view], got %v", item.MissingTools)
	}
	if item.Message == "" {
		t.Fatalf("expected human-readable recovery message to be populated")
	}

	if got, ok := registry.LookupUnavailable("RUN_SHELL"); !ok || got.Name != "run_shell" {
		t.Fatalf("LookupUnavailable must be case-insensitive, got %+v ok=%v", got, ok)
	}
	if _, ok := registry.LookupUnavailable("fetch_page"); ok {
		t.Fatalf("registered skill must not appear unavailable")
	}
	if _, ok := registry.LookupUnavailable("no-such-skill"); ok {
		t.Fatalf("unknown skill must not match unavailable records")
	}
}

// TestRegisterSummaryStubs_RecordsUnavailableSkills 验证 discovery 轻量 stub
// 路径（exec/chat 启动使用）同样登记 unavailable 记录。
func TestRegisterSummaryStubs_RecordsUnavailableSkills(t *testing.T) {
	mcp := newFakeSkillsMCPManager("openai_image_generate")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	summaries := []*SkillSummary{
		{Name: "imagegen", Description: "generate an image", Tools: []string{"openai_image_generate"}},
		{
			Name:        "fetch_url",
			Description: "fetch a url",
			Tools:       []string{"fetch"},
			Source:      &SkillSource{Path: "C:/skills/fetch/skill.yaml", Layer: SkillSourceLayerSystem},
		},
	}

	if err := loader.registerSummaryStubs(summaries, registry); err != nil {
		t.Fatalf("registerSummaryStubs should soft-skip missing tools, got error: %v", err)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 stub registered, got %d", got)
	}

	item, ok := registry.LookupUnavailable("fetch_url")
	if !ok {
		t.Fatalf("expected fetch_url recorded as unavailable")
	}
	if len(item.MissingTools) != 1 || item.MissingTools[0] != "fetch" {
		t.Fatalf("expected missing tools [fetch], got %v", item.MissingTools)
	}
	if item.Scope != SkillSourceLayerSystem {
		t.Fatalf("expected scope system from summary source, got %q", item.Scope)
	}
}

// TestRegisterSkills_DocumentModeNotRecordedUnavailable 验证 SK-7 文档模式
// 豁免不受 SK-4 影响：即使依赖工具缺失，文档模式技能也正常注册，且不会
// 出现在 unavailable 分组中。
func TestRegisterSkills_DocumentModeNotRecordedUnavailable(t *testing.T) {
	mcp := newFakeSkillsMCPManager()
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{
			Name:          "doc-skill",
			Description:   "document mode skill",
			ExecutionMode: ExecutionModeDocument,
			Tools:         []string{"bash"},
		},
	}

	if err := loader.registerSkills(skills, registry); err != nil {
		t.Fatalf("registerSkills should not fail, got error: %v", err)
	}
	if _, ok := registry.Get("doc-skill"); !ok {
		t.Fatalf("document-mode skill must remain registered")
	}
	if items := registry.UnavailableSkills(); len(items) != 0 {
		t.Fatalf("document-mode skill must not be recorded unavailable, got %+v", items)
	}
}

// TestRegisterSkills_NonToolValidationErrorNotRecordedAsUnavailable 验证非
// "工具缺失"类校验错误维持原语义（上报 error），且不污染 unavailable 分组。
func TestRegisterSkills_NonToolValidationErrorNotRecordedAsUnavailable(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{Name: "", Description: "missing name", Tools: []string{"fetch"}},
	}

	if err := loader.registerSkills(skills, registry); err == nil {
		t.Fatalf("expected validation error for skill without name, got nil")
	}
	if items := registry.UnavailableSkills(); len(items) != 0 {
		t.Fatalf("non-tool validation errors must not be recorded as unavailable, got %+v", items)
	}
}

// TestRegistry_RegisterSuccessClearsUnavailableRecord 验证工具恢复后重新
// 注册成功会清理同名 unavailable 记录（技能回到 available）。
func TestRegistry_RegisterSuccessClearsUnavailableRecord(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	if err := loader.registerSkills([]*Skill{
		{Name: "run_shell", Description: "run a command", Tools: []string{"bash"}},
	}, registry); err != nil {
		t.Fatalf("registerSkills should soft-skip, got error: %v", err)
	}
	if _, ok := registry.LookupUnavailable("run_shell"); !ok {
		t.Fatalf("expected run_shell recorded unavailable before recovery")
	}

	if err := loader.registerSkills([]*Skill{
		{Name: "run_shell", Description: "run a command", Tools: []string{"fetch"}},
	}, registry); err != nil {
		t.Fatalf("re-register after tool recovery should succeed, got error: %v", err)
	}
	if _, ok := registry.LookupUnavailable("run_shell"); ok {
		t.Fatalf("successful registration must clear the stale unavailable record")
	}
}

// TestRegistry_ClearResetsUnavailableRecords 验证 Clear（热重载/手动重载）
// 同时清空 unavailable 记录，避免重载后残留过期诊断。
func TestRegistry_ClearResetsUnavailableRecords(t *testing.T) {
	registry := NewRegistry(nil)
	registry.RecordUnavailable(&UnavailableSkill{
		Name:         "needs_tool",
		Reason:       UnavailableReasonMissingTools,
		MissingTools: []string{"bash"},
	})
	if registry.UnavailableCount() != 1 {
		t.Fatalf("expected 1 unavailable record before clear")
	}

	registry.Clear()
	if registry.UnavailableCount() != 0 {
		t.Fatalf("expected unavailable records cleared")
	}
}

// TestUnavailableSkill_JSONContract 验证 unavailable 的 JSON 固定字段契约。
func TestUnavailableSkill_JSONContract(t *testing.T) {
	registry := NewRegistry(nil)
	registry.RecordUnavailable(&UnavailableSkill{
		Name:         "run_shell",
		Path:         "C:/skills/shell/SKILL.md",
		Scope:        SkillSourceLayerExternal,
		MissingTools: []string{"bash"},
		Reason:       UnavailableReasonMissingTools,
	})

	item, ok := registry.LookupUnavailable("run_shell")
	if !ok {
		t.Fatalf("expected unavailable record")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal unavailable record: %v", err)
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal unavailable record: %v", err)
	}
	for _, key := range []string{"name", "path", "scope", "missing_tools", "reason", "message"} {
		if _, exists := decoded[key]; !exists {
			t.Fatalf("expected JSON field %q in %s", key, string(payload))
		}
	}
}

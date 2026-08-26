package skill

import (
	"fmt"
	"testing"
)

// fakeSkillsMCPManager 只暴露给定工具集的 MCP surface，用于模拟
// tools 被禁用/未暴露时的注册场景。
type fakeSkillsMCPManager struct {
	tools map[string]ToolInfo
}

func (f *fakeSkillsMCPManager) FindTool(toolName string) (ToolInfo, error) {
	if t, ok := f.tools[toolName]; ok {
		return t, nil
	}
	return ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

func (f *fakeSkillsMCPManager) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeSkillsMCPManager) ListTools() []ToolInfo {
	out := make([]ToolInfo, 0, len(f.tools))
	for _, t := range f.tools {
		out = append(out, t)
	}
	return out
}

func newFakeSkillsMCPManager(toolNames ...string) *fakeSkillsMCPManager {
	tools := make(map[string]ToolInfo, len(toolNames))
	for _, name := range toolNames {
		tools[name] = ToolInfo{Name: name, Description: "tool " + name}
	}
	return &fakeSkillsMCPManager{tools: tools}
}

// TestRegisterSkills_SkipsSkillsWithMissingTools 验证：部分 skill 依赖的工具
// 未在当前 surface 注册时，仅跳过这些 skill，不阻断整体注册。
func TestRegisterSkills_SkipsSkillsWithMissingTools(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{Name: "fetch_page", Description: "fetch a url", Tools: []string{"fetch"}},
		{Name: "run_shell", Description: "run a command", Tools: []string{"bash"}},
		{Name: "read_file", Description: "read a file", Tools: []string{"view"}},
	}

	if err := loader.registerSkills(skills, registry); err != nil {
		t.Fatalf("registerSkills should soft-skip missing tools, got error: %v", err)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 skill registered, got %d", got)
	}
	if _, ok := registry.Get("fetch_page"); !ok {
		t.Fatalf("skill with available tool should be registered")
	}
	if _, ok := registry.Get("run_shell"); ok {
		t.Fatalf("skill with missing tool should be skipped")
	}
}

// TestRegisterSkills_AllToolsMissingStillSucceeds 验证：所有 skill 的工具都
// 缺失时（例如 exec 默认 tools 禁用），整体注册仍然成功且不报错。
func TestRegisterSkills_AllToolsMissingStillSucceeds(t *testing.T) {
	mcp := newFakeSkillsMCPManager()
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{Name: "run_shell", Description: "run a command", Tools: []string{"bash"}},
		{Name: "read_file", Description: "read a file", Tools: []string{"view"}},
	}

	if err := loader.registerSkills(skills, registry); err != nil {
		t.Fatalf("registerSkills should succeed when all tools are missing, got error: %v", err)
	}
	if got := registry.Count(); got != 0 {
		t.Fatalf("expected 0 skills registered, got %d", got)
	}
}

// TestRegisterSkills_RealValidationErrorStillReported 验证：非"工具缺失"
// 的真实校验错误（如缺少名称/描述）仍然上报，不因软失败逻辑被吞掉。
func TestRegisterSkills_RealValidationErrorStillReported(t *testing.T) {
	mcp := newFakeSkillsMCPManager("fetch")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	skills := []*Skill{
		{Name: "", Description: "missing name", Tools: []string{"fetch"}},
	}

	if err := loader.registerSkills(skills, registry); err == nil {
		t.Fatalf("expected validation error for skill without name, got nil")
	}
}

// TestRegisterSummaryStubs_SkipsStubsWithMissingTools 验证：轻量 stub 注册
// （DiscoverAll 路径，exec/chat 启动时使用）同样软跳过缺失工具的 skill。
func TestRegisterSummaryStubs_SkipsStubsWithMissingTools(t *testing.T) {
	mcp := newFakeSkillsMCPManager("openai_image_generate")
	registry := NewRegistry(mcp)
	loader := NewLoader(mcp)

	summaries := []*SkillSummary{
		{Name: "imagegen", Description: "generate an image", Tools: []string{"openai_image_generate"}},
		{Name: "fetch_url", Description: "fetch a url", Tools: []string{"fetch"}},
	}

	if err := loader.registerSummaryStubs(summaries, registry); err != nil {
		t.Fatalf("registerSummaryStubs should soft-skip missing tools, got error: %v", err)
	}
	if got := registry.Count(); got != 1 {
		t.Fatalf("expected 1 stub registered, got %d", got)
	}
	if _, ok := registry.Get("imagegen"); !ok {
		t.Fatalf("stub with available tool should be registered")
	}
	if _, ok := registry.Get("fetch_url"); ok {
		t.Fatalf("stub with missing tool should be skipped")
	}
}
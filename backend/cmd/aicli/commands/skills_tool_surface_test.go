package commands

import (
	"os"
	"path/filepath"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimecfg "github.com/wwsheng009/ai-agent-runtime/internal/config"
	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

// stubSkillToolSurface 是一张只认识给定工具名的 skill 工具面。
type stubSkillToolSurface struct {
	names map[string]struct{}
}

func (s *stubSkillToolSurface) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if _, ok := s.names[toolName]; !ok {
		return runtimeskill.ToolInfo{}, errors.New(errors.ErrToolNotRegistered, "tool not found: "+toolName)
	}
	return runtimeskill.ToolInfo{Name: toolName}, nil
}

func (s *stubSkillToolSurface) CallTool(interface{}, string, string, map[string]interface{}) (interface{}, error) {
	return nil, nil
}

func (s *stubSkillToolSurface) ListTools() []runtimeskill.ToolInfo {
	out := make([]runtimeskill.ToolInfo, 0, len(s.names))
	for name := range s.names {
		out = append(out, runtimeskill.ToolInfo{Name: name})
	}
	return out
}

func TestMergedSkillToolSurface_UnionsBothSurfaces(t *testing.T) {
	merged := &mergedSkillToolSurface{
		primary:   &stubSkillToolSurface{names: map[string]struct{}{"bash": {}, "view": {}}},
		secondary: &stubSkillToolSurface{names: map[string]struct{}{"mcp_only": {}, "view": {}}},
	}

	// builtin 工具只在主面，进程级 MCP 工具只在副面：两者都必须找得到。
	for _, name := range []string{"bash", "view", "mcp_only"} {
		if _, err := merged.FindTool(name); err != nil {
			t.Fatalf("FindTool(%q) must resolve via the merged surface: %v", name, err)
		}
	}
	if _, err := merged.FindTool("nope"); err == nil {
		t.Fatal("FindTool must fail for a tool absent from both surfaces")
	}

	// ListTools 去重合并：3 个唯一工具，不是 4 条重复项。
	tools := merged.ListTools()
	if len(tools) != 3 {
		t.Fatalf("expected 3 unique tools after merge, got %d (%v)", len(tools), tools)
	}
}

// TestInitSkillFunctions_KeepsBuiltinToolSkillWhenProcessMCPPresent 是本次修复的
// 回归守卫。
//
// 缺陷：initSkillFunctions 在「进程级 MCPManagerInstance 非空」时把 skill 工具面
// 换成纯 MCP 面。而 skill 的 tools 依赖校验（loader.CheckSkill / registry.validate）
// 一旦 FindTool 不到工具，就把该 skill 当 ErrToolNotRegistered **静默跳过**。进程级
// MCP 面里一个 builtin 工具都没有，于是所有声明 builtin 工具依赖的 skill 在开了
// 本地 MCP 的会话里被整批丢弃——表现是「这些 skill 不见了」，且与 skill 本身无关。
//
// 这里用进程级 manager（零 MCP 工具）精确复现该条件，并断言：
//   - 依赖 builtin 工具的 skill 必须仍然注册；
//   - 依赖两面都没有的外部工具的 skill 仍按既有语义跳过（不回归保护过宽）。
func TestInitSkillFunctions_KeepsBuiltinToolSkillWhenProcessMCPPresent(t *testing.T) {
	previous := MCPManagerInstance
	MCPManagerInstance = mcpmanager.NewManager() // 进程级 MCP 存在，但没有连上任何 server
	t.Cleanup(func() { MCPManagerInstance = previous })

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	skillDir := t.TempDir()
	writeSkill := func(dir, name string, tools string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(skillDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		manifest := "name: " + name + "\ndescription: test skill " + name + "\nversion: 1.0.0\ntriggers:\n  - type: keyword\n    values: [\"" + name + "\"]\n    weight: 1\n"
		if tools != "" {
			manifest += "tools:\n  - " + tools + "\n"
		}
		if err := os.WriteFile(filepath.Join(skillDir, dir, "skill.yaml"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeSkill("needs-bash", "needs_bash", "bash")
	writeSkill("needs-unknown", "needs_unknown", "definitely_not_a_tool")

	session := &ChatSession{}
	toolManager := runtimetools.NewDefaultManagerWithRuntimeConfig(nil, runtimecfg.DefaultRuntimeConfig())
	binding, err := initSkillFunctions(&config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:  true,
			SkillDir: skillDir,
		},
	}, session, toolManager, nil, 0, "")
	if err != nil {
		t.Fatalf("initSkillFunctions failed: %v", err)
	}
	if binding == nil {
		t.Fatal("expected skill binding")
	}
	defer func() { _ = binding.Close() }()

	if _, ok := binding.skillFunctions["skill__needs_bash"]; !ok {
		t.Fatalf("builtin-tool skill must survive a process-level MCP surface; registered=%v", binding.skillFunctions)
	}
	if _, ok := binding.skillFunctions["skill__needs_unknown"]; ok {
		t.Fatal("skill depending on a tool absent from both surfaces must stay skipped")
	}
}

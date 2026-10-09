package commands

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

func writeWorkspaceSkillForProbe(t *testing.T, workspace, name string) {
	t.Helper()
	dir := filepath.Join(workspace, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir skill dir: %v", err)
	}
	body := "---\nname: " + name + "\ndescription: probe skill " + name + "\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatalf("write SKILL.md: %v", err)
	}
}

// TestProbeSkillLoadGatesWithoutConfiguredSkillDir 复现生产条件：用户配置只写了
// skills_runtime.config_file，没有 skill_dir；技能只存在于工作区 .agents/skills。
// 这里不设任何断言，只把每道闸的真实取值打出来——目的是把 /skills 报 total=0
// 定位到具体是哪一道闸关着，而不是继续在代码里推演。
func TestProbeSkillLoadGatesWithoutConfiguredSkillDir(t *testing.T) {
	workspace := t.TempDir()
	writeWorkspaceSkillForProbe(t, workspace, "alpha")
	writeWorkspaceSkillForProbe(t, workspace, "beta")
	chdirTest(t, workspace)

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, ConfigFile: "configs/runtime.yaml"},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config:           cfg,
	}

	dirs := resolveChatSkillDirs(cfg, session, nil)
	t.Logf("GATE 1 resolveChatSkillDirs -> %v (len=%d)", dirs, len(dirs))

	effective := effectiveChatSkillConfig(cfg, session)
	enabled := false
	hasSection := effective != nil && effective.SkillsRuntime != nil
	if hasSection {
		enabled = effective.SkillsRuntime.Enabled
	}
	t.Logf("GATE 2 effectiveChatSkillConfig: hasSection=%v enabled=%v", hasSection, enabled)
	t.Logf("GATE 3 session.NoSkills=%v session.DisableTools=%v", session.NoSkills, session.DisableTools)

	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	t.Logf("GATE 4 initSkillFunctions -> binding=%v err=%v", binding != nil, err)
	if binding != nil {
		t.Logf("GATE 5 binding.roots=%v skillFunctions=%d", binding.roots, len(binding.skillFunctions))
		_ = binding.Close()
	}
}

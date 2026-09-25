package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/capability"
)

func TestParseSkillToggleQuery(t *testing.T) {
	cases := []struct {
		query  string
		enable bool
		name   string
		ok     bool
	}{
		{"disable demo", false, "demo", true},
		{"off demo", false, "demo", true},
		{"enable demo", true, "demo", true},
		{"on demo", true, "demo", true},
		{"DISABLE Demo-Skill", false, "Demo-Skill", true},
		{"disable", false, "", false},
		{"", false, "", false},
		{"list", false, "", false},
		{"select", false, "", false},
		{"image", false, "", false},
	}
	for _, item := range cases {
		enable, name, ok := parseSkillToggleQuery(item.query)
		if ok != item.ok || enable != item.enable || name != item.name {
			t.Fatalf("parseSkillToggleQuery(%q) = (%v, %q, %v), want (%v, %q, %v)",
				item.query, enable, name, ok, item.enable, item.name, item.ok)
		}
	}
}

func writeToggleTestSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, name)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	document := "name: " + name + "\ndescription: toggle test skill\nversion: 1.0.0\ntriggers:\n  - type: keyword\n    values: [\"" + name + "\"]\n    weight: 1\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "skill.yaml"), []byte(document), 0o644))
}

func registryHasSkillFunction(session *ChatSession, functionName string) bool {
	if session == nil || session.FunctionRegistry == nil {
		return false
	}
	_, ok := session.FunctionRegistry.Get(functionName)
	return ok
}

func catalogSkillNames(session *ChatSession) []string {
	catalog := ensureFunctionCatalog(session)
	if catalog == nil {
		return nil
	}
	report := buildFunctionCatalogReport(catalog)
	if report == nil {
		return nil
	}
	names := make([]string, 0, len(report.Skills))
	for _, item := range report.Skills {
		if item.Descriptor != nil {
			names = append(names, item.Descriptor.Name)
		}
	}
	return names
}

func TestRunSkillToggleCommand_DisableAndEnableRefreshRuntime(t *testing.T) {
	tempDir := t.TempDir()
	chdirTest(t, tempDir)
	writeToggleTestSkill(t, tempDir, "alpha_skill")
	writeToggleTestSkill(t, tempDir, "beta_skill")

	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("skills_runtime:\n  enabled: true\n"), 0o644))

	cfg := &config.Config{
		ConfigFilePath: configPath,
		SkillsRuntime:  &config.SkillsRuntimeConfig{Enabled: true, SkillDir: tempDir},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config:           cfg,
	}
	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, binding)
	defer func() { _ = binding.Close() }()

	require.True(t, registryHasSkillFunction(session, "skill__alpha_skill"))
	require.True(t, registryHasSkillFunction(session, "skill__beta_skill"))

	// 停用：函数面立即撤销（不是"只挡新注册"的假开关）+ 配置落盘。
	message, err := runSkillToggleCommand(session, false, "Alpha_Skill")
	require.NoError(t, err)
	assert.Contains(t, message, "已停用")
	assert.False(t, registryHasSkillFunction(session, "skill__alpha_skill"))
	assert.True(t, registryHasSkillFunction(session, "skill__beta_skill"))
	assert.NotContains(t, catalogSkillNames(session), "alpha_skill")
	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.Contains(t, string(raw), "disabled_skills")
	assert.Equal(t, []string{"alpha_skill"}, cfg.SkillsRuntime.DisabledSkillNames())

	// 解禁：注册与函数面恢复，配置键被清理。
	message, err = runSkillToggleCommand(session, true, "alpha_skill")
	require.NoError(t, err)
	assert.Contains(t, message, "已启用")
	assert.True(t, registryHasSkillFunction(session, "skill__alpha_skill"))
	assert.Contains(t, catalogSkillNames(session), "alpha_skill")
	raw, err = os.ReadFile(configPath)
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "disabled_skills")
	assert.Empty(t, cfg.SkillsRuntime.DisabledSkillNames())

	// 未知 skill / 未停用的 skill：给出提示而不是静默改配置。
	message, err = runSkillToggleCommand(session, false, "missing_skill")
	require.NoError(t, err)
	assert.Contains(t, message, "未找到")
	message, err = runSkillToggleCommand(session, true, "beta_skill")
	require.NoError(t, err)
	assert.Contains(t, message, "未停用")
	assert.True(t, registryHasSkillFunction(session, "skill__beta_skill"))
}

func TestExecuteStructuredSkillsMenuCommand_RoutesToggle(t *testing.T) {
	tempDir := t.TempDir()
	chdirTest(t, tempDir)
	writeToggleTestSkill(t, tempDir, "alpha_skill")

	configPath := filepath.Join(tempDir, "config.yaml")
	require.NoError(t, os.WriteFile(configPath, []byte("skills_runtime:\n  enabled: true\n"), 0o644))

	cfg := &config.Config{
		ConfigFilePath: configPath,
		SkillsRuntime:  &config.SkillsRuntimeConfig{Enabled: true, SkillDir: tempDir},
	}
	session := &ChatSession{
		ProviderName:     "nvidia",
		Model:            "z-ai/glm4.7",
		FunctionRegistry: functions.NewFunctionRegistry(),
		Config:           cfg,
	}
	binding, err := initSkillFunctions(cfg, session, nil, nil, 0, "")
	require.NoError(t, err)
	require.NotNil(t, binding)
	defer func() { _ = binding.Close() }()

	result, handled := executeStructuredSkillsMenuCommand(session, "/skills disable alpha_skill")
	require.True(t, handled)
	require.NotEmpty(t, result.Blocks)
	assert.Contains(t, renderDocumentText(result.Blocks[0].Document), "已停用")
	assert.False(t, registryHasSkillFunction(session, "skill__alpha_skill"))

	raw, err := os.ReadFile(configPath)
	require.NoError(t, err)
	assert.True(t, strings.Contains(string(raw), "alpha_skill"))
}

func TestParseSkillCatalogToggleInput(t *testing.T) {
	cases := []struct {
		input  string
		target string
		ok     bool
	}{
		{"x 1", "1", true},
		{"X 2", "2", true},
		{"toggle beta_skill", "beta_skill", true},
		{"TOGGLE 1", "1", true},
		{"x", "", false},
		{"", "", false},
		{"1", "", false},
		{"beta_skill", "", false},
		{"disable beta_skill", "", false},
	}
	for _, item := range cases {
		target, ok := parseSkillCatalogToggleInput(item.input)
		if ok != item.ok || target != item.target {
			t.Fatalf("parseSkillCatalogToggleInput(%q) = (%q, %v), want (%q, %v)", item.input, target, ok, item.target, item.ok)
		}
	}
}

// TestBuildSkillPickerCatalogEntriesIncludesDisabledRows 回归"停用后必须在同一
// 入口启得回来"：选择器要带上已停用行，且与可用行去重、可重复调用。
func TestBuildSkillPickerCatalogEntriesIncludesDisabledRows(t *testing.T) {
	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{
			Enabled:        true,
			DisabledSkills: []string{"Gamma_Skill", "beta_skill"},
		},
	}
	session := &ChatSession{Config: cfg}
	enabled := []aicliFunctionDescriptorReport{
		{FunctionName: "skill__beta_skill", Descriptor: &capability.Descriptor{Name: "beta_skill"}},
	}

	entries := buildSkillPickerCatalogEntries(session, enabled)
	require.Len(t, entries, 2, "beta_skill 已在可用列表里，不应重复追加")
	assert.False(t, entries[0].Disabled)
	assert.Equal(t, "beta_skill", skillCatalogEntryLabel(entries[0]))
	assert.True(t, entries[1].Disabled, "停用名单里的 skill 必须以已停用行出现")
	assert.Equal(t, "Gamma_Skill", skillCatalogEntryLabel(entries[1]))
	assert.Empty(t, strings.TrimSpace(entries[1].FunctionName), "停用行没有可调用函数，不能被当作 draft 目标")

	// 幂等：选择器每轮刷新都会重新合并，不能越合并越多。
	again := buildSkillPickerCatalogEntries(session, entries)
	require.Len(t, again, len(entries))

	// 没有停用名单时原样返回（不改变未配置行为）。
	plain := buildSkillPickerCatalogEntries(&ChatSession{Config: &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true},
	}}, enabled)
	require.Len(t, plain, 1)
	assert.False(t, plain[0].Disabled)
}

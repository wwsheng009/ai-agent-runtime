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
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestParseSkillToolDeclaration(t *testing.T) {
	cases := []struct {
		raw       string
		name      string
		qualifier string
	}{
		{"Bash(git:*)", "Bash", "git:*"},
		{"Read", "Read", ""},
		{"  read_file  ", "read_file", ""},
		{"Bash(git * --version)", "Bash", "git * --version"},
		{"", "", ""},
		{"   ", "", ""},
	}
	for _, item := range cases {
		declaration := parseSkillToolDeclaration(item.raw)
		assert.Equal(t, item.name, declaration.Name, "name for %q", item.raw)
		assert.Equal(t, item.qualifier, declaration.Qualifier, "qualifier for %q", item.raw)
	}
}

// schemasForNames 构造与 catalogSchemaIndex 同形状的索引（键为小写名）。
func schemasForNames(names ...string) map[string]map[string]interface{} {
	schemas := make(map[string]map[string]interface{}, len(names))
	for _, name := range names {
		schemas[toolDeclarationLookupKey(name)] = map[string]interface{}{"name": name}
	}
	return schemas
}

func TestApplySkillToolRestriction(t *testing.T) {
	skill := &runtimeskill.Skill{
		Name: "restricted",
		Codex: &runtimeskill.CodexSkillMetadata{
			Name:            "restricted",
			AllowedTools:    []string{"read_file", "Bash(git:*)", "missing_tool"},
			DisallowedTools: []string{"write"},
			Model:           "claude-sonnet",
			Effort:          "high",
		},
	}
	pin := &skillTurnPin{PinnedFunctions: []string{"skill__restricted"}}
	applySkillToolRestriction(pin, skill, schemasForNames("read_file", "bash", "write"))

	assert.Equal(t, []string{"read_file", "bash"}, pin.AllowedFunctions,
		"带括号的声明按工具名命中目录（保留目录里的规范名）")
	assert.Equal(t, []string{"write"}, pin.DisallowedFunctions)
	assert.Equal(t, []string{"missing_tool"}, pin.UnavailableAllowed, "未命中的声明进入诊断")
	assert.Equal(t, []string{"Bash(git:*)"}, pin.QualifiedAllowed)
	assert.Equal(t, "claude-sonnet", pin.SkillModel)
	assert.Equal(t, "high", pin.SkillEffort)

	hints := skillTurnHints(pin)
	assert.Contains(t, hints, "## Skill execution hints")
	assert.Contains(t, hints, "model: claude-sonnet")
	assert.Contains(t, hints, "effort: high")
	assert.Contains(t, hints, "unavailable allowed-tools: missing_tool")
	assert.Contains(t, hints, "disallowed-tools: write")
}

func TestSkillTurnHintsEmptyWithoutDeclarations(t *testing.T) {
	assert.Empty(t, skillTurnHints(&skillTurnPin{PinnedFunctions: []string{"skill__x"}}))
	assert.Empty(t, skillTurnHints(nil))
}

// TestRestrictFunctionSelectionAllowedOnly 允许清单只减不增：不在清单里的内置
// 工具与 skill 函数都被移出本回合函数面，触发本回合的 skill 函数本身恒保留。
func TestRestrictFunctionSelectionAllowedOnly(t *testing.T) {
	selection := &aicliFunctionSelection{
		Mode:               "prefer",
		IncludeBuiltin:     true,
		BuiltinFunctions:   []string{"bash", "write", "read_file"},
		SkillFunctions:     []string{"skill__restricted", "skill__other"},
		FinalFunctionNames: []string{"bash", "write", "read_file", "skill__restricted", "skill__other"},
		Schemas: []map[string]interface{}{
			{"name": "bash"},
			{"name": "write"},
			{"name": "read_file"},
			{"name": "skill__restricted"},
			{"name": "skill__other"},
		},
	}
	pin := &skillTurnPin{
		PinnedFunctions:  []string{"skill__restricted", "bash"},
		AllowedFunctions: []string{"bash"},
	}

	restricted := restrictFunctionSelection(selection, pin)
	require.NotNil(t, restricted)
	assert.Equal(t, []string{"bash"}, restricted.BuiltinFunctions)
	assert.Equal(t, []string{"skill__restricted"}, restricted.SkillFunctions, "触发本回合的 skill 函数恒保留")
	assert.Equal(t, []string{"bash", "skill__restricted"}, restricted.FinalFunctionNames)
	require.Len(t, restricted.Schemas, 2)
	assert.Equal(t, "bash", restricted.Schemas[0]["name"])
	assert.Equal(t, "skill__restricted", restricted.Schemas[1]["name"])

	// 原选择对象（会话级稳定快照）不得被修改。
	assert.Len(t, selection.Schemas, 5)
	assert.Equal(t, []string{"bash", "write", "read_file"}, selection.BuiltinFunctions)
}

// TestRestrictFunctionSelectionDeniedOnly 只有 disallowed-tools 时不做白名单收窄，
// 仅剔除被禁工具。
func TestRestrictFunctionSelectionDeniedOnly(t *testing.T) {
	selection := &aicliFunctionSelection{
		BuiltinFunctions:   []string{"bash", "write"},
		SkillFunctions:     []string{"skill__x"},
		FinalFunctionNames: []string{"bash", "write", "skill__x"},
		Schemas:            []map[string]interface{}{{"name": "bash"}, {"name": "write"}, {"name": "skill__x"}},
	}
	pin := &skillTurnPin{
		PinnedFunctions:     []string{"skill__x"},
		DisallowedFunctions: []string{"WRITE"},
	}

	restricted := restrictFunctionSelection(selection, pin)
	require.NotNil(t, restricted)
	assert.Equal(t, []string{"bash"}, restricted.BuiltinFunctions)
	assert.Equal(t, []string{"bash", "skill__x"}, restricted.FinalFunctionNames)
	require.Len(t, restricted.Schemas, 2)

	// 无声明时原样返回（指针相同，不产生副本）。
	same := restrictFunctionSelection(selection, &skillTurnPin{PinnedFunctions: []string{"skill__x"}})
	assert.Same(t, selection, same)
}

// TestResolveSkillTurnPinAppliesStandardToolFields 端到端：SKILL.md 里的
// allowed-tools / disallowed-tools / model / effort 真的进到 pin 与 guide。
func TestResolveSkillTurnPinAppliesStandardToolFields(t *testing.T) {
	tempDir := t.TempDir()
	chdirTest(t, tempDir)
	skillDir := filepath.Join(tempDir, "restricted_skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	body := strings.Join([]string{
		"---",
		"name: restricted_skill",
		"description: tool restriction test skill",
		"allowed-tools:",
		"  - read_file",
		"  - Bash(git:*)",
		"disallowed-tools:",
		"  - write",
		"model: claude-sonnet",
		"effort: high",
		"---",
		"",
		"Do the thing with $ARGUMENTS.",
		"",
	}, "\n")
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644))

	cfg := &config.Config{
		SkillsRuntime: &config.SkillsRuntimeConfig{Enabled: true, SkillDir: tempDir},
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
	session.SkillsBinding = binding

	// allowed-tools 按函数目录（catalog schema 索引）解析：先把内置工具放进注册表，
	// 否则 read_file/bash 无从命中，会退化成诊断列表。
	for _, name := range []string{"bash", "read_file", "write"} {
		session.FunctionRegistry.Register(&testFunction{name: name})
	}

	functionName := ""
	for name := range binding.skillFunctions {
		if strings.Contains(strings.ToLower(name), "restricted_skill") {
			functionName = name
			break
		}
	}
	require.NotEmpty(t, functionName, "skill 函数应已注册: %#v", binding.skillFunctions)

	pin, err := resolveSkillTurnPin(session, &SendSkillTurnRequest{
		SkillName:     functionName,
		Prompt:        "run it",
		VisiblePrompt: "/skill " + functionName + " run it",
	})
	require.NoError(t, err)
	require.NotNil(t, pin)

	assert.Contains(t, pin.AllowedFunctions, "bash", "Bash(git:*) 应折算为目录里的 bash")
	assert.Contains(t, pin.AllowedFunctions, "read_file")
	assert.Equal(t, []string{"write"}, pin.DisallowedFunctions)
	assert.Equal(t, "claude-sonnet", pin.SkillModel)
	assert.Equal(t, "high", pin.SkillEffort)
	assert.Contains(t, pin.Guide, "Skill execution hints")
	assert.Contains(t, pin.Guide, "effort: high")

	// $ARGUMENTS 与 ${effort} 的建议值：skill 声明的 effort 覆盖会话值。
	selection := &aicliFunctionSelection{
		BuiltinFunctions:   []string{"bash", "write", "read_file"},
		FinalFunctionNames: []string{"bash", "write", "read_file", functionName},
		Schemas: []map[string]interface{}{
			{"name": "bash"}, {"name": "write"}, {"name": "read_file"}, {"name": functionName},
		},
	}
	overlaid := overlayPinnedFunctions(selection, pin)
	require.NotNil(t, overlaid)
	assert.NotContains(t, overlaid.BuiltinFunctions, "write", "disallowed-tools 必须真的从函数面移除")
	assert.Contains(t, overlaid.FinalFunctionNames, functionName, "skill 函数本身保留")
	for _, schema := range overlaid.Schemas {
		assert.NotEqual(t, "write", schema["name"], "被禁工具不得留在请求 schema 里")
	}
}

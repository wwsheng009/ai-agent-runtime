package commands

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// skillSurfaceFakeExecutor 是 skillToolSurface 测试用的确定性执行器。
type skillSurfaceFakeExecutor struct {
	mainLoopPrompt string
	output         string
}

func (f *skillSurfaceFakeExecutor) Execute(_ context.Context, _ *runtimeskill.Skill, _ *runtimetypes.Request) (*runtimeskill.ExecuteResult, error) {
	output := f.output
	if output == "" {
		output = f.mainLoopPrompt
	}
	return &runtimeskill.ExecuteResult{Success: true, Output: output}, nil
}

func (f *skillSurfaceFakeExecutor) BuildMainLoopPrompt(_ *runtimeskill.Skill, _ *runtimetypes.Request) (string, error) {
	return f.mainLoopPrompt, nil
}

func newSkillSurfaceTestSession(t *testing.T) (*ChatSession, *SkillFunction) {
	t.Helper()
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	session := &ChatSession{
		FunctionRegistry: registry,
		FunctionCatalog:  catalog,
	}
	fn := newSkillSurfaceTestFunction("brand-guidelines", "## Skill instructions (brand-guidelines)\n- Orange: #d97757")
	catalog.RegisterSkillFunction(fn)
	return session, fn
}

func newSkillSurfaceTestFunction(name, prompt string) *SkillFunction {
	return &SkillFunction{
		summary: &runtimeskill.SkillSummary{
			Name:        name,
			Description: "Anthropic brand colors and typography",
		},
		functionName: buildSkillFunctionName(name),
		skill:        &runtimeskill.Skill{Name: name, Description: "Anthropic brand colors and typography"},
		executor:     &skillSurfaceFakeExecutor{mainLoopPrompt: prompt},
	}
}

// 回归：模型直接调用 skill__* 时，工具面必须能解析并转发到函数目录执行，
// 而不是像原先那样在预检阶段直接判 TOOL_NOT_FOUND。
func TestSkillToolSurfaceResolvesAndExecutesSkillFunction(t *testing.T) {
	session, fn := newSkillSurfaceTestSession(t)
	surface := wrapSkillToolSurface(session, nil)

	info, err := surface.FindTool(fn.Name())
	require.NoError(t, err)
	require.Equal(t, fn.Name(), info.Name)
	require.Equal(t, skillToolSurfaceMCPName, info.MCPName)
	require.Contains(t, info.Description, "brand")

	output, err := surface.CallTool(context.Background(), info.MCPName, fn.Name(), map[string]interface{}{
		"prompt": "列出品牌主色",
	})
	require.NoError(t, err)
	require.Contains(t, output, "#d97757", "skill 函数执行必须返回函数目录的结果")

	rich, ok := surface.(*skillToolSurface)
	require.True(t, ok)
	output, _, err = rich.CallToolWithMeta(context.Background(), info.MCPName, fn.Name(), map[string]interface{}{
		"prompt": "列出品牌主色",
	})
	require.NoError(t, err)
	require.Contains(t, output, "#d97757")
}

// 常驻工具面必须保持原样：skill 函数不能借 ListTools 绕过 skills-top-k 路由。
func TestSkillToolSurfaceKeepsListToolsUnchanged(t *testing.T) {
	session, fn := newSkillSurfaceTestSession(t)
	next := stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{{Name: "view", MCPName: "toolkit", Enabled: true}}}
	surface := wrapSkillToolSurface(session, next)

	require.Equal(t, next.tools, surface.ListTools())

	info, err := surface.FindTool("view")
	require.NoError(t, err)
	require.Equal(t, "view", info.Name)
	require.Equal(t, "toolkit", info.MCPName)

	_, err = surface.FindTool(fn.Name())
	require.NoError(t, err, "skill 函数仍应可解析")

	_, err = surface.FindTool("skill__missing")
	require.Error(t, err)
}

// 未配置执行器的 skill 条目（目录可读但不可执行）不得被工具面认领。
func TestSkillToolSurfaceSkipsSkillFunctionWithoutExecutor(t *testing.T) {
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	session := &ChatSession{FunctionRegistry: registry, FunctionCatalog: catalog}
	catalog.RegisterSkillFunction(&SkillFunction{
		summary:      &runtimeskill.SkillSummary{Name: "no-executor", Description: "document only"},
		functionName: buildSkillFunctionName("no-executor"),
	})
	surface := wrapSkillToolSurface(session, nil)

	_, err := surface.FindTool(buildSkillFunctionName("no-executor"))
	require.Error(t, err)
}

// 合成 policy allowlist 必须覆盖会话 skill 函数，否则 FindTool 之后仍会被
// AllowToolInfo 拒绝（热加载新增项由 syncLocalChatToolPolicyAllowlist 补齐）。
func TestLocalChatToolPolicyIncludesSkillFunctions(t *testing.T) {
	session, fn := newSkillSurfaceTestSession(t)
	surface := wrapSkillToolSurface(session, stubLocalChatToolSurface{
		tools: []runtimeskill.ToolInfo{{Name: "view", MCPName: "toolkit", Enabled: true}},
	})

	policy := buildLocalChatToolPolicy(session, surface, nil)
	require.NotNil(t, policy)
	require.True(t, policy.AllowlistEnabled)
	require.True(t, policy.AllowedTools[fn.Name()], "skill 函数必须进入合成 allowlist")
	require.True(t, policy.AllowedTools["view"])

	// 运行中热加载新增的 skill：turn 边界同步必须能把它补进同一份策略。
	hotFn := newSkillSurfaceTestFunction("hot-skill", "hot")
	session.FunctionCatalog.RegisterSkillFunction(hotFn)
	added := syncLocalChatToolPolicyAllowlist(session, surface, nil, policy)
	require.Equal(t, []string{hotFn.Name()}, added)
	require.True(t, policy.AllowedTools[hotFn.Name()])
	require.Empty(t, syncLocalChatToolPolicyAllowlist(session, surface, nil, policy), "同步必须幂等")

	// 显式策略（session.ToolPolicy 非 nil）不得被工具面扩权。
	explicit := &ChatSession{
		FunctionRegistry: session.FunctionRegistry,
		FunctionCatalog:  session.FunctionCatalog,
		ToolPolicy:       runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}
	require.Empty(t, syncLocalChatToolPolicyAllowlist(explicit, surface, nil, explicit.ToolPolicy))
	require.False(t, explicit.ToolPolicy.AllowedTools[fn.Name()])
}

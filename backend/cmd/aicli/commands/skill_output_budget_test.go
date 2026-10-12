package commands

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/functions"
	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/output"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

// 回归：文本类技能（纯说明型，无 handler/workflow）的正文就是全部载荷。
// 技能函数结果必须声明模型可见预算，否则 >12 KiB 的正文会被渲染层按通用
// 回退预算折叠成首段 + artifact 指针（2026-10-12 会话审查发现：skill-creator
// 22309B 只投递了前 226/413 行，正文尾部丢失）。
func TestSkillFunctionExecuteWithMetaDeclaresModelVisibleBudget(t *testing.T) {
	body := "## 技能指令\n" + strings.Repeat("x", 20*1024) // 20 KiB > 12 KiB 层预算
	fn := &SkillFunction{
		summary:      &runtimeskill.SkillSummary{Name: "text-only-skill", Description: "text only"},
		functionName: buildSkillFunctionName("text-only-skill"),
		skill:        &runtimeskill.Skill{Name: "text-only-skill", Description: "text only"},
		executor:     &skillSurfaceFakeExecutor{mainLoopPrompt: body},
	}

	out, meta, err := fn.ExecuteWithMeta(context.Background(), map[string]interface{}{"prompt": "执行文本技能"})
	require.NoError(t, err)
	require.Equal(t, body, out)
	require.Equal(t, skillModelVisibleBudgetBytes, meta[toolresult.MetadataModelVisibleBudgetKey])

	// 声明值经渲染层生效：同一正文不再被折叠，完整进入模型历史。
	rendered := output.RenderToolResultContentForModel(out, "", &output.Envelope{
		ToolName:   fn.Name(),
		ToolCallID: "call_skill_budget",
		OK:         true,
		Metadata:   meta,
	})
	require.NotContains(t, rendered, "output truncated for history safety")
	require.Greater(t, len(rendered), 12*1024)

	// 反向对照：无声明时同一正文按层回退预算折叠——证明声明是生效变量，
	// 而不是渲染层恰好放过了 20 KiB 正文。
	folded := output.RenderToolResultContentForModel(out, "", &output.Envelope{
		ToolName:   fn.Name(),
		ToolCallID: "call_skill_budget_unset",
		OK:         true,
	})
	require.Contains(t, folded, "output truncated for history safety")
}

// newSkillWindowTestSession 构造带窗口 provider 的会话与文本技能函数：provider
// 与生产接线一致（skillOutputWindowBytesForConfig 读 session.Config），并同步
// 登记 SkillsBinding 以覆盖 /skill 直调 pin 解析路径。
func newSkillWindowTestSession(t *testing.T, cfg *config.SkillsRuntimeConfig, name, body string) (*ChatSession, *SkillFunction) {
	t.Helper()
	registry := functions.NewFunctionRegistry()
	catalog := newAICLIFunctionCatalog("openai", registry)
	session := &ChatSession{
		FunctionRegistry: registry,
		FunctionCatalog:  catalog,
		Config:           &config.Config{SkillsRuntime: cfg},
	}
	fn := &SkillFunction{
		summary:           &runtimeskill.SkillSummary{Name: name, Description: "window test"},
		functionName:      buildSkillFunctionName(name),
		skill:             &runtimeskill.Skill{Name: name, Description: "window test"},
		executor:          &skillSurfaceFakeExecutor{mainLoopPrompt: body},
		skillOutputWindow: func() int { return skillOutputWindowBytesForConfig(session.Config) },
	}
	catalog.RegisterSkillFunction(fn)
	binding := &skillsRuntimeBinding{skillFunctions: map[string]*SkillFunction{fn.Name(): fn}}
	catalog.SetSkillsBinding(binding)
	session.SkillsBinding = binding
	return session, fn
}

// renderSkillFunctionResult 走执行侧工具面（/skill 回合模型调用 skill 函数的
// 同一条链）获取结果，并按其 metadata 渲染进模型历史。
func renderSkillFunctionResult(t *testing.T, session *ChatSession, fn *SkillFunction) string {
	t.Helper()
	surface, ok := wrapSkillToolSurface(session, nil).(*skillToolSurface)
	require.True(t, ok, "wrapSkillToolSurface 必须返回可走 rich 路径的 *skillToolSurface")
	out, meta, err := surface.CallToolWithMeta(context.Background(), skillToolSurfaceMCPName, fn.Name(), map[string]interface{}{"prompt": "执行"})
	require.NoError(t, err)
	require.Equal(t, skillOutputWindowBytesForConfig(session.Config), meta[toolresult.MetadataModelVisibleBudgetKey])
	require.Equal(t, toolresult.FoldRecoveryArtifactRead, meta[toolresult.MetadataFoldRecoveryKey])
	return output.RenderToolResultContentForModel(out, "", &output.Envelope{
		ToolName:   fn.Name(),
		ToolCallID: "call_skill_window",
		OK:         true,
		Metadata:   meta,
	})
}

// 配置项 skills_runtime.skill_output_window_bytes 驱动技能结果的声明窗口：
// 同一份 40 KiB 正文在默认 32 KiB 窗口下折叠（且提示 artifact_read 续读），
// 配置 64 KiB 后完整进入历史。
func TestSkillOutputWindowConfigDrivesDeclaredBudget(t *testing.T) {
	body := "## 技能指令\n" + strings.Repeat("z", 40*1024)

	session, fn := newSkillWindowTestSession(t, nil, "cfg-window-skill", body)
	folded := renderSkillFunctionResult(t, session, fn)
	require.Contains(t, folded, "output truncated for history safety")
	require.Contains(t, folded, "artifact_read")
	require.Less(t, len(folded), len(body))

	raised, fnRaised := newSkillWindowTestSession(t, &config.SkillsRuntimeConfig{SkillOutputWindowBytes: 64 * 1024}, "cfg-window-skill", body)
	full := renderSkillFunctionResult(t, raised, fnRaised)
	require.NotContains(t, full, "output truncated for history safety")
	require.Greater(t, len(full), 40*1024)
}

// /skill 直调端到端：pin 产出 ProgramGuide（要求模型用函数加载正文），模型按
// 指引调用 skill 函数后，20 KiB 正文完整进入模型历史——锁定"正文经工具结果
// 完整到达历史"的契约，而不是被 12 KiB 通用预算折叠。
func TestSkillTurnDirectCallBodyReachesHistoryInFull(t *testing.T) {
	body := "## 技能指令\n" + strings.Repeat("w", 20*1024)
	session, fn := newSkillWindowTestSession(t, nil, "direct-call-skill", body)

	pin, err := resolveSkillTurnPin(session, &SendSkillTurnRequest{SkillName: fn.Name(), Prompt: "生成设计系统"})
	require.NoError(t, err)
	require.NotNil(t, pin)
	require.Contains(t, pin.Guide, "Skill program guide")
	require.Contains(t, pin.Guide, fn.Name())

	full := renderSkillFunctionResult(t, session, fn)
	require.NotContains(t, full, "output truncated for history safety")
	require.Greater(t, len(full), 20*1024)
	require.Contains(t, full, strings.Repeat("w", 512))
}

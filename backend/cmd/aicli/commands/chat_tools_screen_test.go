package commands

// /tools 屏渲染的单测（§14/§17 收官：TUI 观测面与 web /runtime/tools 同源）。
// 冻结面分支与本地目录分支各一组，渲染断言按账本分组口径。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolsurface"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

func TestRenderToolsScreenBodyGroupsByLedger(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{
		{Name: "shell", Description: "Run a shell command", Metadata: map[string]interface{}{"surface_source": "core"}},
		{Name: "view", Description: "View file content", Metadata: map[string]interface{}{"surface_source": "core"}},
		{Name: "job_start", Description: "Start a background job", Metadata: map[string]interface{}{"surface_source": "orchestration"}},
		{Name: "take_snapshot", Description: "Snapshot page", Metadata: map[string]interface{}{"surface_source": "mcp:chrome-devtools"}},
	}

	body := renderToolsScreenBody(tools)
	require.Contains(t, body, "当前会话工具面：4 个工具")
	require.Contains(t, body, "[core] ×2")
	require.Contains(t, body, "[orchestration] ×1")
	require.Contains(t, body, "[mcp:chrome-devtools] ×1")
	require.Contains(t, body, "• shell — Run a shell command")
	// 组内字母序。
	coreBlock := body[strings.Index(body, "[core]"):strings.Index(body, "[orchestration]")]
	require.Less(t, strings.Index(coreBlock, "shell"), strings.Index(coreBlock, "view"))
}

func TestRenderToolsScreenBodyDefaultsToCoreWithoutLedger(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{{Name: "bare_tool"}}
	body := renderToolsScreenBody(tools)
	require.Contains(t, body, "[core] ×1")
	require.Contains(t, body, "• bare_tool")
}

func TestRenderToolsScreenBodyTruncatesLongDescription(t *testing.T) {
	tools := []runtimetypes.ToolDefinition{
		{Name: "verbose", Description: strings.Repeat("很长的描述", 60)},
	}
	body := renderToolsScreenBody(tools)
	// 96 rune 截断 + … 结尾（180 rune 输入 → 96 rune 输出 + 省略号）。
	require.Contains(t, body, "…")
	require.NotContains(t, body, strings.Repeat("很长的描述", 60))
}

// 冻结面缺失时（刚 /profile use、尚未开始 turn）的兜底口径：基线候选必须按
// session.ToolPolicy 过滤，展示下一 turn 请求面，并列出被策略裁剪的工具名。
func TestBuildToolsScreenBodyFallbackAppliesSessionPolicy(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view", Description: "View file content"},
				{Name: "shell", Description: "Run a shell command"},
				{Name: "write", Description: "Write file content"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：1 个工具（下一 turn 生效面")
	require.Contains(t, body, "• view — View file content")
	require.NotContains(t, body, "• shell")
	require.NotContains(t, body, "• write")
	require.Contains(t, body, "策略裁剪: 2 个工具不进入下一 turn 请求面: shell, write")
}

// disable_tools 与请求面口径一致：只关执行与 tool_choice，目录保持完整，
// 展示不按策略裁剪（与 agent loop 的 disable_tools 例外同语义）。
func TestBuildToolsScreenBodyFallbackKeepsCatalogWhenDisableTools(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		DisableTools: true,
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view"},
				{Name: "shell"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：2 个工具（与 /api/runtime/tools 同源）")
	require.Contains(t, body, "• shell")
	require.NotContains(t, body, "策略裁剪")
}

// 策略把候选全部收窄掉时也必须可见（0 个工具 + 裁剪明细），不能退回
// "没有可枚举的工具面"。
func TestBuildToolsScreenBodyFallbackReportsEmptyFilteredSurface(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "shell"},
			}},
		},
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{"view"}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：0 个工具")
	require.Contains(t, body, "策略裁剪: 1 个工具不进入下一 turn 请求面: shell")
}

// 无策略时保持原有基线口径（不新增过滤说明），避免对无 profile 会话改变输出。
func TestBuildToolsScreenBodyFallbackWithoutPolicyKeepsBaselineRendering(t *testing.T) {
	session := &ChatSession{
		ChatExecutor: newAICLIActorChatExecutor(),
		LocalRuntimeHost: &localChatRuntimeHost{
			ToolSurface: stubLocalChatToolSurface{tools: []runtimeskill.ToolInfo{
				{Name: "view"},
				{Name: "shell"},
			}},
		},
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：2 个工具（与 /api/runtime/tools 同源）")
	require.NotContains(t, body, "策略裁剪")
	require.NotContains(t, body, "下一 turn 生效面")
}

func TestFormatBlockedToolNamesTruncatesOverLimit(t *testing.T) {
	names := make([]string, 0, 18)
	for i := 0; i < 18; i++ {
		names = append(names, fmt.Sprintf("tool_%02d", i))
	}

	text := formatBlockedToolNames(names)
	require.Contains(t, text, "tool_00, tool_01")
	require.Contains(t, text, "…（共 18 个）")
	require.NotContains(t, text, "tool_17")
}

// §2.2-C / T7：server 模式下 /tools 直接消费 /api/runtime/tools 的同一 payload。
// 计数、分组、裁剪明细全部取端点字段；本地 session.ToolPolicy 不再二次裁剪——
// 旧实现用本地策略重算目录正是 "10 vs 22" 缺口的来源，这里做回归锁定。
func TestBuildToolsScreenBodyServerModeUsesEndpointPayload(t *testing.T) {
	tools := make([]runtimetypes.ToolDefinition, 0, 22)
	for i := 0; i < 20; i++ {
		name := fmt.Sprintf("core_tool_%02d", i)
		tools = append(tools, runtimetypes.ToolDefinition{
			Name:        name,
			Description: "core capability " + name,
			Metadata:    map[string]interface{}{"surface_source": "core"},
		})
	}
	for _, name := range []string{"job_start", "subagent_spawn"} {
		tools = append(tools, runtimetypes.ToolDefinition{
			Name:        name,
			Description: "orchestration capability " + name,
			Metadata:    map[string]interface{}{"surface_source": "orchestration"},
		})
	}
	require.Len(t, tools, 22)

	payload := map[string]interface{}{
		"session_id": "sess-22",
		"source":     "runtime-server",
		"count":      len(tools),
		"tools":      tools,
		"groups": []toolsurface.Group{
			{ID: "core", Label: "工作区", Protected: true, Enabled: true, ToolCount: 20},
			{ID: "orchestration", Label: "编排", Enabled: true, ToolCount: 2},
		},
		"filtered": []toolsurface.FilteredTool{
			{Name: "mcp:chrome-devtools", Reason: toolsurface.FilterMCPDisabled},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := "/api/runtime/sessions/sess-22/runtime/tools"; r.URL.Path != want {
			t.Errorf("runtime tools path = %q, want %q", r.URL.Path, want)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encode payload: %v", err)
		}
	}))
	defer server.Close()

	session := &ChatSession{
		ChatExecutor:   newAICLIRuntimeServerChatExecutor(server.URL),
		RuntimeSession: &runtimechat.Session{ID: "sess-22"},
		// 本地策略只放行 10 个：server 模式必须无视它（端点才是权威口径）。
		ToolPolicy: runtimepolicy.NewToolExecutionPolicy([]string{
			"view", "read", "shell", "write", "edit", "glob", "grep", "ls", "apply_patch", "multiedit",
		}, false),
	}

	body, ok := buildToolsScreenBody(session)
	require.True(t, ok)
	require.Contains(t, body, "当前会话工具面：22 个工具（与 /api/runtime/tools 同一 payload）")
	require.NotContains(t, body, "10 个工具")
	require.NotContains(t, body, "策略裁剪")
	// 分组目录行取端点 groups[]（含 protected/enabled/计数）。
	require.Contains(t, body, "分组: core(工作区) ×20, orchestration(编排) ×2")
	// 裁剪明细取端点 filtered[]，不在本地重算。
	require.Contains(t, body, "已裁剪: mcp:chrome-devtools(mcp_disabled)")
	// 正文仍按 surface_source 分组，且两端工具都在。
	require.Contains(t, body, "[core] ×20")
	require.Contains(t, body, "[orchestration] ×2")
	require.Contains(t, body, "• job_start — orchestration capability job_start")
}

// 旧版 server（响应缺 groups[]）时用共享构建点补齐，渲染口径不变；
// 缺 filtered[] 时按空数组处理（可加性契约：不出现 nil/缺失差异）。
func TestServerToolsObservationFillsGroupsWhenEndpointOmitsThem(t *testing.T) {
	payload := runtimeServerRuntimeToolsResponse{
		Tools: []runtimetypes.ToolDefinition{
			{Name: "job_start", Description: "Start a job", Metadata: map[string]interface{}{"surface_source": "orchestration"}},
		},
	}

	observation := serverToolsObservation(payload)
	require.NotEmpty(t, observation.Groups)
	require.NotNil(t, observation.Filtered)

	body := renderToolsScreenObservation(observation)
	require.Contains(t, body, "当前会话工具面：1 个工具（与 /api/runtime/tools 同一 payload）")
	require.Contains(t, body, "[orchestration] ×1")
	require.NotContains(t, body, "已裁剪")
}

// 端点已给 groups[]/filtered[] 时原样采用（端点权威）：已关闭的组必须可见。
func TestServerToolsObservationKeepsEndpointGroupsAndFiltered(t *testing.T) {
	payload := runtimeServerRuntimeToolsResponse{
		Tools: []runtimetypes.ToolDefinition{
			{Name: "view", Description: "View file content", Metadata: map[string]interface{}{"surface_source": "core"}},
		},
		Groups: []toolsurface.Group{
			{ID: "core", Label: "工作区", Protected: true, Enabled: true, ToolCount: 7},
			{ID: "mesh", Label: "网格", Enabled: false, ToolCount: 0},
		},
		Filtered: []toolsurface.FilteredTool{
			{Name: "mcp:chrome-devtools", Reason: toolsurface.FilterMCPDisabled},
		},
	}

	observation := serverToolsObservation(payload)
	require.Equal(t, payload.Groups, observation.Groups)
	require.Equal(t, payload.Filtered, observation.Filtered)

	body := renderToolsScreenObservation(observation)
	require.Contains(t, body, "分组: core(工作区) ×7, mesh(网格) 已关闭 ×0")
	require.Contains(t, body, "已裁剪: mcp:chrome-devtools(mcp_disabled)")
	require.Contains(t, body, "• view — View file content")
}

func TestFormatFilteredToolsTruncatesOverLimit(t *testing.T) {
	filtered := make([]toolsurface.FilteredTool, 0, 11)
	for i := 0; i < 11; i++ {
		filtered = append(filtered, toolsurface.FilteredTool{Name: fmt.Sprintf("mcp:server_%02d", i), Reason: toolsurface.FilterMCPDisabled})
	}

	text := formatFilteredTools(filtered)
	require.Contains(t, text, "mcp:server_00(mcp_disabled), mcp:server_01(mcp_disabled)")
	require.Contains(t, text, "…（共 11 个）")
	require.NotContains(t, text, "mcp:server_10")
}

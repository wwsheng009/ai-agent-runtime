package commands

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolsurface"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
	usageanalytics "github.com/wwsheng009/ai-agent-runtime/internal/usageanalytics"
)

// chatWebAnalysisAvailableTool 是会话可用工具面里的单个工具身份条目。
//
// 分析页只做「使用 / 未使用」对账，不投影 schema / description：工具定义可能
// 很大，而页面已经能从 usage_* 明细拿到调用统计；这里只补工具名与可选来源。
type chatWebAnalysisAvailableTool struct {
	Name   string `json:"name"`
	Source string `json:"source,omitempty"`
}

// chatWebAnalysisToolsResponse 在 usageanalytics.ToolStatsResult 之上追加
// available_tools：前者只含「统计窗口内实际调用过」的工具，后者是当前会话
// 模型可见工具面。两者取并集，前端才能把 0 次调用的工具也列出来。
type chatWebAnalysisToolsResponse struct {
	usageanalytics.ToolStatsResult
	AvailableTools       []chatWebAnalysisAvailableTool `json:"available_tools"`
	AvailableToolsSource string                         `json:"available_tools_source,omitempty"`
}

const (
	chatWebAnalysisToolsSourceStable      = "stable_surface"
	chatWebAnalysisToolsSourceCatalog     = "tool_catalog"
	chatWebAnalysisToolsSourceSession     = "session_tool_surface"
	chatWebAnalysisToolsSourceDisabled    = "disabled"
	chatWebAnalysisToolsSourceScopeAll    = "scope_all"
	chatWebAnalysisToolsSourceOther       = "other_session"
	chatWebAnalysisToolsSourceUnavailable = "unavailable"
)

// chatWebAnalysisToolsPayload 组装 /tools 响应：usage 统计保持原契约，
// available_tools 只在「当前会话」范围注入。全局聚合（scope=all）与显式查询
// 其它会话时不能拿当前会话的工具面冒充它们的分母，否则会把别的会话里常用的
// 工具误标成未使用。
func chatWebAnalysisToolsPayload(session *ChatSession, values url.Values, result usageanalytics.ToolStatsResult) chatWebAnalysisToolsResponse {
	response := chatWebAnalysisToolsResponse{
		ToolStatsResult: result,
		AvailableTools:  []chatWebAnalysisAvailableTool{},
	}
	scope := strings.TrimSpace(values.Get("scope"))
	if strings.EqualFold(scope, chatWebAnalysisScopeAll) {
		response.AvailableToolsSource = chatWebAnalysisToolsSourceScopeAll
		return response
	}
	currentID := currentRuntimeSessionID(session)
	querySessionID := chatWebAnalysisFirstValue(values, "session_id", "session")
	if querySessionID != "" && !strings.EqualFold(querySessionID, currentID) {
		response.AvailableToolsSource = chatWebAnalysisToolsSourceOther
		return response
	}
	response.AvailableTools, response.AvailableToolsSource = chatWebAnalysisAvailableTools(session, currentID)
	return response
}

// chatWebAnalysisAvailableTools 解析当前会话的模型可见工具面，返回工具身份
// 列表和来源标签。优先级与 TUI /tools、runtime /runtime/tools 对齐：
//
//  1. actor 冻结面（StableToolSurface）：真实请求前缀，最精确；
//  2. actor 当前候选目录（CollectToolCatalogDefinitions）：首轮前尚未冻结时
//     仍能拿到 MCP + scheduler + broker + 内置的完整候选面；
//  3. 本地装配候选目录：actor 已 idle 驱逐或尚未建立时按同一装配路径取；
//  4. durable runtime state 里持久化的 StableToolSurface：装配不可得时仍可
//     对账最近一次真实 turn；
//  5. ToolSurface 目录：最后兜底（只含 MCP/goal 包装面）。
//
// 任一来源不可得时返回空数组而不是猜测。session.DisableTools=true 时没有
// 可执行工具，直接返回 disabled，避免把「仍在请求里但 tool_choice=none」的
// 工具错误展示成可调用。
func chatWebAnalysisAvailableTools(session *ChatSession, sessionID string) ([]chatWebAnalysisAvailableTool, string) {
	sessionID = strings.TrimSpace(sessionID)
	if session == nil || sessionID == "" {
		return []chatWebAnalysisAvailableTool{}, chatWebAnalysisToolsSourceUnavailable
	}
	if session.DisableTools {
		return []chatWebAnalysisAvailableTool{}, chatWebAnalysisToolsSourceDisabled
	}
	host := session.LocalRuntimeHost
	if host == nil {
		return []chatWebAnalysisAvailableTool{}, chatWebAnalysisToolsSourceUnavailable
	}

	if host.SessionHub != nil {
		if actor, ok := host.SessionHub.Get(sessionID); ok && actor != nil {
			if tools := actor.StableToolSurfaceForInspection(); len(tools) > 0 {
				return chatWebAnalysisAvailableToolEntries(tools), chatWebAnalysisToolsSourceStable
			}
			if tools := actor.ToolCatalogForInspection(context.Background()); len(tools) > 0 {
				return chatWebAnalysisAvailableToolEntries(tools), chatWebAnalysisToolsSourceCatalog
			}
		}
	}

	if tools, ledger, ok := localToolsScreenAssembly(session, host); ok {
		tools, _ = filterToolsByChatPolicy(session, tools)
		if observation, observed := toolsurface.Observe(tools, ledger, nil); observed {
			tools = observation.Tools
		}
		if len(tools) > 0 {
			return chatWebAnalysisAvailableToolEntries(tools), chatWebAnalysisToolsSourceSession
		}
	}

	if host.RuntimeStore != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		state, err := host.RuntimeStore.LoadState(ctx, sessionID)
		cancel()
		if err == nil && state != nil && state.StableToolSurfaceSet && len(state.StableToolSurface) > 0 {
			return chatWebAnalysisAvailableToolEntries(state.StableToolSurface), chatWebAnalysisToolsSourceStable
		}
	}

	if host.ToolSurface != nil {
		infos := host.ToolSurface.ListTools()
		tools := make([]runtimetypes.ToolDefinition, 0, len(infos))
		for _, info := range infos {
			tools = append(tools, runtimetypes.ToolDefinition{
				Name:        info.Name,
				Description: info.Description,
				Parameters:  info.InputSchema,
				Metadata:    info.Metadata,
			})
		}
		tools, _ = filterToolsByChatPolicy(session, tools)
		if observation, observed := toolsurface.Observe(tools, toolsurface.Ledger{}, nil); observed {
			tools = observation.Tools
		}
		if len(tools) > 0 {
			return chatWebAnalysisAvailableToolEntries(tools), chatWebAnalysisToolsSourceSession
		}
	}

	return []chatWebAnalysisAvailableTool{}, chatWebAnalysisToolsSourceUnavailable
}

// chatWebAnalysisAvailableToolEntries 把工具定义投影成稳定、去重、按名排序的
// 身份列表。surface_source / mcp_name 只作为可选来源透出；缺失时不臆造。
func chatWebAnalysisAvailableToolEntries(definitions []runtimetypes.ToolDefinition) []chatWebAnalysisAvailableTool {
	entries := make([]chatWebAnalysisAvailableTool, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, chatWebAnalysisAvailableTool{
			Name:   name,
			Source: chatWebAnalysisToolSource(definition),
		})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Name < entries[j].Name
	})
	return entries
}

func chatWebAnalysisToolSource(definition runtimetypes.ToolDefinition) string {
	if definition.Metadata == nil {
		return ""
	}
	if source, ok := definition.Metadata[toolsurface.MetadataSurfaceSource].(string); ok {
		if trimmed := strings.TrimSpace(source); trimmed != "" {
			return trimmed
		}
	}
	if mcpName, ok := definition.Metadata["mcp_name"].(string); ok {
		if trimmed := strings.TrimSpace(mcpName); trimmed != "" {
			return "mcp:" + trimmed
		}
	}
	return ""
}

package commands

// /tools 会话内命令：当前会话工具面目录 + 来源账本
// （docs/architecture/unified-tool-surface-registry.md §14/§17 收官——TUI 侧观测面
// 与 web `/runtime/tools` 消费同一 toolsurface.Build 实现点，T1 的观测闭环）。
//
// 数据源优先级（与 web 端点同口径）：
//  1. 会话 actor 的 StableToolSurface（冻结面 = 真实请求前缀，turn 装配时已按
//     执行策略裁剪，天然含 profile 收窄）；
//  2. server 模式：runtime-server 的 GET /api/runtime/tools **同一 payload**
//     （§2.2-C：tools + groups + filtered 直接渲染，不再本地二次 Build/二次分类）；
//  3. 兜底（无 actor / 未冻结 / local 模式）：按 buildLocalChatAgent 同一条装配路径
//     （含 broker 源）取候选目录，用真实 sourcesurface 账本经 toolsurface.Observe
//     装配；装配不可行时退回本会话 ToolSurface 目录。兜底分支按 session.ToolPolicy
//     过滤基线候选，显示"下一 turn 将请求的面"——否则 /profile use 清掉冻结面后，
//     展示反而不含新 profile 的裁剪（看起来像假开关或切完工具变多）。
//
// 输出：按 surface_source 分组 + 计数行；兜底分支附数据源说明与策略裁剪明细。

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolsurface"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// buildToolsScreenBody 渲染当前会话工具面目录（账本分组）。
// ok=false：会话没有可枚举的工具面（未附着 runtime / 无 actor / 无本地目录）。
// 数据源三优先级：actor 冻结面 → runtime-server 端点（server 模式）→ 本地目录。
func buildToolsScreenBody(session *ChatSession) (string, bool) {
	if session == nil {
		return "", false
	}
	if tools, ok := frozenSurfaceForScreen(session); ok {
		return renderToolsScreenBody(tools), true
	}
	// runtime-server 模式：LocalRuntimeHost 为 nil（server 模式不挂载本地能力面），
	// 走 runtime-server 的 /runtime/tools（live 口径，含账本）。
	// headless/首帧路径：ChatExecutor 由后台装载 goroutine 异步建立，/tools 作为
	// 只读命令必须像 turn 一样 ensureChatExecutor await（否则异步未完成时
	// ChatExecutor==nil，三分支全 miss——2026-10-14 ConPTY 实测）。
	if executor, execErr := ensureChatExecutor(session); execErr == nil {
		if serverExecutor, ok := executor.(*aicliRuntimeServerChatExecutor); ok && serverExecutor != nil {
			if session.RuntimeSession == nil {
				return "", false
			}
			payload, listErr := serverExecutor.listRuntimeServerToolsPayload(context.Background(), session, session.RuntimeSession.ID)
			if listErr != nil {
				return fmt.Sprintf("工具面查询失败（runtime-server）: %v", listErr), true
			}
			if len(payload.Tools) == 0 {
				return "", false
			}
			// §2.2-C：TUI 观测面 = 端点同一 payload。这里刻意**不**再按本地
			// session.ToolPolicy 过滤、也不用空账本二次 Build：server 模式下
			// profile 归属 server 会话，本地二次判定正是 10 vs 22 缺口的来源。
			return renderToolsScreenObservation(serverToolsObservation(payload)), true
		}
	}
	if host := session.LocalRuntimeHost; host != nil {
		if catalog, ledger, ok := localToolsScreenAssembly(session, host); ok {
			return renderFallbackToolsScreenBody(session, catalog, ledger), true
		}
		if host.ToolSurface != nil {
			infos := host.ToolSurface.ListTools()
			if len(infos) == 0 {
				return "", false
			}
			catalog := make([]runtimetypes.ToolDefinition, 0, len(infos))
			for _, info := range infos {
				catalog = append(catalog, runtimetypes.ToolDefinition{
					Name:        info.Name,
					Description: info.Description,
					Parameters:  nil,
					Metadata:    cloneToolInfoMetadata(info.Metadata),
				})
			}
			// 兜底目录只有 MCP/goal 包装面（ToolSurface 不含 broker），账本如实缺失：
			// 按 Classify 的 default（core）标注，不虚构编排面。
			return renderFallbackToolsScreenBody(session, catalog, toolsurface.Ledger{}), true
		}
	}
	return "", false
}

// localToolsScreenAssembly 用与本会话请求装配同一条路径重建候选目录（§2.2-C）：
//
//	buildLocalChatAgent（chat_actor_executor_deps.go 同一函数：内置 + MCP + scheduler
//	+ broker 四源）→ CollectToolCatalogDefinitions → Ledger{Broker: 真实 broker}。
//
// 装配不可行（无 Bootstrap/无 session 行/无 ToolSurface，例如单测与降级宿主）返回
// ok=false，调用方退回 ToolSurface 目录口径。
func localToolsScreenAssembly(session *ChatSession, host *localChatRuntimeHost) ([]runtimetypes.ToolDefinition, toolsurface.Ledger, bool) {
	if session == nil || host == nil || host.Bootstrap == nil || host.ToolSurface == nil {
		return nil, toolsurface.Ledger{}, false
	}
	if session.RuntimeSession == nil {
		return nil, toolsurface.Ledger{}, false
	}
	workspaceRoot := ""
	if host.RuntimeConfig != nil {
		workspaceRoot = strings.TrimSpace(host.RuntimeConfig.Workspace.Root)
	}
	apiAgent := buildLocalChatAgent(session, host, host.RuntimeConfig, workspaceRoot, "", "")
	if apiAgent == nil {
		return nil, toolsurface.Ledger{}, false
	}
	catalog := apiAgent.CollectToolCatalogDefinitions(context.Background())
	if len(catalog) == 0 {
		return nil, toolsurface.Ledger{}, false
	}
	return catalog, toolsurface.Ledger{Broker: apiAgent.GetToolBroker()}, true
}

// renderFallbackToolsScreenBody 渲染"冻结面缺失"时的兜底目录：先按当前会话策略
// 过滤（若有），再经 toolsurface.Observe（Build + 共享观测补齐）标注来源账本。
// 过滤后为空也要渲染（0 个工具 + 裁剪明细）：一个把工具全部收窄掉的 profile
// 必须可见，不能退回"没有可枚举的工具面"。
func renderFallbackToolsScreenBody(session *ChatSession, catalog []runtimetypes.ToolDefinition, ledger toolsurface.Ledger) string {
	filtered, blocked := filterToolsByChatPolicy(session, catalog)
	observation, ok := toolsurface.Observe(filtered, ledger, nil)
	tools := observation.Tools
	if !ok {
		// 空目录（含策略收窄到 0）时不虚构账本，直接渲染过滤后的目录。
		tools = filtered
	}
	if !chatToolsPolicyFilterActive(session) {
		return renderToolsScreenBody(tools)
	}
	return renderToolsScreenBodyPolicyFiltered(tools, blocked)
}

// serverToolsObservation 把 runtime-server 的 /runtime/tools payload 映射成共享观测
// 结构（§2.2-C）：端点已给 groups[]/filtered[] 时原样采用，缺失（旧版 server）时
// 用共享构建点本地补齐，保证渲染口径不因服务端版本而分叉。
func serverToolsObservation(payload runtimeServerRuntimeToolsResponse) toolsurface.Observation {
	// 账本为空是有意的：端点返回的工具已带 surface_source（服务端已按真实 broker
	// 账本装配），这里只补 registry 元数据与 groups[]/filtered[] 缺省。
	observation := toolsurface.Enrich(payload.Tools, toolsurface.Ledger{}, payload.Filtered)
	if len(payload.Groups) > 0 {
		observation.Groups = payload.Groups
	}
	return observation
}

// renderToolsScreenObservation 渲染与 /api/runtime/tools 同一 payload 的观测面：
// 计数、分组目录、裁剪明细全部取端点字段，正文仍按 surface_source 分组。
func renderToolsScreenObservation(observation toolsurface.Observation) string {
	var body strings.Builder
	body.WriteString(fmt.Sprintf("当前会话工具面：%d 个工具（与 /api/runtime/tools 同一 payload）\n", len(observation.Tools)))
	if line := renderToolsScreenGroupDirectory(observation.Groups); line != "" {
		body.WriteString(line)
	}
	if len(observation.Filtered) > 0 {
		body.WriteString(fmt.Sprintf("  已裁剪: %s\n", formatFilteredTools(observation.Filtered)))
	}
	body.WriteString(renderToolsScreenGroups(observation.Tools))
	return body.String()
}

// renderToolsScreenGroupDirectory 渲染 groups[] 目录行（只列有工具或已关闭的组）。
func renderToolsScreenGroupDirectory(groups []toolsurface.Group) string {
	if len(groups) == 0 {
		return ""
	}
	parts := make([]string, 0, len(groups))
	for _, group := range groups {
		id := strings.TrimSpace(group.ID)
		if id == "" {
			continue
		}
		if group.ToolCount == 0 && group.Enabled {
			continue
		}
		label := id
		if groupLabel := strings.TrimSpace(group.Label); groupLabel != "" {
			label = fmt.Sprintf("%s(%s)", id, groupLabel)
		}
		if !group.Enabled {
			label += " 已关闭"
		}
		parts = append(parts, fmt.Sprintf("%s ×%d", label, group.ToolCount))
	}
	if len(parts) == 0 {
		return ""
	}
	return fmt.Sprintf("  分组: %s\n", strings.Join(parts, ", "))
}

// formatFilteredTools 折叠 filtered[]：name(reason) 形式，超过上限截断并给出总数。
func formatFilteredTools(filtered []toolsurface.FilteredTool) string {
	parts := make([]string, 0, len(filtered))
	for _, item := range filtered {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s(%s)", name, item.Reason))
	}
	if len(parts) == 0 {
		return ""
	}
	const maxNames = 8
	if len(parts) > maxNames {
		return strings.Join(parts[:maxNames], ", ") + fmt.Sprintf(" …（共 %d 个）", len(parts))
	}
	return strings.Join(parts, ", ")
}

// filterToolsByChatPolicy 按会话执行策略过滤候选工具面，语义与 agent loop 的
// filterPolicyBlockedToolDefinitions 一致（policy.AllowsDefinition：暴露前的
// allowlist / denylist / read_only / delegation 判定）。profile 的工具裁剪最终
// 投影到 session.ToolPolicy（applyProfileStateToChatSession → buildLocalChatToolPolicy），
// 所以这一过滤让 /tools 在冻结面重建前就显示下一 turn 的真实请求面。
//
// disable_tools 例外与请求面一致：agent loop 对 disable_tools 保留完整目录
// （只关执行与 tool_choice），展示口径同样不裁剪。
func filterToolsByChatPolicy(session *ChatSession, tools []runtimetypes.ToolDefinition) ([]runtimetypes.ToolDefinition, []string) {
	if !chatToolsPolicyFilterActive(session) {
		return tools, nil
	}
	policy := session.ToolPolicy
	filtered := make([]runtimetypes.ToolDefinition, 0, len(tools))
	var blocked []string
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if !policy.AllowsDefinition(name) {
			blocked = append(blocked, name)
			continue
		}
		filtered = append(filtered, tool)
	}
	sort.Strings(blocked)
	return filtered, blocked
}

// chatToolsPolicyFilterActive：会话有策略且未 disable_tools 时，兜底展示才应用
// 策略过滤（无策略 = 基线即下一 turn 请求面，保持原渲染口径）。
func chatToolsPolicyFilterActive(session *ChatSession) bool {
	return session != nil && !session.DisableTools && session.ToolPolicy != nil
}

// frozenSurfaceForScreen 读会话 actor 的稳定工具面（durable、与请求前缀一致）。
func frozenSurfaceForScreen(session *ChatSession) ([]runtimetypes.ToolDefinition, bool) {
	if session == nil || session.LocalRuntimeHost == nil || session.LocalRuntimeHost.SessionHub == nil {
		return nil, false
	}
	actor, err := session.LocalRuntimeHost.SessionHub.GetOrCreate(session.RuntimeSession.ID)
	if err != nil || actor == nil {
		return nil, false
	}
	state := actor.StateForInspection()
	if state == nil || !state.StableToolSurfaceSet || len(state.StableToolSurface) == 0 {
		return nil, false
	}
	return cloneStableSurfaceForScreen(state.StableToolSurface), true
}

func cloneStableSurfaceForScreen(surface []runtimetypes.ToolDefinition) []runtimetypes.ToolDefinition {
	cloned := make([]runtimetypes.ToolDefinition, len(surface))
	copy(cloned, surface)
	return cloned
}

func cloneToolInfoMetadata(input map[string]interface{}) map[string]interface{} {
	if input == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

// renderToolsScreenBody 纯渲染：账本分组 → 组内字母序，便于与 web 端点对读。
func renderToolsScreenBody(tools []runtimetypes.ToolDefinition) string {
	return fmt.Sprintf("当前会话工具面：%d 个工具（与 /api/runtime/tools 同源）\n", len(tools)) +
		renderToolsScreenGroups(tools)
}

// renderToolsScreenBodyPolicyFiltered 渲染"冻结面缺失、按会话策略过滤后"的下一
// turn 请求面：显式标注数据源口径（避免与冻结面/基线口径混淆），并列出被策略
// 裁剪掉的名字（T3 可解释：这个工具为什么不在下一轮请求里）。
func renderToolsScreenBodyPolicyFiltered(tools []runtimetypes.ToolDefinition, blocked []string) string {
	var body strings.Builder
	body.WriteString(fmt.Sprintf("当前会话工具面：%d 个工具（下一 turn 生效面：基线候选按当前 profile/会话策略过滤）\n", len(tools)))
	body.WriteString("  说明: 稳定工具面尚未冻结（刚切换 profile 或尚未开始 turn）；基线候选与 /api/runtime/tools 同源\n")
	if len(blocked) > 0 {
		body.WriteString(fmt.Sprintf("  策略裁剪: %d 个工具不进入下一 turn 请求面: %s\n",
			len(blocked), formatBlockedToolNames(blocked)))
	}
	body.WriteString(renderToolsScreenGroups(tools))
	return body.String()
}

// renderToolsScreenGroups 渲染账本分组正文（不含计数首行）。
func renderToolsScreenGroups(tools []runtimetypes.ToolDefinition) string {
	groups := make(map[string][]string)
	byName := make(map[string]string, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		source := "core"
		if tool.Metadata != nil {
			if raw, ok := tool.Metadata["surface_source"].(string); ok && strings.TrimSpace(raw) != "" {
				source = strings.TrimSpace(raw)
			}
		}
		groups[source] = append(groups[source], name)
		byName[name] = strings.TrimSpace(tool.Description)
	}

	sourceOrder := make([]string, 0, len(groups))
	for source := range groups {
		sourceOrder = append(sourceOrder, source)
	}
	sort.Strings(sourceOrder)

	var body strings.Builder
	for _, source := range sourceOrder {
		names := groups[source]
		sort.Strings(names)
		body.WriteString(fmt.Sprintf("\n[%s] ×%d\n", source, len(names)))
		for _, name := range names {
			if desc := byName[name]; desc != "" {
				body.WriteString(fmt.Sprintf("  • %s — %s\n", name, firstLine(desc)))
			} else {
				body.WriteString(fmt.Sprintf("  • %s\n", name))
			}
		}
	}
	return body.String()
}

// formatBlockedToolNames 折叠策略裁剪名单：超过上限截断并给出总数（明细不刷屏）。
func formatBlockedToolNames(names []string) string {
	const maxNames = 16
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	if len(sorted) > maxNames {
		return strings.Join(sorted[:maxNames], ", ") + fmt.Sprintf(" …（共 %d 个）", len(sorted))
	}
	return strings.Join(sorted, ", ")
}

func firstLine(text string) string {
	text = strings.TrimSpace(text)
	if index := strings.IndexAny(text, "\r\n"); index >= 0 {
		text = text[:index]
	}
	const maxDesc = 96
	if len([]rune(text)) > maxDesc {
		runes := []rune(text)
		text = string(runes[:maxDesc]) + "…"
	}
	return text
}

// chatToolsScreenSpec 映射 /tools：工具面目录 + 来源账本（与 web /api/runtime/tools 同源）。
func chatToolsScreenSpec(body string) chatScreenSpec {
	return chatScreenDocumentSpec("tools.screen", "工具面目录", textLinesDocument(strings.Split(body, "\n")))
}

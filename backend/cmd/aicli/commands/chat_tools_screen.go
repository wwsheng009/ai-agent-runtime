package commands

// /tools 会话内命令：当前会话工具面目录 + 来源账本
// （docs/architecture/unified-tool-surface-registry.md §14/§17 收官——TUI 侧观测面
// 与 web `/runtime/tools` 消费同一 toolsurface.Build 实现点，T1 的观测闭环）。
//
// 数据源优先级（与 web 端点同口径）：
//  1. 会话 actor 的 StableToolSurface（冻结面 = 真实请求前缀，turn 装配时已按
//     执行策略裁剪，天然含 profile 收窄）；
//  2. 兜底：runtime-server live 目录 / 本地 ToolSurface 目录经 toolsurface.Build
//     （无 actor / 未冻结时）。兜底分支按 session.ToolPolicy 过滤基线候选，显示
//     "下一 turn 将请求的面"——否则 /profile use 清掉冻结面后，展示反而不含新
//     profile 的裁剪（看起来像假开关或切完工具变多）。
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
			tools, listErr := serverExecutor.listRuntimeServerTools(context.Background(), session, session.RuntimeSession.ID)
			if listErr != nil {
				return fmt.Sprintf("工具面查询失败（runtime-server）: %v", listErr), true
			}
			if len(tools) == 0 {
				return "", false
			}
			return renderFallbackToolsScreenBody(session, tools, toolsurface.Ledger{}), true
		}
	}
	if host := session.LocalRuntimeHost; host != nil && host.ToolSurface != nil {
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
		return renderFallbackToolsScreenBody(session, catalog, toolsurface.Ledger{}), true
	}
	return "", false
}

// renderFallbackToolsScreenBody 渲染"冻结面缺失"时的兜底目录：先按当前会话策略
// 过滤（若有），再经 toolsurface.Build 标注来源账本。过滤后为空也要渲染
// （0 个工具 + 裁剪明细）：一个把工具全部收窄掉的 profile 必须可见，不能退回
// "没有可枚举的工具面"。
func renderFallbackToolsScreenBody(session *ChatSession, catalog []runtimetypes.ToolDefinition, ledger toolsurface.Ledger) string {
	filtered, blocked := filterToolsByChatPolicy(session, catalog)
	tools, ok := toolsurface.Build(filtered, ledger)
	if !ok {
		// 空目录（含策略收窄到 0）时不虚构账本，直接渲染过滤后的目录。
		tools = filtered
	}
	if !chatToolsPolicyFilterActive(session) {
		return renderToolsScreenBody(tools)
	}
	return renderToolsScreenBodyPolicyFiltered(tools, blocked)
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

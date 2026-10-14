package commands

// /tools 会话内命令：当前会话工具面目录 + 来源账本
// （docs/architecture/unified-tool-surface-registry.md §14/§17 收官——TUI 侧观测面
// 与 web `/runtime/tools` 消费同一 toolsurface.Build 实现点，T1 的观测闭环）。
//
// 数据源优先级（与 web 端点同口径）：
//  1. 会话 actor 的 StableToolSurface（冻结面 = 真实请求前缀）；
//  2. 兜底：本地 ToolSurface 目录经 toolsurface.Build（无 actor / 未冻结时）。
//
// 输出：按 surface_source 分组 + 计数行，标注数据与 web 端点一致。

import (
	"fmt"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolsurface"
	runtimetypes "github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// buildToolsScreenBody 渲染当前会话工具面目录（账本分组）。
// ok=false：会话没有可枚举的工具面（未附着 runtime / 无 actor / 无本地目录）。
func buildToolsScreenBody(session *ChatSession) (string, bool) {
	if session == nil {
		return "", false
	}
	if tools, ok := frozenSurfaceForScreen(session); ok {
		return renderToolsScreenBody(tools), true
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
		if tools, ok := toolsurface.Build(catalog, toolsurface.Ledger{}); ok {
			return renderToolsScreenBody(tools), true
		}
	}
	return "", false
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
	body.WriteString(fmt.Sprintf("当前会话工具面：%d 个工具（与 /api/runtime/tools 同源）\n", len(tools)))
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

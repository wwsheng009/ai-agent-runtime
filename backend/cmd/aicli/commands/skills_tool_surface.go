package commands

import (
	"fmt"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// mergedSkillToolSurface 把「会话工具管理器面」与「进程级 MCP 面」并成
// 一个 skill 可见的工具面。
//
// 为什么必须并集而不是二选一：skill 的 tools 依赖校验
// （skill.Loader.CheckSkill / skill.Registry.validate）在挂上 MCPManager 时
// 会要求每个声明的工具都能被 FindTool 找到，否则把该 skill 当作
// ErrToolNotRegistered **静默跳过**（只登记 unavailable）。而这两张面各自
// 只覆盖一半：
//
//   - 会话工具管理器面（tools.AgentAdapter）：覆盖本地 builtin toolkit
//     （bash/view/fetch/openai_image_generate/web_search…）+ 会话/本地 MCP；
//   - 进程级 MCP 面（skill.MCPAdapter over MCPManagerInstance）：**只有**
//     进程级配置链连上的 MCP 工具，一个 builtin 工具都没有。
//
// 此前 initSkillFunctions 在 MCPManagerInstance 非空时直接选后者，于是本地
// MCP 一开，所有声明 builtin 工具依赖的 skill（imagegen、run_shell_command、
// fetch_url_content、view_file_content…）会被整批静默丢弃——表现为「这些
// skill 不见了」，且只在开了本地 MCP 的会话里复现。
//
// 语义上工具「可用」= 任一面能解析到即算可用：两者的工具都是真实可调用的，
// 因此并集只会减少误判丢失，不会把不可用工具伪装成可用。
type mergedSkillToolSurface struct {
	primary   runtimeskill.MCPManager
	secondary runtimeskill.MCPManager
}

// FindTool 先查主面，主面没有再查副面；两面都没有时返回主面的原始错误，
// 保持既有失败语义（loader/registry 会把它包成 ErrToolNotRegistered 走
// unavailable 软跳过）。
func (m *mergedSkillToolSurface) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if m == nil {
		return runtimeskill.ToolInfo{}, fmt.Errorf("skill tool surface is not configured")
	}
	primaryErr := error(nil)
	if m.primary != nil {
		info, err := m.primary.FindTool(toolName)
		if err == nil {
			return info, nil
		}
		primaryErr = err
	}
	if m.secondary != nil {
		if info, err := m.secondary.FindTool(toolName); err == nil {
			return info, nil
		}
	}
	if primaryErr != nil {
		return runtimeskill.ToolInfo{}, primaryErr
	}
	return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

// CallTool 路由到真正持有该工具的那一面（与 FindTool 同序），避免把 builtin
// 工具调用转发给只有 MCP 工具的进程级面。
func (m *mergedSkillToolSurface) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if m == nil {
		return nil, fmt.Errorf("skill tool surface is not configured")
	}
	if m.primary != nil {
		if _, err := m.primary.FindTool(toolName); err == nil {
			return m.primary.CallTool(ctx, mcpName, toolName, args)
		}
	}
	if m.secondary != nil {
		if _, err := m.secondary.FindTool(toolName); err == nil {
			return m.secondary.CallTool(ctx, mcpName, toolName, args)
		}
	}
	return nil, fmt.Errorf("tool not found: %s", toolName)
}

// ListTools 按主面优先去重合并两面工具，供列表/统计投影使用。
func (m *mergedSkillToolSurface) ListTools() []runtimeskill.ToolInfo {
	if m == nil {
		return nil
	}
	seen := make(map[string]struct{})
	merged := make([]runtimeskill.ToolInfo, 0)
	for _, surface := range []runtimeskill.MCPManager{m.primary, m.secondary} {
		if surface == nil {
			continue
		}
		for _, info := range surface.ListTools() {
			if _, dup := seen[info.Name]; dup {
				continue
			}
			seen[info.Name] = struct{}{}
			merged = append(merged, info)
		}
	}
	return merged
}

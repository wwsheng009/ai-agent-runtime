package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	runtimeskill "github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

// skillToolSurfaceMCPName 是会话 skill 函数在工具面上的逻辑来源名。
const skillToolSurfaceMCPName = "aicli_skills"

// skillToolSurface 把会话函数目录里的 skill 函数接入执行侧工具面。
//
// 背景：模型工具调用的准入链路是「函数目录决定 schema/exposure → 运行时预检
// ToolSurface.FindTool → 策略 allowlist → mcpManager.CallTool 执行」。skill 函数
// 此前只登记在函数目录里，执行侧工具面对 skill__* 一律返回 not found，于是
// /skill 回合中模型一旦直接调用 skill 函数就会被判 TOOL_NOT_FOUND（回合
// success=false），只能靠读 SKILL.md 兜底。
//
// 这里对 skill__* 名字做按需解析与转发，执行复用函数目录的
// ExecuteFunctionWithMeta（与 /call、宿主既有执行链同一实现）。
//
// ListTools 故意保持基础工具面原样：skill 函数不进常驻清单，避免绕过
// skills-top-k 路由把所有技能塞给模型；执行授权由合成 policy allowlist 通过
// sessionSkillFunctionNames 补齐（含热加载后新增的技能）。
type skillToolSurface struct {
	session *ChatSession
	next    runtimeskill.MCPManager
}

func wrapSkillToolSurface(session *ChatSession, next runtimeskill.MCPManager) runtimeskill.MCPManager {
	if session == nil {
		return next
	}
	return &skillToolSurface{session: session, next: next}
}

func (s *skillToolSurface) FindTool(toolName string) (runtimeskill.ToolInfo, error) {
	if s.next != nil {
		// 只把「有名字的命中」视为找到：部分 surface 实现（含测试桩）对未知
		// 工具返回零值 + nil error，不能据此吞掉后续的 skill 解析与报错。
		if info, err := s.next.FindTool(toolName); err == nil && strings.TrimSpace(info.Name) != "" {
			return info, nil
		}
	}
	if fn := s.skillFunction(toolName); fn != nil {
		return skillFunctionToolInfo(fn), nil
	}
	return runtimeskill.ToolInfo{}, fmt.Errorf("tool not found: %s", toolName)
}

func (s *skillToolSurface) ListTools() []runtimeskill.ToolInfo {
	if s.next == nil {
		return nil
	}
	return s.next.ListTools()
}

func (s *skillToolSurface) CallTool(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, error) {
	if isSkillToolSurfaceName(mcpName) {
		output, _, err := s.executeSkillTool(ctx, toolName, args)
		return output, err
	}
	if s.next == nil {
		return nil, fmt.Errorf("tool surface is not configured: %s", toolName)
	}
	return s.next.CallTool(ctx, mcpName, toolName, args)
}

// CallToolWithMeta 走 richToolCaller 路径时保留函数目录返回的 metadata。
func (s *skillToolSurface) CallToolWithMeta(ctx interface{}, mcpName, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	if isSkillToolSurfaceName(mcpName) {
		return s.executeSkillTool(ctx, toolName, args)
	}
	if rich, ok := s.next.(interface {
		CallToolWithMeta(interface{}, string, string, map[string]interface{}) (interface{}, map[string]interface{}, error)
	}); ok {
		return rich.CallToolWithMeta(ctx, mcpName, toolName, args)
	}
	output, err := s.CallTool(ctx, mcpName, toolName, args)
	return output, nil, err
}

func (s *skillToolSurface) executeSkillTool(ctx interface{}, toolName string, args map[string]interface{}) (interface{}, map[string]interface{}, error) {
	catalog := s.session.FunctionCatalog
	if catalog == nil {
		return nil, nil, fmt.Errorf("skill function catalog is not configured")
	}
	execCtx, _ := ctx.(context.Context)
	if execCtx == nil {
		execCtx = context.Background()
	}
	output, meta, err := catalog.ExecuteFunctionWithMeta(execCtx, toolName, args)
	if err != nil {
		return nil, meta, err
	}
	return output, meta, nil
}

// skillFunction 只在函数目录里解析「可执行的」skill 函数：文档型/未配置执行器
// 的条目仍留在目录（/functions 可读），但不会被工具面认领。
func (s *skillToolSurface) skillFunction(name string) *SkillFunction {
	if s == nil || s.session == nil {
		return nil
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil
	}
	catalog := s.session.FunctionCatalog
	if catalog == nil {
		return nil
	}
	entry := catalog.entries[name]
	if entry == nil || !entry.isSkill {
		return nil
	}
	fn, _ := entry.fn.(*SkillFunction)
	if fn == nil || fn.executor == nil {
		return nil
	}
	return fn
}

func isSkillToolSurfaceName(mcpName string) bool {
	return strings.EqualFold(strings.TrimSpace(mcpName), skillToolSurfaceMCPName)
}

func skillFunctionToolInfo(fn *SkillFunction) runtimeskill.ToolInfo {
	if fn == nil {
		return runtimeskill.ToolInfo{}
	}
	return runtimeskill.ToolInfo{
		Name:        fn.Name(),
		Description: fn.Description(),
		InputSchema: fn.Parameters(),
		MCPName:     skillToolSurfaceMCPName,
		Enabled:     true,
	}
}

// sessionSkillFunctionNames 返回当前会话函数目录里可执行的 skill 函数名。
//
// 合成 policy allowlist 用它把 skill 函数纳入执行授权（含热加载后新增项，
// 见 syncLocalChatToolPolicyAllowlist）；是否暴露给模型仍由函数目录的
// exposure 选择（skills-top-k / pin）决定，两者互不影响。
func sessionSkillFunctionNames(session *ChatSession) []string {
	if session == nil {
		return nil
	}
	catalog := session.FunctionCatalog
	if catalog == nil {
		return nil
	}
	names := make([]string, 0, len(catalog.entries))
	for name, entry := range catalog.entries {
		if entry == nil || !entry.isSkill {
			continue
		}
		fn, ok := entry.fn.(*SkillFunction)
		if !ok || fn == nil || fn.executor == nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

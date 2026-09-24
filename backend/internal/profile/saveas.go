package profile

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// SaveAsSurface 是"从会话固化"（§23 G1/D24，Batch 13 slice 5）的可声明面：
// 只承载 profile.yaml 能表达的差分字段（工具面 / skills / MCP）。
//
// 纪律（D35）：会话里存在、但 profile.yaml 无字段可表达的生效项（prompt、
// 权限模式、skills 目录）**绝不静默丢弃**——由调用方在报告中逐项提示。
// 差分口径：只写"与内置默认面不同"的声明（空 allowlist = 全量、空 denylist =
// 不排除、read_only 默认 false），基线演进时新工具/新技能自动继承，不静默过时。
type SaveAsSurface struct {
	ToolAllowlist     []string
	ToolDenylist      []string
	ReadOnly          bool
	SkillAllowlist    []string
	SkillDenylist     []string
	MCPUseServers     []string
	MCPExcludeServers []string
}

// Empty 判定"无差分"：没有任何需要写盘的声明。A9 要求此时必须报错且不产 profile。
func (s SaveAsSurface) Empty() bool {
	return len(s.ToolAllowlist) == 0 &&
		len(s.ToolDenylist) == 0 &&
		!s.ReadOnly &&
		len(s.SkillAllowlist) == 0 &&
		len(s.SkillDenylist) == 0 &&
		len(s.MCPUseServers) == 0 &&
		len(s.MCPExcludeServers) == 0
}

// saveAsDocument 是 save-as 的输出模型：字段类型与 ProfileSpec 同源（不引入第二套
// 方言），只把 section 换成指针以便"无差分即不落 section"（yaml.v3 对 struct 的
// omitempty 不生效，直接 marshal ProfileSpec 会写出 runtime: {} / providers: {}）。
type saveAsDocument struct {
	Profile ProfileMetaSpec `yaml:"profile"`
	Tools   *ToolPolicySpec `yaml:"tools,omitempty"`
	Skills  *SkillsSpec     `yaml:"skills,omitempty"`
	MCP     *MCPSpec        `yaml:"mcp,omitempty"`
	// Agents 只放一个**空声明**：解析器要求 profile 必须能解析出 agent
	// （ErrAgentUnresolved："explicit agent or profile.default_agent is required"），
	// 而空声明不携带任何基线内容（无 prompt/tools 覆盖），不违反"仅含差分"。
	Agents map[string]AgentSpec `yaml:"agents,omitempty"`
}

// RenderSaveAsProfile 把差分面渲染为可直接解析的 profile.yaml 字节。
// 输出是纯函数：同一 surface 恒得同一字节（便于测试与 diff）。
// agent 是会话当前生效的 agent id（缺省用内置默认 agent）：只声明、不固化其内容。
func RenderSaveAsProfile(name, description, agent string, surface SaveAsSurface) ([]byte, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, fmt.Errorf("profile 名称不能为空")
	}
	if surface.Empty() {
		return nil, fmt.Errorf("差分面为空：没有可写入 profile.yaml 的声明")
	}
	agent = strings.TrimSpace(agent)
	if agent == "" {
		return nil, fmt.Errorf("agent id 不能为空（profile 必须能解析出 agent）")
	}
	doc := saveAsDocument{
		Profile: ProfileMetaSpec{Name: name, Description: strings.TrimSpace(description), DefaultAgent: agent},
		Agents:  map[string]AgentSpec{agent: {}},
	}
	if len(surface.ToolAllowlist) > 0 || len(surface.ToolDenylist) > 0 || surface.ReadOnly {
		tools := &ToolPolicySpec{
			Allowlist: saveAsSortedNames(surface.ToolAllowlist),
			Denylist:  saveAsSortedNames(surface.ToolDenylist),
		}
		if surface.ReadOnly {
			readOnly := true
			tools.ReadOnly = &readOnly
		}
		doc.Tools = tools
	}
	if len(surface.SkillAllowlist) > 0 || len(surface.SkillDenylist) > 0 {
		doc.Skills = &SkillsSpec{
			Allowlist: saveAsSortedNames(surface.SkillAllowlist),
			Denylist:  saveAsSortedNames(surface.SkillDenylist),
		}
	}
	if len(surface.MCPUseServers) > 0 || len(surface.MCPExcludeServers) > 0 {
		doc.MCP = &MCPSpec{
			UseServers:     saveAsSortedNames(surface.MCPUseServers),
			ExcludeServers: saveAsSortedNames(surface.MCPExcludeServers),
		}
	}
	body, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("渲染 profile.yaml 失败: %w", err)
	}
	header := strings.Join([]string{
		fmt.Sprintf("# %s — 由 `/profile save-as` 从当前会话固化（D24 差分固化）。", name),
		"# 只写与内置默认面不同的声明：基线新增工具/技能会自动继承，不会静默过时。",
		"# 未包含 prompt（会话 prompt 可能来自临时上下文）；需要时另行补充。",
		"",
	}, "\n")
	return []byte(header + string(body)), nil
}

// saveAsSortedNames 归一化名称列表：去空白、剔除空串、大小写不敏感去重、升序。
// 顺序稳定是"同一 surface 恒得同一字节"的前提，也让产物的 diff 可读。
func saveAsSortedNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

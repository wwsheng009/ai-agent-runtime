package skills

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentdef"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// buildRuntimeProfileViewGroups 组装 UI 编辑所需的分组视图（Batch 8 前端契约，
// 对应设计 §10.5「解析后视图」）。
//
// 纪律（R10 单一权威）：所有**只读**生效面（effective/excluded/sources/dirs/
// servers/permission_mode）都在这里由后端推导，前端只展示，不复刻解析语义；
// 可写字段一律给出 profile.yaml 里的原始声明（allowlist/denylist/…），
// 命名与 profile schema 一致（不引入第二套方言）。
func buildRuntimeProfileViewGroups(
	target runtimeProfileTarget,
	spec *profilesys.ProfileSpec,
	resolved *profilesys.ResolvedAgent,
) map[string]interface{} {
	if spec == nil {
		spec = &profilesys.ProfileSpec{}
	}
	agentID := ""
	if resolved != nil {
		agentID = strings.TrimSpace(resolved.AgentID)
	}
	if agentID == "" {
		agentID = strings.TrimSpace(spec.Profile.DefaultAgent)
	}

	// 工具面：profile 顶层声明可写；agent 级声明与合并后生效面只读。
	tools := map[string]interface{}{
		"allowlist":       profileViewStrings(spec.Tools.Allowlist),
		"denylist":        profileViewStrings(spec.Tools.Denylist),
		"agent_id":        agentID,
		"agent_allowlist": []string{},
		"agent_denylist":  []string{},
		"effective":       []string{},
		"excluded":        []string{},
		"sources":         []string{},
	}
	if inline, ok := spec.Agent(agentID); ok {
		tools["agent_allowlist"] = profileViewStrings(inline.Tools.Allowlist)
		tools["agent_denylist"] = profileViewStrings(inline.Tools.Denylist)
	}
	if resolved != nil {
		tools["effective"] = profileViewStrings(resolved.ToolPolicy.Allowlist)
		tools["excluded"] = profileViewStrings(resolved.ToolPolicy.Denylist)
		tools["sources"] = profileViewStrings(resolved.ToolPolicy.Sources)
		if resolved.ToolPolicy.ReadOnly != nil {
			tools["read_only"] = *resolved.ToolPolicy.ReadOnly
		}
	}

	// Skills：allow/deny 可写；目录与曝光参数来自解析结果 / runtime.overrides（只读）。
	skills := map[string]interface{}{
		"allowlist": profileViewStrings(spec.Skills.Allowlist),
		"denylist":  profileViewStrings(spec.Skills.Denylist),
		"dirs":      []string{},
	}
	if resolved != nil {
		skills["dirs"] = profileViewStrings(resolved.SkillDirs)
	}
	exposureMode, topK := runtimeSkillsExposureFromOverrides(spec.Runtime.Overrides)
	skills["exposure_mode"] = exposureMode
	skills["top_k"] = topK

	// MCP：use/exclude 可写；servers 为 use/exclude 之后的连接选择（只读）。
	servers := buildRuntimeProfileMCPServers(resolved)
	usedCount := 0
	for _, server := range servers {
		if used, ok := server["used"].(bool); ok && used {
			usedCount++
		}
	}
	mcp := map[string]interface{}{
		"use_servers":     profileViewStrings(spec.MCP.UseServers),
		"exclude_servers": profileViewStrings(spec.MCP.ExcludeServers),
		"servers":         servers,
		"server_count":    len(servers),
		"used_count":      usedCount,
	}

	// Prompts：只有 mode 可写；三个 prompt 文件路径由解析结果给出（只读）。
	promptMode := strings.TrimSpace(spec.Prompts.Mode)
	if resolved != nil && strings.TrimSpace(resolved.PromptMode) != "" {
		promptMode = strings.TrimSpace(resolved.PromptMode)
	}
	if promptMode == "" {
		promptMode = "replace"
	}
	prompts := map[string]interface{}{"mode": promptMode}
	systemPath, rolePath, toolsPath := "", "", ""
	if resolved != nil {
		systemPath = strings.TrimSpace(resolved.Prompts.System)
		rolePath = strings.TrimSpace(resolved.Prompts.Role)
		toolsPath = strings.TrimSpace(resolved.Prompts.Tools)
	}
	prompts["system"] = promptLayerView(systemPath)
	prompts["role"] = promptLayerView(rolePath)
	prompts["tools"] = promptLayerView(toolsPath)

	// Agents：default_agent 可写；清单来自 profile.yaml 的 agents 节（V7）。
	agentIDs := make([]string, 0, len(spec.Agents))
	for id := range spec.Agents {
		if strings.TrimSpace(id) != "" {
			agentIDs = append(agentIDs, id)
		}
	}
	sort.Strings(agentIDs)
	entries := make([]map[string]interface{}, 0, len(agentIDs))
	for _, id := range agentIDs {
		inline := spec.Agents[id]
		entries = append(entries, map[string]interface{}{
			"id":       id,
			"provider": strings.TrimSpace(inline.Provider),
			"model":    strings.TrimSpace(inline.Model),
			"tools": map[string]interface{}{
				"allowlist": profileViewStrings(inline.Tools.Allowlist),
				"denylist":  profileViewStrings(inline.Tools.Denylist),
			},
		})
	}
	agents := map[string]interface{}{
		"default_agent": strings.TrimSpace(spec.Profile.DefaultAgent),
		"available":     agentIDs,
		"entries":       entries,
	}

	// Overrides：keys/origins 保持既有结构，另附逐键 entries（值文本 + 是否放行）。
	overrides := buildRuntimeProfileOverrides(spec)
	overrides["entries"] = buildRuntimeProfileOverrideEntries(spec)

	return map[string]interface{}{
		"tools":       tools,
		"skills":      skills,
		"mcp":         mcp,
		"prompts":     prompts,
		"agents":      agents,
		"overrides":   overrides,
		"preferences": buildRuntimeProfilePreferences(target, spec, resolved, agentID),
	}
}

// buildRuntimeProfilePreferences 给出「偏好与审批」只读面：权限模式走 agentdef
// 权威解析（D17 来源标注 + D16 bypass 约束由 BuildBinding 统一执行），
// provider/model 优先取解析结果，缺失时回落 agentdef。
func buildRuntimeProfilePreferences(
	target runtimeProfileTarget,
	spec *profilesys.ProfileSpec,
	resolved *profilesys.ResolvedAgent,
	agentID string,
) map[string]interface{} {
	preferences := map[string]interface{}{
		"permission_mode":        "",
		"permission_mode_source": "",
		"provider":               "",
		"model":                  "",
	}
	if resolved != nil {
		preferences["provider"] = strings.TrimSpace(resolved.Provider)
		preferences["model"] = strings.TrimSpace(resolved.Model)
	}
	if agentID == "" {
		return preferences
	}
	def, err := agentdef.Resolve(agentID, agentdef.DiscoverOptions{
		ProfileRoot: strings.TrimSpace(target.Root),
	})
	if err != nil || def == nil {
		return preferences
	}
	source := strings.TrimSpace(def.SourcePath)
	if source == "" {
		source = string(def.Source)
	}
	preferences["permission_mode_source"] = source
	if binding, err := agentdef.BuildBinding(def); err == nil && binding != nil {
		// BuildBinding 是 D16 的单一执行点（profile 通道不得默认 bypass）。
		preferences["permission_mode"] = string(binding.PermissionMode)
		if provider := strings.TrimSpace(binding.Provider); provider != "" {
			preferences["provider"] = provider
		}
		if model := strings.TrimSpace(binding.Model); model != "" {
			preferences["model"] = model
		}
		return preferences
	}
	// spec 未显式声明时的兜底：只用原始声明，不猜枚举。
	preferences["permission_mode"] = strings.TrimSpace(def.PermissionMode)
	return preferences
}

// buildRuntimeProfileOverrideEntries 展开 runtime.overrides 的叶子键：
// 值文本（供 UI 编辑）+ origin（profile）+ allowed（D14 白名单逐键判定）。
func buildRuntimeProfileOverrideEntries(spec *profilesys.ProfileSpec) []map[string]interface{} {
	entries := make([]map[string]interface{}, 0, 4)
	if spec == nil || len(spec.Runtime.Overrides) == 0 {
		return entries
	}
	keys := profilesys.OverrideKeyList(spec.Runtime.Overrides)
	origins := profilesys.OverrideOrigins(spec.Runtime.Overrides)
	for _, key := range keys {
		value := overrideLeafValue(spec.Runtime.Overrides, key)
		origin := origins[key]
		if origin == "" {
			origin = profilesys.OverrideOriginProfile
		}
		entries = append(entries, map[string]interface{}{
			"key":     key,
			"value":   overrideValueText(value),
			"origin":  origin,
			"allowed": overrideKeyAllowed(key, value),
		})
	}
	return entries
}

// overrideLeafValue 按点分键路径取出叶子值（键路径由 OverrideKeyList 生成，
// 一定存在；`{}` 叶子返回空映射本身，与 keys 口径一致）。
func overrideLeafValue(overrides map[string]interface{}, key string) interface{} {
	var current interface{} = overrides
	for _, part := range strings.Split(key, ".") {
		section, ok := current.(map[string]interface{})
		if !ok {
			return nil
		}
		current, ok = section[part]
		if !ok {
			return nil
		}
	}
	return current
}

// overrideKeyAllowed 用 D14 的同一校验器判定单个键是否放行（不另立白名单）。
func overrideKeyAllowed(key string, value interface{}) bool {
	parts := strings.Split(key, ".")
	nested := map[string]interface{}{}
	cursor := nested
	for index, part := range parts {
		if index == len(parts)-1 {
			cursor[part] = value
			break
		}
		next := map[string]interface{}{}
		cursor[part] = next
		cursor = next
	}
	for _, issue := range profilesys.ValidateOverrides(nested) {
		if issue.Severity == profilesys.ProfileSpecIssueError {
			return false
		}
	}
	return true
}

// overrideValueText 把叶子值渲染成可编辑文本（标量直出，结构体走 JSON）。
func overrideValueText(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return fmt.Sprintf("%t", typed)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprintf("%d", typed)
	case float32, float64:
		return fmt.Sprintf("%v", typed)
	default:
		if data, err := json.Marshal(typed); err == nil {
			return string(data)
		}
		return fmt.Sprintf("%v", typed)
	}
}

// runtimeSkillsExposureFromOverrides 读取 runtime.overrides 里的 skills 曝光参数
// （键在 D14 白名单内：skills_runtime.aicli_skill_exposure_mode/_top_k）。
func runtimeSkillsExposureFromOverrides(overrides map[string]interface{}) (string, int) {
	section, ok := overrides["skills_runtime"].(map[string]interface{})
	if !ok {
		return "", 0
	}
	mode := ""
	if raw, ok := section["aicli_skill_exposure_mode"].(string); ok {
		mode = strings.TrimSpace(raw)
	}
	topK := 0
	switch typed := section["aicli_skill_exposure_top_k"].(type) {
	case int:
		topK = typed
	case int64:
		topK = int(typed)
	case float64:
		topK = int(typed)
	}
	return mode, topK
}

// promptLayerView 描述单个 prompt 层（路径 + 是否存在，供 UI 标注只读文件）。
func promptLayerView(path string) map[string]interface{} {
	exists := false
	if path != "" {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			exists = true
		}
	}
	return map[string]interface{}{"path": path, "exists": exists}
}

// profileViewStrings 保证 JSON 里是稳定的数组（缺省空数组而不是 null）。
func profileViewStrings(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return values
}

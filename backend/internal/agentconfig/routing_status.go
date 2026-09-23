package agentconfig

import (
	"sort"
	"strings"
	"time"
)

// 路由只读投影（方案 §6.1：单一投影函数，防四处漂移）。
//
// 所有展示面——TUI 状态栏段、/routing show、/routing doctor、micro web client
// 状态栏快照、frontend 会话详情路由区块、GET /api/runtime/sessions/{id}/routing
// ——只消费本文件产出的投影，禁止各自拼装字段。runtime server 侧直接以本结构
// 的 snake_case JSON 序列化（§6.5），TUI 与 web 字段一一对应。

// RoutingStatusSchemaVersion 是投影 JSON 的 schema 版本（§6.1/§6.5）。
const RoutingStatusSchemaVersion = 1

// RoutingEffectiveFromNextTurn 是写入生效时机的固定值（§4.5：下一 turn 生效）。
const RoutingEffectiveFromNextTurn = "next_turn"

// RoutingStatusProjection 是 §6.1 定义的唯一投影结构。
type RoutingStatusProjection struct {
	SchemaVersion int      `json:"schema_version"`
	Enabled       bool     `json:"enabled"`
	Level         string   `json:"level"`     // 当前 turn 生效档位（未判定时为空）
	Provider      string   `json:"provider"`  // 该档位生效 provider（未配置时为空）
	Model         string   `json:"model"`     // 该档位生效 model
	Reasoning     string   `json:"reasoning"` // 该档位生效 reasoning_effort
	Source        string   `json:"source"`    // session|workspace|config|default|derived
	Disabled      bool     `json:"disabled"`
	Warnings      []string `json:"warnings"`
	Revision      string   `json:"revision"` // §3.4 UpdatedAt/UpdatedBy 投影
	EffectiveFrom string   `json:"effective_from"`
}

// RoutingLevelSummary 是面板逐级表格的一行（§5.3/§5.7：level × provider/model/effort）。
type RoutingLevelSummary struct {
	Level     string `json:"level"`
	Enabled   bool   `json:"enabled"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	Source    string `json:"source,omitempty"`
	Expensive bool   `json:"expensive,omitempty"`
}

// RoutingPanelMetadata 是 GET /routing 附加的面板元数据（§4.4：各层徽标/可写性/
// 目标文件路径，§5.4 写入层选择器）。
type RoutingPanelMetadata struct {
	Scope              string                   `json:"scope"` // main | sub
	ChildSession       bool                     `json:"child_session"`
	SessionOverride    bool                     `json:"session_override"`
	WorkspaceOverride  bool                     `json:"workspace_override"`
	ConfigOverride     bool                     `json:"config_override"`
	WorkspacePath      string                   `json:"workspace_path,omitempty"`
	WorkspacePrefsPath string                   `json:"workspace_prefs_path,omitempty"`
	ConfigPath         string                   `json:"config_path,omitempty"`
	ConfigLayer        string                   `json:"config_layer,omitempty"`
	WritableLayers     []string                 `json:"writable_layers"`
	Levels             []RoutingLevelSummary    `json:"levels,omitempty"`
	SubAgent           *RoutingStatusProjection `json:"sub_agent,omitempty"`
}

// ProjectRoutingStatus 把解析结果投影为 §6.1 结构。
//
// level 为当前 turn 生效档位（调用方已知时传入，未判定时传空串：此时用
// default_difficulty 兜底，仍为空则只显示 baseline）。revision 来自会话覆盖的
// UpdatedAt/UpdatedBy（§3.4），无覆盖时为空串。
func ProjectRoutingStatus(res RoutingResolution, level, revision string) RoutingStatusProjection {
	proj := RoutingStatusProjection{
		SchemaVersion: RoutingStatusSchemaVersion,
		EffectiveFrom: RoutingEffectiveFromNextTurn,
		Warnings:      RoutingWarningsToStrings(res.Warnings),
		Revision:      strings.TrimSpace(revision),
		Source:        string(RoutingSourceDefault),
	}
	effective := res.Effective
	if effective == nil || !effective.Enabled {
		proj.Disabled = true
		proj.Level = ""
		return proj
	}
	proj.Enabled = true
	level = normalizeRoutingLevelForDisplay(level)
	if level == "" {
		level = normalizeRoutingLevelForDisplay(effective.DefaultDifficulty)
	}
	proj.Level = level
	if profile, ok := effective.Profiles[level]; ok {
		proj.Provider = strings.TrimSpace(profile.Provider)
		proj.Model = strings.TrimSpace(profile.Model)
		proj.Reasoning = strings.TrimSpace(profile.ReasoningEffort)
	}
	if source := routingLevelSource(res.Sources, level); source != "" {
		proj.Source = string(source)
	}
	return proj
}

// ProjectSubAgentRoutingStatus 是子 Agent 侧的同一投影（§5.6：sub 作用域）。
func ProjectSubAgentRoutingStatus(res RoutingResolution, level, revision string) RoutingStatusProjection {
	proj := RoutingStatusProjection{
		SchemaVersion: RoutingStatusSchemaVersion,
		EffectiveFrom: RoutingEffectiveFromNextTurn,
		Warnings:      RoutingWarningsToStrings(res.Warnings),
		Revision:      strings.TrimSpace(revision),
		Source:        string(RoutingSourceDefault),
	}
	effective := res.EffectiveSub
	if effective == nil || (effective.Enabled != nil && !*effective.Enabled) {
		proj.Disabled = true
		return proj
	}
	proj.Enabled = true
	level = normalizeRoutingLevelForDisplay(level)
	if level == "" {
		level = normalizeRoutingLevelForDisplay(effective.DefaultDifficulty)
	}
	proj.Level = level
	if profile, ok := effective.Levels[level]; ok {
		proj.Provider = strings.TrimSpace(profile.Provider)
		proj.Model = strings.TrimSpace(profile.Model)
		proj.Reasoning = strings.TrimSpace(profile.ReasoningEffort)
	}
	if source := routingSubLevelSource(res.SubSources, level); source != "" {
		proj.Source = string(source)
	}
	return proj
}

// BuildRoutingLevelSummaries 产出逐级表格行（面板/`/routing show` 共用）。
// scope = "main" 时用主 Agent 的 levels + profiles，scope = "sub" 时用子 Agent 的
// levels map 键集合。档位顺序：内置四档优先，其余按字典序追加。
func BuildRoutingLevelSummaries(res RoutingResolution, scope string) []RoutingLevelSummary {
	if strings.EqualFold(strings.TrimSpace(scope), "sub") {
		effective := res.EffectiveSub
		if effective == nil {
			return nil
		}
		levels := orderedRoutingLevels(routingLevelKeys(effective.Levels))
		out := make([]RoutingLevelSummary, 0, len(levels))
		for _, level := range levels {
			row := RoutingLevelSummary{Level: level, Enabled: effective.Enabled == nil || *effective.Enabled}
			if profile, ok := effective.Levels[level]; ok {
				row.Provider = strings.TrimSpace(profile.Provider)
				row.Model = strings.TrimSpace(profile.Model)
				row.Reasoning = strings.TrimSpace(profile.ReasoningEffort)
			}
			if source := routingSubLevelSource(res.SubSources, level); source != "" {
				row.Source = string(source)
			}
			out = append(out, row)
		}
		return out
	}
	effective := res.Effective
	if effective == nil {
		return nil
	}
	levels := orderedRoutingLevels(effective.Levels)
	expensive := make(map[string]struct{}, len(effective.ExpensiveLevels))
	for _, level := range effective.ExpensiveLevels {
		expensive[normalizeRoutingLevelForDisplay(level)] = struct{}{}
	}
	out := make([]RoutingLevelSummary, 0, len(levels))
	for _, level := range levels {
		row := RoutingLevelSummary{Level: level, Enabled: true}
		if _, ok := expensive[level]; ok {
			row.Expensive = true
		}
		if profile, ok := effective.Profiles[level]; ok {
			row.Provider = strings.TrimSpace(profile.Provider)
			row.Model = strings.TrimSpace(profile.Model)
			row.Reasoning = strings.TrimSpace(profile.ReasoningEffort)
		}
		if source := routingLevelSource(res.Sources, level); source != "" {
			row.Source = string(source)
		}
		out = append(out, row)
	}
	return out
}

// RoutingWarningsToStrings 把 warning 结构压成展示字符串（§6.1 的 Warnings 字段）。
func RoutingWarningsToStrings(warnings []RoutingWarning) []string {
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		field := strings.TrimSpace(warning.Field)
		reason := strings.TrimSpace(warning.Reason)
		text := field
		if reason != "" {
			if text == "" {
				text = reason
			} else {
				text = text + ": " + reason
			}
		}
		if warning.FallbackTo != "" {
			text = text + " (fallback: " + string(warning.FallbackTo) + ")"
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		out = append(out, text)
	}
	return out
}

// FormatRoutingRevision 把会话覆盖的写入端信息投影为 revision（§3.4 M11）。
func FormatRoutingRevision(updatedAt time.Time, updatedBy string) string {
	by := strings.TrimSpace(updatedBy)
	if updatedAt.IsZero() {
		return by
	}
	stamp := updatedAt.UTC().Format(time.RFC3339)
	if by == "" {
		return stamp
	}
	return stamp + "@" + by
}

// routingLevelSource 取主 Agent 某档位的来源：优先 model 字段，其次 provider /
// reasoning_effort，最后 default_difficulty；都没有时返回空（调用方回落 default）。
func routingLevelSource(sources map[string]RoutingSource, level string) RoutingSource {
	return routingLevelSourceFor(sources, "main_agent.", "profiles", level)
}

// routingSubLevelSource 取子 Agent 某档位的来源（键空间 sub_agent.levels.*，§3.5.1）。
func routingSubLevelSource(sources map[string]RoutingSource, level string) RoutingSource {
	return routingLevelSourceFor(sources, "sub_agent.", "levels", level)
}

// routingLevelSourceFor 按作用域键空间取档位来源（§3.5.1 键名表）：
// 主 Agent 是 main_agent.profiles.<level>.<field>，子 Agent 是
// sub_agent.levels.<level>.<field>；default_difficulty 用同一前缀。
//
// 2026-09-22 复审修正：此前子 Agent 侧复用主 Agent 的键前缀，键空间不匹配导致
// ProjectSubAgentRoutingStatus 的 source 恒回落 default、逐级表格的 source 恒为空
// ——展示来源与实际解析结果不一致（I-6/I-8）。
func routingLevelSourceFor(sources map[string]RoutingSource, prefix, section, level string) RoutingSource {
	if sources == nil {
		return ""
	}
	if level != "" {
		for _, field := range []string{"model", "provider", "reasoning_effort"} {
			if source, ok := sources[prefix+section+"."+level+"."+field]; ok && source != "" {
				return source
			}
		}
	}
	if source, ok := sources[prefix+"default_difficulty"]; ok && source != "" {
		return source
	}
	return ""
}

func routingLevelKeys[T any](levels map[string]T) []string {
	keys := make([]string, 0, len(levels))
	for level := range levels {
		keys = append(keys, normalizeRoutingLevelForDisplay(level))
	}
	return keys
}

// normalizeRoutingLevelForDisplay 归一化档位用于展示与查表：内置四档走配置包的
// 既有归一化口径（config.go 的 normalizeSubagentDifficulty），其余档位按小写原样
// 保留（自定义档位不得因展示层归一化而消失）。
func normalizeRoutingLevelForDisplay(level string) string {
	level = strings.TrimSpace(level)
	if level == "" {
		return ""
	}
	if normalized, ok := normalizeSubagentDifficulty(level); ok {
		return normalized
	}
	return strings.ToLower(level)
}

// orderedRoutingLevels 归一化并排序档位：内置四档（easy/normal/hard/expert）优先，
// 其余档位按字典序追加；重复项只保留一次。
func orderedRoutingLevels(levels []string) []string {
	seen := make(map[string]struct{}, len(levels))
	normalized := make([]string, 0, len(levels))
	for _, level := range levels {
		level = normalizeRoutingLevelForDisplay(level)
		if level == "" {
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		normalized = append(normalized, level)
	}
	out := make([]string, 0, len(normalized))
	for _, builtin := range []string{"easy", "normal", "hard", "expert"} {
		if _, ok := seen[builtin]; ok {
			out = append(out, builtin)
			delete(seen, builtin)
		}
	}
	rest := make([]string, 0, len(seen))
	for level := range seen {
		rest = append(rest, level)
	}
	sort.Strings(rest)
	return append(out, rest...)
}

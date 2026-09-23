package agentconfig

import (
	"reflect"
	"strings"
	"time"
)

// 会话/工作区覆盖的写入语义（方案 §3.2/§3.5.2/§5.4）。
//
// 本文件只做「补丁 → 覆盖结构」的纯函数变换：字段级合并、键路径清除、
// 覆盖物化到配置结构（workspace/config 层写入用）。校验与阶梯回退不在这里，
// 由 ResolveMainAgentRouting / ResolveSubagentRouting 统一完成（§4.1）。

// SessionRoutingPatch 是 PATCH /routing 的补丁体（§3.2 结构 + 清除语义）。
//
// 字段级语义：非 nil 字段覆盖既有值，nil 字段保持既有值不变（不是清除）；
// 清除走 ClearFields（§3.5.1 键路径）或整体 Clear。
type SessionRoutingPatch struct {
	MainAgent   *AICLISessionMainAgentRoutingOverride `json:"main_agent,omitempty"`
	SubAgent    *AICLISessionSubAgentRoutingOverride  `json:"sub_agent,omitempty"`
	ClearFields []string                              `json:"clear_fields,omitempty"`
	UpdatedBy   string                                `json:"updated_by,omitempty"`
}

// HasContent 报告补丁是否携带任何写入内容（空补丁等价于清除，见 §4.5）。
func (p *SessionRoutingPatch) HasContent() bool {
	if p == nil {
		return false
	}
	if p.MainAgent != nil || p.SubAgent != nil {
		return true
	}
	return len(p.ClearFields) > 0
}

// MergeSessionRoutingOverride 把补丁合并到既有覆盖上，返回新结构（不原地改写
// base）。合并后为空（主/子都为 nil）时返回 nil，调用方据此删除 context 键。
func MergeSessionRoutingOverride(base *AICLISessionRoutingOverride, patch *SessionRoutingPatch, now time.Time, updatedBy string) *AICLISessionRoutingOverride {
	var merged AICLISessionRoutingOverride
	if base != nil {
		merged = *base
		merged.MainAgent = MergeMainAgentOverride(nil, base.MainAgent)
		merged.SubAgent = MergeSubAgentOverride(nil, base.SubAgent)
	}
	if patch != nil {
		if patch.MainAgent != nil {
			merged.MainAgent = MergeMainAgentOverride(merged.MainAgent, patch.MainAgent)
		}
		if patch.SubAgent != nil {
			merged.SubAgent = MergeSubAgentOverride(merged.SubAgent, patch.SubAgent)
		}
	}
	ApplySessionRoutingClearFields(&merged, patchClearFields(patch))
	if merged.MainAgent == nil && merged.SubAgent == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	merged.UpdatedAt = now.UTC()
	if by := strings.TrimSpace(updatedBy); by != "" {
		merged.UpdatedBy = by
	} else if base != nil {
		merged.UpdatedBy = base.UpdatedBy
	}
	return &merged
}

func patchClearFields(patch *SessionRoutingPatch) []string {
	if patch == nil {
		return nil
	}
	return patch.ClearFields
}

// MergeMainAgentOverride 做字段级合并：delta 的非 nil 字段胜出，nil 字段保留
// base 值；profile 覆盖按 level 逐字段合并（INV-A7）。
func MergeMainAgentOverride(base, delta *AICLISessionMainAgentRoutingOverride) *AICLISessionMainAgentRoutingOverride {
	if base == nil && delta == nil {
		return nil
	}
	var merged AICLISessionMainAgentRoutingOverride
	if base != nil {
		merged = *base
		merged.Levels = cloneStringSlicePtr(base.Levels)
		merged.ExpensiveLevels = cloneStringSlicePtr(base.ExpensiveLevels)
		merged.Profiles = cloneMainProfileOverrideMap(base.Profiles)
	}
	if delta == nil {
		return &merged
	}
	if delta.Enabled != nil {
		merged.Enabled = cloneBoolPtr(delta.Enabled)
	}
	if delta.Levels != nil {
		merged.Levels = cloneStringSlicePtr(delta.Levels)
	}
	if delta.AllowExpert != nil {
		merged.AllowExpert = cloneBoolPtr(delta.AllowExpert)
	}
	if delta.DefaultDifficulty != nil {
		merged.DefaultDifficulty = cloneStringPtr(delta.DefaultDifficulty)
	}
	if delta.CostGuardMode != nil {
		merged.CostGuardMode = cloneStringPtr(delta.CostGuardMode)
	}
	if delta.MaxConsecutiveExpensiveSteps != nil {
		merged.MaxConsecutiveExpensiveSteps = cloneIntPtr(delta.MaxConsecutiveExpensiveSteps)
	}
	if delta.ExpensiveLevels != nil {
		merged.ExpensiveLevels = cloneStringSlicePtr(delta.ExpensiveLevels)
	}
	if delta.MaxInvalidReportsPerTurn != nil {
		merged.MaxInvalidReportsPerTurn = cloneIntPtr(delta.MaxInvalidReportsPerTurn)
	}
	if delta.DowngradeConfirmSteps != nil {
		merged.DowngradeConfirmSteps = cloneIntPtr(delta.DowngradeConfirmSteps)
	}
	if delta.MinDwellSteps != nil {
		merged.MinDwellSteps = cloneIntPtr(delta.MinDwellSteps)
	}
	if delta.RespectProviderHealth != nil {
		merged.RespectProviderHealth = cloneBoolPtr(delta.RespectProviderHealth)
	}
	if len(delta.Profiles) > 0 {
		if merged.Profiles == nil {
			merged.Profiles = make(map[string]AICLISessionRouteProfileOverride, len(delta.Profiles))
		}
		for level, deltaProfile := range delta.Profiles {
			level = normalizeRoutingLevelForDisplay(level)
			if level == "" {
				continue
			}
			existing := merged.Profiles[level]
			merged.Profiles[level] = mergeRouteProfileOverride(existing, deltaProfile)
		}
	}
	return &merged
}

// MergeSubAgentOverride 与 MergeMainAgentOverride 同口径（子 Agent 侧）。
func MergeSubAgentOverride(base, delta *AICLISessionSubAgentRoutingOverride) *AICLISessionSubAgentRoutingOverride {
	if base == nil && delta == nil {
		return nil
	}
	var merged AICLISessionSubAgentRoutingOverride
	if base != nil {
		merged = *base
		merged.Enabled = cloneBoolPtr(base.Enabled)
		merged.DefaultDifficulty = cloneStringPtr(base.DefaultDifficulty)
		if len(base.Levels) > 0 {
			merged.Levels = make(map[string]AICLISessionRouteProfileOverride, len(base.Levels))
			for level, profile := range base.Levels {
				merged.Levels[level] = profile
			}
		}
	}
	if delta == nil {
		return &merged
	}
	if delta.Enabled != nil {
		merged.Enabled = cloneBoolPtr(delta.Enabled)
	}
	if delta.DefaultDifficulty != nil {
		merged.DefaultDifficulty = cloneStringPtr(delta.DefaultDifficulty)
	}
	if len(delta.Levels) > 0 {
		if merged.Levels == nil {
			merged.Levels = make(map[string]AICLISessionRouteProfileOverride, len(delta.Levels))
		}
		for level, deltaProfile := range delta.Levels {
			level = normalizeRoutingLevelForDisplay(level)
			if level == "" {
				continue
			}
			existing := merged.Levels[level]
			merged.Levels[level] = mergeRouteProfileOverride(existing, deltaProfile)
		}
	}
	return &merged
}

func mergeRouteProfileOverride(base, delta AICLISessionRouteProfileOverride) AICLISessionRouteProfileOverride {
	merged := base
	if delta.Provider != nil {
		merged.Provider = cloneStringPtr(delta.Provider)
	}
	if delta.Model != nil {
		merged.Model = cloneStringPtr(delta.Model)
	}
	if delta.ReasoningEffort != nil {
		merged.ReasoningEffort = cloneStringPtr(delta.ReasoningEffort)
	}
	if delta.ThinkingEffort != nil {
		merged.ThinkingEffort = cloneStringPtr(delta.ThinkingEffort)
	}
	if delta.MaxTokens != nil {
		merged.MaxTokens = cloneIntPtr(delta.MaxTokens)
	}
	if delta.Timeout != nil {
		merged.Timeout = cloneStringPtr(delta.Timeout)
	}
	if delta.Temperature != nil {
		value := *delta.Temperature
		merged.Temperature = &value
	}
	if delta.Availability != nil {
		merged.Availability = cloneStringPtr(delta.Availability)
	}
	if delta.AvailabilityReason != nil {
		merged.AvailabilityReason = cloneStringPtr(delta.AvailabilityReason)
	}
	if delta.PromptCache != nil {
		merged.PromptCache = cloneBoolPtr(delta.PromptCache)
	}
	if delta.Candidates != nil {
		value := append([]AICLISubagentRouteCandidate(nil), (*delta.Candidates)...)
		merged.Candidates = &value
	}
	return merged
}

// ApplySessionRoutingClearFields 按 §3.5.1 键路径清除字段（reset 语义）。
// 支持：`main_agent` / `sub_agent`（整节）、`main_agent.profiles`（整表）、
// `main_agent.profiles.<level>`（整档）、`main_agent.profiles.<level>.<field>`
// 与子 Agent 的 `sub_agent.levels.<level>[.<field>]`，以及标量键（如
// `main_agent.enabled`、`sub_agent.default_difficulty`）。
func ApplySessionRoutingClearFields(ov *AICLISessionRoutingOverride, fields []string) {
	if ov == nil {
		return
	}
	for _, raw := range fields {
		clearSessionRoutingField(ov, raw)
	}
}

func clearSessionRoutingField(ov *AICLISessionRoutingOverride, raw string) {
	key := strings.TrimSpace(raw)
	if key == "" {
		return
	}
	parts := strings.Split(key, ".")
	if len(parts) == 0 {
		return
	}
	switch strings.ToLower(parts[0]) {
	case "main", "main_agent":
		clearMainAgentField(ov, parts[1:])
	case "sub", "sub_agent":
		clearSubAgentField(ov, parts[1:])
	}
}

func clearMainAgentField(ov *AICLISessionRoutingOverride, parts []string) {
	if len(parts) == 0 {
		ov.MainAgent = nil
		return
	}
	main := ov.MainAgent
	if main == nil {
		return
	}
	if strings.EqualFold(parts[0], "profiles") {
		switch len(parts) {
		case 1:
			main.Profiles = nil
		case 2:
			delete(main.Profiles, normalizeRoutingLevelForDisplay(parts[1]))
		default:
			level := normalizeRoutingLevelForDisplay(parts[1])
			profile, ok := main.Profiles[level]
			if !ok {
				return
			}
			profile = clearRouteProfileField(profile, parts[2])
			if routeProfileOverrideEmpty(profile) {
				delete(main.Profiles, level)
			} else {
				main.Profiles[level] = profile
			}
		}
		return
	}
	clearMainAgentScalar(main, parts[0])
}

func clearMainAgentScalar(main *AICLISessionMainAgentRoutingOverride, field string) {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "enabled":
		main.Enabled = nil
	case "levels":
		main.Levels = nil
	case "allow_expert":
		main.AllowExpert = nil
	case "default_difficulty":
		main.DefaultDifficulty = nil
	case "cost_guard_mode":
		main.CostGuardMode = nil
	case "max_consecutive_expensive_steps":
		main.MaxConsecutiveExpensiveSteps = nil
	case "expensive_levels":
		main.ExpensiveLevels = nil
	case "max_invalid_reports_per_turn":
		main.MaxInvalidReportsPerTurn = nil
	case "downgrade_confirm_steps":
		main.DowngradeConfirmSteps = nil
	case "min_dwell_steps":
		main.MinDwellSteps = nil
	case "health_gate.respect_provider_health", "respect_provider_health":
		main.RespectProviderHealth = nil
	}
}

func clearSubAgentField(ov *AICLISessionRoutingOverride, parts []string) {
	if len(parts) == 0 {
		ov.SubAgent = nil
		return
	}
	sub := ov.SubAgent
	if sub == nil {
		return
	}
	if strings.EqualFold(parts[0], "levels") {
		switch len(parts) {
		case 1:
			sub.Levels = nil
		case 2:
			delete(sub.Levels, normalizeRoutingLevelForDisplay(parts[1]))
		default:
			level := normalizeRoutingLevelForDisplay(parts[1])
			profile, ok := sub.Levels[level]
			if !ok {
				return
			}
			profile = clearRouteProfileField(profile, parts[2])
			if routeProfileOverrideEmpty(profile) {
				delete(sub.Levels, level)
			} else {
				sub.Levels[level] = profile
			}
		}
		return
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "enabled":
		sub.Enabled = nil
	case "default_difficulty":
		sub.DefaultDifficulty = nil
	}
}

func clearRouteProfileField(profile AICLISessionRouteProfileOverride, field string) AICLISessionRouteProfileOverride {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "provider":
		profile.Provider = nil
	case "model":
		profile.Model = nil
	case "reasoning_effort":
		profile.ReasoningEffort = nil
	case "thinking_effort":
		profile.ThinkingEffort = nil
	case "max_tokens":
		profile.MaxTokens = nil
	case "timeout":
		profile.Timeout = nil
	case "temperature":
		profile.Temperature = nil
	case "availability":
		profile.Availability = nil
	case "availability_reason":
		profile.AvailabilityReason = nil
	case "prompt_cache":
		profile.PromptCache = nil
	case "candidates":
		profile.Candidates = nil
	}
	return profile
}

func routeProfileOverrideEmpty(profile AICLISessionRouteProfileOverride) bool {
	return profile.Provider == nil && profile.Model == nil && profile.ReasoningEffort == nil &&
		profile.ThinkingEffort == nil && profile.MaxTokens == nil && profile.Timeout == nil &&
		profile.Temperature == nil && profile.Availability == nil && profile.AvailabilityReason == nil &&
		profile.PromptCache == nil && profile.Candidates == nil
}

// MaterializeMainAgentRoutingConfig 把会话覆盖物化到配置结构上（workspace/config
// 层写入用）：先深拷贝 base（绝不原地改写快照，INV-A5），再按非 nil 字段覆盖。
func MaterializeMainAgentRoutingConfig(base *AICLIMainAgentRoutingConfig, ov *AICLISessionMainAgentRoutingOverride) *AICLIMainAgentRoutingConfig {
	clone := cloneMainAgentRoutingConfigForWrite(base)
	if ov == nil {
		return clone
	}
	if ov.Enabled != nil {
		clone.Enabled = *ov.Enabled
	}
	if ov.Levels != nil {
		clone.Levels = append([]string(nil), (*ov.Levels)...)
	}
	if ov.AllowExpert != nil {
		clone.AllowExpert = *ov.AllowExpert
	}
	if ov.DefaultDifficulty != nil {
		clone.DefaultDifficulty = *ov.DefaultDifficulty
	}
	if ov.CostGuardMode != nil {
		clone.CostGuardMode = *ov.CostGuardMode
	}
	if ov.MaxConsecutiveExpensiveSteps != nil {
		clone.MaxConsecutiveExpensiveSteps = *ov.MaxConsecutiveExpensiveSteps
	}
	if ov.ExpensiveLevels != nil {
		clone.ExpensiveLevels = append([]string(nil), (*ov.ExpensiveLevels)...)
	}
	if ov.MaxInvalidReportsPerTurn != nil {
		clone.MaxInvalidReportsPerTurn = *ov.MaxInvalidReportsPerTurn
	}
	if ov.DowngradeConfirmSteps != nil {
		clone.DowngradeConfirmSteps = *ov.DowngradeConfirmSteps
	}
	if ov.MinDwellSteps != nil {
		clone.MinDwellSteps = *ov.MinDwellSteps
	}
	if ov.RespectProviderHealth != nil {
		clone.HealthGate.RespectProviderHealth = *ov.RespectProviderHealth
	}
	if len(ov.Profiles) > 0 {
		if clone.Profiles == nil {
			clone.Profiles = make(map[string]AICLISubagentRouteProfile, len(ov.Profiles))
		}
		for level, override := range ov.Profiles {
			level = normalizeRoutingLevelForDisplay(level)
			if level == "" {
				continue
			}
			profile := clone.Profiles[level]
			applyMainRouteProfileOverride(&profile, override)
			clone.Profiles[level] = profile
		}
	}
	return clone
}

// MaterializeSubagentRoutingConfig 是子 Agent 侧的同一物化（§5.6）。
func MaterializeSubagentRoutingConfig(base *AICLISubagentRoutingConfig, ov *AICLISessionSubAgentRoutingOverride) *AICLISubagentRoutingConfig {
	clone := cloneSubagentRoutingConfigForWrite(base)
	if ov == nil {
		return clone
	}
	if ov.Enabled != nil {
		value := *ov.Enabled
		clone.Enabled = &value
	}
	if ov.DefaultDifficulty != nil {
		clone.DefaultDifficulty = *ov.DefaultDifficulty
	}
	if len(ov.Levels) > 0 {
		if clone.Levels == nil {
			clone.Levels = make(map[string]AICLISubagentRouteProfile, len(ov.Levels))
		}
		for level, override := range ov.Levels {
			level = normalizeRoutingLevelForDisplay(level)
			if level == "" {
				continue
			}
			profile := clone.Levels[level]
			applyMainRouteProfileOverride(&profile, override)
			clone.Levels[level] = profile
		}
	}
	return clone
}

// ClearWorkspaceRoutingFields 按 §3.5.1 键路径清除工作区偏好里的 routing 字段
// （workspace 层 reset）。整节清除由调用方传 `main_agent` / `sub_agent`。
func ClearWorkspaceRoutingFields(prefs *AICLIWorkspaceRoutingPreferences, fields []string) {
	if prefs == nil {
		return
	}
	for _, raw := range fields {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		parts := strings.Split(key, ".")
		switch strings.ToLower(parts[0]) {
		case "main", "main_agent":
			if len(parts) == 1 {
				prefs.MainAgent = nil
				continue
			}
			if prefs.MainAgent == nil {
				continue
			}
			if strings.EqualFold(parts[1], "profiles") {
				switch len(parts) {
				case 2:
					prefs.MainAgent.Profiles = nil
				case 3:
					delete(prefs.MainAgent.Profiles, normalizeRoutingLevelForDisplay(parts[2]))
				default:
					level := normalizeRoutingLevelForDisplay(parts[2])
					profile, ok := prefs.MainAgent.Profiles[level]
					if !ok {
						continue
					}
					clearRouteProfileConfigField(&profile, parts[3])
					prefs.MainAgent.Profiles[level] = profile
				}
				continue
			}
			clearMainAgentConfigScalar(prefs.MainAgent, parts[1])
		case "sub", "sub_agent":
			if len(parts) == 1 {
				prefs.SubAgent = nil
				continue
			}
			if prefs.SubAgent == nil {
				continue
			}
			if strings.EqualFold(parts[1], "levels") {
				switch len(parts) {
				case 2:
					prefs.SubAgent.Levels = nil
				case 3:
					delete(prefs.SubAgent.Levels, normalizeRoutingLevelForDisplay(parts[2]))
				default:
					level := normalizeRoutingLevelForDisplay(parts[2])
					profile, ok := prefs.SubAgent.Levels[level]
					if !ok {
						continue
					}
					clearRouteProfileConfigField(&profile, parts[3])
					prefs.SubAgent.Levels[level] = profile
				}
			}
		}
	}
}

// ClearMainAgentRoutingConfigFields 按 §3.5.1 键路径清除 config 层
// `aicli.main_agent.routing` 内的字段（config 层的字段级 reset）。
//
// 键路径口径与 ClearWorkspaceRoutingFields 一致（main/main_agent 前缀），
// 差别只在于目标是 config 的 main routing 结构本身。清空的档位条目整条删除，
// 避免在配置文件里留下 `profiles: {hard: {}}` 这类残渣。
func ClearMainAgentRoutingConfigFields(cfg *AICLIMainAgentRoutingConfig, fields []string) {
	if cfg == nil {
		return
	}
	for _, raw := range fields {
		key := strings.TrimSpace(raw)
		if key == "" {
			continue
		}
		parts := strings.Split(key, ".")
		switch strings.ToLower(parts[0]) {
		case "main", "main_agent":
			if len(parts) == 1 {
				*cfg = AICLIMainAgentRoutingConfig{}
				continue
			}
			if strings.EqualFold(parts[1], "profiles") {
				switch len(parts) {
				case 2:
					cfg.Profiles = nil
				case 3:
					delete(cfg.Profiles, normalizeRoutingLevelForDisplay(parts[2]))
				default:
					level := normalizeRoutingLevelForDisplay(parts[2])
					profile, ok := cfg.Profiles[level]
					if !ok {
						continue
					}
					clearRouteProfileConfigField(&profile, parts[3])
					if reflect.DeepEqual(profile, AICLISubagentRouteProfile{}) {
						delete(cfg.Profiles, level)
						continue
					}
					cfg.Profiles[level] = profile
				}
				continue
			}
			clearMainAgentConfigScalar(cfg, parts[1])
		}
	}
}

// IsEmptyMainAgentRoutingConfig 报告 main routing 是否已无任何覆盖内容。
// config 层字段级清除后为空即删除该节，解析回到下一层（§3.5.2 阶梯回退）。
func IsEmptyMainAgentRoutingConfig(cfg *AICLIMainAgentRoutingConfig) bool {
	if cfg == nil {
		return true
	}
	return reflect.DeepEqual(*cfg, AICLIMainAgentRoutingConfig{})
}

func clearMainAgentConfigScalar(cfg *AICLIMainAgentRoutingConfig, field string) {
	if cfg == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "enabled":
		cfg.Enabled = false
	case "levels":
		cfg.Levels = nil
	case "allow_expert":
		cfg.AllowExpert = false
	case "default_difficulty":
		cfg.DefaultDifficulty = ""
	case "cost_guard_mode":
		cfg.CostGuardMode = ""
	case "max_consecutive_expensive_steps":
		cfg.MaxConsecutiveExpensiveSteps = 0
	case "expensive_levels":
		cfg.ExpensiveLevels = nil
	case "max_invalid_reports_per_turn":
		cfg.MaxInvalidReportsPerTurn = 0
	case "downgrade_confirm_steps":
		cfg.DowngradeConfirmSteps = 0
	case "min_dwell_steps":
		cfg.MinDwellSteps = 0
	case "health_gate.respect_provider_health", "respect_provider_health":
		cfg.HealthGate.RespectProviderHealth = false
	}
}

func clearRouteProfileConfigField(profile *AICLISubagentRouteProfile, field string) {
	if profile == nil {
		return
	}
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "provider":
		profile.Provider = ""
	case "model":
		profile.Model = ""
	case "reasoning_effort":
		profile.ReasoningEffort = ""
	case "thinking_effort":
		profile.ThinkingEffort = ""
	case "max_tokens":
		profile.MaxTokens = 0
	case "timeout":
		profile.Timeout = 0
	case "temperature":
		profile.Temperature = nil
	case "availability":
		profile.Availability = ""
	case "availability_reason":
		profile.AvailabilityReason = ""
	case "prompt_cache":
		profile.PromptCache = nil
	case "candidates":
		profile.Candidates = nil
	}
}

func applyMainRouteProfileOverride(profile *AICLISubagentRouteProfile, override AICLISessionRouteProfileOverride) {
	if profile == nil {
		return
	}
	if override.Provider != nil {
		profile.Provider = strings.TrimSpace(*override.Provider)
	}
	if override.Model != nil {
		profile.Model = strings.TrimSpace(*override.Model)
	}
	if override.ReasoningEffort != nil {
		profile.ReasoningEffort = strings.TrimSpace(*override.ReasoningEffort)
	}
	if override.ThinkingEffort != nil {
		profile.ThinkingEffort = strings.TrimSpace(*override.ThinkingEffort)
	}
	if override.MaxTokens != nil {
		profile.MaxTokens = *override.MaxTokens
	}
	if override.Timeout != nil {
		if duration, err := time.ParseDuration(strings.TrimSpace(*override.Timeout)); err == nil {
			profile.Timeout = duration
		}
	}
	if override.Temperature != nil {
		value := *override.Temperature
		profile.Temperature = &value
	}
	if override.Availability != nil {
		profile.Availability = strings.TrimSpace(*override.Availability)
	}
	if override.AvailabilityReason != nil {
		profile.AvailabilityReason = strings.TrimSpace(*override.AvailabilityReason)
	}
	if override.PromptCache != nil {
		value := *override.PromptCache
		profile.PromptCache = &value
	}
	if override.Candidates != nil {
		profile.Candidates = append([]AICLISubagentRouteCandidate(nil), (*override.Candidates)...)
	}
}

func cloneMainAgentRoutingConfigForWrite(base *AICLIMainAgentRoutingConfig) *AICLIMainAgentRoutingConfig {
	if base == nil {
		return &AICLIMainAgentRoutingConfig{}
	}
	clone := *base
	clone.Levels = append([]string(nil), base.Levels...)
	clone.ExpensiveLevels = append([]string(nil), base.ExpensiveLevels...)
	if len(base.Profiles) > 0 {
		clone.Profiles = make(map[string]AICLISubagentRouteProfile, len(base.Profiles))
		for level, profile := range base.Profiles {
			profile.Candidates = append([]AICLISubagentRouteCandidate(nil), profile.Candidates...)
			clone.Profiles[level] = profile
		}
	}
	return &clone
}

func cloneSubagentRoutingConfigForWrite(base *AICLISubagentRoutingConfig) *AICLISubagentRoutingConfig {
	if base == nil {
		return &AICLISubagentRoutingConfig{}
	}
	clone := *base
	if base.Enabled != nil {
		value := *base.Enabled
		clone.Enabled = &value
	}
	if base.InheritParentWhenMissing != nil {
		value := *base.InheritParentWhenMissing
		clone.InheritParentWhenMissing = &value
	}
	if base.ValidateModelCapabilities != nil {
		value := *base.ValidateModelCapabilities
		clone.ValidateModelCapabilities = &value
	}
	clone.AllowedProviderOverrides = append([]string(nil), base.AllowedProviderOverrides...)
	clone.AllowedModelOverrides = append([]string(nil), base.AllowedModelOverrides...)
	if len(base.Levels) > 0 {
		clone.Levels = make(map[string]AICLISubagentRouteProfile, len(base.Levels))
		for level, profile := range base.Levels {
			profile.Candidates = append([]AICLISubagentRouteCandidate(nil), profile.Candidates...)
			clone.Levels[level] = profile
		}
	}
	return &clone
}

func cloneMainProfileOverrideMap(base map[string]AICLISessionRouteProfileOverride) map[string]AICLISessionRouteProfileOverride {
	if len(base) == 0 {
		return nil
	}
	clone := make(map[string]AICLISessionRouteProfileOverride, len(base))
	for level, profile := range base {
		clone[level] = profile
	}
	return clone
}

func cloneBoolPtr(value *bool) *bool {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneIntPtr(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func cloneStringSlicePtr(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	clone := append([]string(nil), (*value)...)
	return &clone
}

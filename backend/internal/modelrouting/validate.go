package modelrouting

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// ValidateConfig validates static routing configuration without requiring a
// runtime provider catalog.
func ValidateConfig(cfg *agentconfig.AICLISubagentRoutingConfig) error {
	_, err := ValidateConfigWithWarnings(cfg)
	return err
}

// ValidateConfigWithWarnings 与 ValidateConfig 相同，但额外返回不影响启动的
// 静态告警（G5 的"同 profile 别名撞车"、G6 的 max_expert_concurrency=0）。
// Resolver 会把它们并入 RouteDecision.Warnings，使配置层的问题出现在路由审计
// 里，而不是只停留在启动日志。
func ValidateConfigWithWarnings(cfg *agentconfig.AICLISubagentRoutingConfig) ([]string, error) {
	if cfg == nil {
		return nil, nil
	}
	warnings := []string{}
	if _, ok := NormalizeCompatibilityMode(cfg.CompatibilityMode); !ok {
		return nil, fmt.Errorf("invalid subagent compatibility mode %q", cfg.CompatibilityMode)
	}
	if strings.TrimSpace(cfg.DefaultDifficulty) != "" {
		if _, ok := NormalizeDifficulty(cfg.DefaultDifficulty); !ok {
			return nil, fmt.Errorf("invalid default subagent difficulty %q", cfg.DefaultDifficulty)
		}
	}
	if strings.TrimSpace(cfg.UnsupportedReasoningPolicy) != "" {
		if _, ok := NormalizeUnsupportedReasoningPolicy(cfg.UnsupportedReasoningPolicy); !ok {
			return nil, fmt.Errorf("invalid subagent unsupported reasoning policy %q", cfg.UnsupportedReasoningPolicy)
		}
	}
	if strings.TrimSpace(cfg.OnReasoningUnsupported) != "" {
		if _, ok := NormalizeUnsupportedReasoningPolicy(cfg.OnReasoningUnsupported); !ok {
			return nil, fmt.Errorf("invalid subagent on reasoning unsupported policy %q", cfg.OnReasoningUnsupported)
		}
	}
	if strings.TrimSpace(cfg.AvailabilityPolicy) != "" {
		if _, ok := NormalizeAvailabilityPolicy(cfg.AvailabilityPolicy); !ok {
			return nil, fmt.Errorf("invalid subagent availability policy %q", cfg.AvailabilityPolicy)
		}
	}
	if _, ok := NormalizePromoteExplicitMode(cfg.PromoteExplicitDifficulty); !ok {
		return nil, fmt.Errorf("invalid subagent promote_explicit_difficulty %q (want off|warn|enforce)", cfg.PromoteExplicitDifficulty)
	}
	if cfg.Heuristics != nil {
		if err := validateKeywordList("heuristics.promote_keywords", cfg.Heuristics.PromoteKeywords); err != nil {
			return nil, err
		}
		if err := validateKeywordList("heuristics.promote_keywords_combo", cfg.Heuristics.PromoteKeywordsCombo); err != nil {
			return nil, err
		}
	}
	if err := validateRouteAliases("levels", cfg.Levels, &warnings); err != nil {
		return nil, err
	}
	for _, key := range sortedRouteKeys(cfg.Levels) {
		profile := cfg.Levels[key]
		difficulty, ok := NormalizeDifficulty(key)
		if !ok {
			return nil, fmt.Errorf("invalid subagent difficulty route key %q", key)
		}
		if err := validateRouteProfile(difficulty, profile, cfg); err != nil {
			return nil, err
		}
	}
	// v4（K-4/K-6）：task_types 是封闭枚举查表。未知键 → task_type_unknown:<v>
	// warning（不报错、不静默）：该键是死条目（运行期只按已知类别匹配），但仍照常
	// 校验其内层 difficulty profile，以便抓出难度键拼写错误。
	for _, taskType := range sortedTaskTypeKeys(cfg.TaskTypes) {
		levels := cfg.TaskTypes[taskType]
		normalized, ok := ValidateTaskType(taskType)
		if !ok {
			warnings = append(warnings, "task_type_unknown:"+NormalizeTaskType(taskType))
			normalized = NormalizeTaskType(taskType)
		}
		if err := validateRouteAliases("task_types."+normalized, levels, &warnings); err != nil {
			return nil, err
		}
		for _, key := range sortedRouteKeys(levels) {
			profile := levels[key]
			difficulty, ok := NormalizeDifficulty(key)
			if !ok {
				return nil, fmt.Errorf("invalid subagent task_type route key %q.%q", taskType, key)
			}
			if err := validateRouteProfile(normalized+"."+difficulty, profile, cfg); err != nil {
				return nil, err
			}
		}
	}
	for _, role := range sortedRoleKeys(cfg.Roles) {
		levels := cfg.Roles[role]
		if strings.TrimSpace(role) == "" {
			return nil, fmt.Errorf("subagent role override key cannot be empty")
		}
		if err := validateRouteAliases("roles."+strings.TrimSpace(role), levels, &warnings); err != nil {
			return nil, err
		}
		for _, key := range sortedRouteKeys(levels) {
			profile := levels[key]
			difficulty, ok := NormalizeDifficulty(key)
			if !ok {
				return nil, fmt.Errorf("invalid subagent role route key %q.%q", role, key)
			}
			if err := validateRouteProfile(role+"."+difficulty, profile, cfg); err != nil {
				return nil, err
			}
		}
	}
	if cfg.MaxExpertConcurrency < -1 {
		return nil, fmt.Errorf("max_expert_concurrency must be -1 (explicit unlimited) or a positive limit, got %d", cfg.MaxExpertConcurrency)
	}
	if cfg.MaxExpertConcurrency == 0 {
		warnings = append(warnings, "max_expert_concurrency_zero_means_unlimited")
	}
	return warnings, nil
}

// validateRouteAliases 检测归一后撞车的难度键（G5）：两个原始键归一后相同且
// profile 完全一致时只记 warning（无行为风险）；profile 不一致时返回 error，
// 消息含两个原始键与归一结果，便于直接照做修复。
func validateRouteAliases(label string, levels map[string]agentconfig.AICLISubagentRouteProfile, warnings *[]string) error {
	seen := map[string]string{}
	for _, key := range sortedRouteKeys(levels) {
		difficulty, ok := NormalizeDifficulty(key)
		if !ok {
			return fmt.Errorf("invalid subagent %s key %q", label, key)
		}
		prev, dup := seen[difficulty]
		if !dup {
			seen[difficulty] = key
			continue
		}
		if reflect.DeepEqual(levels[prev], levels[key]) {
			*warnings = append(*warnings, fmt.Sprintf(
				"subagent %s alias keys %q and %q both normalize to %q with identical profiles",
				label, prev, key, difficulty,
			))
			continue
		}
		return fmt.Errorf(
			"subagent %s alias keys %q and %q both normalize to %q with different profiles; keep one spelling or make both profiles identical",
			label, prev, key, difficulty,
		)
	}
	return nil
}

// validateKeywordList 拒绝空条目与归一后重复的词：重复词会让同一次命中产生
// 多条重复告警，空词则无法解释也无法回溯。
func validateKeywordList(label string, keywords []string) error {
	seen := map[string]string{}
	for _, keyword := range keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			return fmt.Errorf("subagent %s contains an empty keyword", label)
		}
		folded := strings.ToLower(trimmed)
		if prev, dup := seen[folded]; dup {
			return fmt.Errorf("subagent %s contains duplicate keyword %q (already declared as %q)", label, trimmed, prev)
		}
		seen[folded] = trimmed
	}
	return nil
}

func sortedRouteKeys(levels map[string]agentconfig.AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(levels))
	for key := range levels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedRoleKeys(roles map[string]map[string]agentconfig.AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(roles))
	for key := range roles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedTaskTypeKeys(taskTypes map[string]map[string]agentconfig.AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(taskTypes))
	for key := range taskTypes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func validateRouteProfile(label string, profile agentconfig.AICLISubagentRouteProfile, cfg *agentconfig.AICLISubagentRoutingConfig) error {
	if profile.MaxTokens < 0 {
		return fmt.Errorf("subagent route %s max_tokens cannot be negative", label)
	}
	if profile.Timeout < 0 {
		return fmt.Errorf("subagent route %s timeout cannot be negative", label)
	}
	if strings.TrimSpace(profile.Availability) != "" {
		if _, ok := NormalizeAvailability(profile.Availability); !ok {
			return fmt.Errorf("subagent route %s invalid availability %q", label, profile.Availability)
		}
	}
	for index, candidate := range profile.Candidates {
		if err := validateRouteCandidate(fmt.Sprintf("%s.candidates[%d]", label, index), candidate); err != nil {
			return err
		}
	}
	if !InheritParentWhenMissing(cfg) && (strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "") {
		return fmt.Errorf("subagent route %s must set provider and model when inherit_parent_when_missing=false", label)
	}
	return nil
}

// validateRouteCandidate rejects fallback entries that could never be selected
// or that would send a subagent to an unparseable target.
func validateRouteCandidate(label string, candidate agentconfig.AICLISubagentRouteCandidate) error {
	if strings.TrimSpace(candidate.Provider) == "" {
		return fmt.Errorf("subagent route %s must set provider", label)
	}
	if strings.TrimSpace(candidate.Model) == "" {
		return fmt.Errorf("subagent route %s must set model", label)
	}
	if strings.TrimSpace(candidate.Availability) != "" {
		if _, ok := NormalizeAvailability(candidate.Availability); !ok {
			return fmt.Errorf("subagent route %s invalid availability %q", label, candidate.Availability)
		}
	}
	return nil
}

package modelrouting

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// Resolve returns the effective child-agent route. When routing is disabled,
// the decision mirrors legacy behavior: inherit parent provider/reasoning and
// allow the task model field to override only the model.
func (r Resolver) Resolve(parent ParentDefaults, task TaskHint) (RouteDecision, error) {
	if !RoutingEnabled(r.Config) {
		return r.resolveDisabled(parent, task), nil
	}
	staticWarnings, err := ValidateConfigWithWarnings(r.Config)
	if err != nil {
		return RouteDecision{}, err
	}

	decision := RouteDecision{
		DifficultyRationale: strings.TrimSpace(task.DifficultyRationale),
		TaskType:            EffectiveTaskType(task.TaskType, task.Role, task.ReadOnly),
		TaskSubject:         strings.TrimSpace(task.TaskSubject),
		Provider:            strings.TrimSpace(parent.Provider),
		Model:               strings.TrimSpace(parent.Model),
		ReasoningEffort:     NormalizeReasoningEffort(parent.ReasoningEffort),
		MaxTokens:           parent.MaxTokens,
		Timeout:             parent.Timeout,
		Temperature:         parent.Temperature,
		Source:              SourceParentInherit,
		Warnings:            append([]string(nil), task.Warnings...),
	}

	difficulty, difficultySource, difficultyWarnings, err := resolveDifficulty(r.Config, task)
	if err != nil {
		return RouteDecision{}, err
	}
	decision.Difficulty = difficulty
	decision.DifficultySource = difficultySource
	decision.Warnings = append(decision.Warnings, staticWarnings...)
	decision.Warnings = append(decision.Warnings, difficultyWarnings...)

	profile, source, ok := routeProfileForTask(r.Config, task, difficulty)
	if ok {
		if err := r.applyRouteProfile(&decision, profile, source, difficulty); err != nil {
			return RouteDecision{}, err
		}
	}

	r.applyExplicitOverrides(&decision, task)
	if err := r.finalizeDecision(parent, &decision); err != nil {
		return RouteDecision{}, err
	}
	return decision, nil
}

func (r Resolver) resolveDisabled(parent ParentDefaults, task TaskHint) RouteDecision {
	decision := RouteDecision{
		Provider:            strings.TrimSpace(parent.Provider),
		Model:               strings.TrimSpace(parent.Model),
		TaskType:            EffectiveTaskType(task.TaskType, task.Role, task.ReadOnly),
		TaskSubject:         strings.TrimSpace(task.TaskSubject),
		ReasoningEffort:     NormalizeReasoningEffort(parent.ReasoningEffort),
		MaxTokens:           parent.MaxTokens,
		Timeout:             parent.Timeout,
		Temperature:         parent.Temperature,
		DifficultyRationale: strings.TrimSpace(task.DifficultyRationale),
		Source:              SourceDisabled,
		Warnings:            append([]string(nil), task.Warnings...),
	}
	if difficulty, ok := NormalizeDifficulty(task.Difficulty); ok {
		decision.Difficulty = difficulty
		decision.DifficultySource = "explicit"
	}
	if strings.TrimSpace(task.Model) != "" {
		decision.Model = strings.TrimSpace(task.Model)
	}
	if task.BudgetTokens > 0 {
		decision.MaxTokens = task.BudgetTokens
	}
	if task.Timeout > 0 {
		decision.Timeout = task.Timeout
	}
	return decision
}

func resolveDifficulty(cfg *agentconfig.AICLISubagentRoutingConfig, task TaskHint) (string, string, []string, error) {
	// v4（K-4）：显式但未知的 task_type 一律记 task_type_unknown:<v> 并忽略该
	// 字段（不报错、不回退猜词、不参与 floor）。off 模式也保留这条输入校验告警：
	// 它描述的是新字段本身非法，不是"被提升"，不属于三态开关管辖。
	taskTypeWarnings := []string{}
	if raw := strings.TrimSpace(task.TaskType); raw != "" {
		if _, ok := ValidateTaskType(raw); !ok {
			taskTypeWarnings = append(taskTypeWarnings, "task_type_unknown:"+NormalizeTaskType(raw))
		}
	}
	if difficulty, ok := NormalizeDifficulty(task.Difficulty); ok {
		difficulty, source, warnings, err := resolveExplicitDifficulty(cfg, task, difficulty)
		if err != nil {
			return "", "", nil, err
		}
		return difficulty, source, append(taskTypeWarnings, warnings...), nil
	}

	warnings := append([]string(nil), taskTypeWarnings...)
	if strings.TrimSpace(task.Difficulty) == "" {
		warnings = append(warnings, "difficulty_missing_defaulted")
	} else {
		if StrictCompatibilityMode(cfg) {
			return "", "", nil, fmt.Errorf("invalid subagent difficulty %q", task.Difficulty)
		}
		warnings = append(warnings, "difficulty_invalid_defaulted")
	}

	difficulty := DefaultDifficulty(cfg)
	source := "default"
	if promoted, hits := promotedDifficulty(task, difficulty, cfg); promoted != difficulty {
		warnings = append(warnings, "difficulty_promoted_by_heuristic")
		warnings = append(warnings, promotionWarnings(hits, task.Role)...)
		difficulty = promoted
		source = "inferred"
	}
	return difficulty, source, warnings, nil
}

// resolveExplicitDifficulty 处理「LLM 显式声明了合法难度」的分支（G3）。
//
// 历史行为是立即返回、完全短路启发式；改后按 promote_explicit_difficulty 三态
// 决定：off 回到历史行为，warn 只写告警，enforce 真正提升。提升取 rank 最大值，
// 因此只会升档，绝不会把显式声明的 expert 降下来。
func resolveExplicitDifficulty(
	cfg *agentconfig.AICLISubagentRoutingConfig,
	task TaskHint,
	difficulty string,
) (string, string, []string, error) {
	mode := PromoteExplicitMode(cfg)
	if mode == PromoteExplicitOff {
		return difficulty, "explicit", nil, nil
	}
	promoted, hits := promotedDifficulty(task, difficulty, cfg)
	if promoted == difficulty || hits.empty() {
		return difficulty, "explicit", nil, nil
	}
	warnings := []string{"difficulty_promoted_over_explicit"}
	warnings = append(warnings, promotionWarnings(hits, task.Role)...)
	if mode == PromoteExplicitWarn {
		warnings = append(warnings, "difficulty_promotion_warn_only")
		return difficulty, "explicit", warnings, nil
	}
	return promoted, SourceExplicitPromoted, warnings, nil
}

// promotedDifficulty 返回提升后的难度与命中证据（v4 G3 四输入 rank-max）：
//
//	rank = max(显式 difficulty, floor(显式 task_type), 角色底, 关键词命中)
//
// 提升是单调的：只抬高 rank，不降低。floor 只认**显式** task_type——缺省时按
// role 别名推导出的隐式 task_type 只喂 profile 查表与审计（见 EffectiveTaskType），
// 不参与 floor，否则 role=writer（v3 底 normal）会经 implement（floor=hard）
// 被抬档，破坏「task_type 缺省 ⇒ 档位与 v3 逐字一致」（doc8 §8.1）。角色规则
// 与关键词规则保持 v3 原样，相互独立、可叠加；关键词与三态开关正交（doc8 §5.3）。
func promotedDifficulty(task TaskHint, difficulty string, cfg *agentconfig.AICLISubagentRoutingConfig) (string, promotionHits) {
	rank := difficultyRank(difficulty)
	hits := promotionHits{}
	if normalized, ok := ValidateTaskType(task.TaskType); ok {
		if floor, ok := TaskTypeFloor(normalized); ok && rank < difficultyRank(floor) {
			rank = difficultyRank(floor)
			hits.TaskType = normalized
		}
	}
	role := NormalizeRole(task.Role)
	if (role == "verifier" || (role == "writer" && !task.ReadOnly)) && rank < difficultyRank(DifficultyNormal) {
		rank = difficultyRank(DifficultyNormal)
		hits.Role = true
	}
	if HeuristicsDisabled(cfg) {
		return difficultyForRank(rank), hits
	}
	if goal := normalizeKeywordText(task.Goal); goal != "" {
		if strong := matchedKeywords(goal, PromoteKeywords(cfg)); len(strong) > 0 {
			if rank < difficultyRank(DifficultyHard) {
				rank = difficultyRank(DifficultyHard)
			}
			hits.Strong = strong
		}
		combo := matchedKeywords(goal, PromoteComboKeywords(cfg))
		if len(combo) >= weakSignalMinHits || (len(combo) >= 1 && role == "writer" && !task.ReadOnly) {
			if rank < difficultyRank(DifficultyHard) {
				rank = difficultyRank(DifficultyHard)
			}
			hits.Combo = combo
		}
	}
	return difficultyForRank(rank), hits
}

func difficultyRank(difficulty string) int {
	switch difficulty {
	case DifficultyEasy:
		return 1
	case DifficultyNormal:
		return 2
	case DifficultyHard:
		return 3
	case DifficultyExpert:
		return 4
	default:
		return 2
	}
}

func difficultyForRank(rank int) string {
	switch {
	case rank <= 1:
		return DifficultyEasy
	case rank == 2:
		return DifficultyNormal
	case rank == 3:
		return DifficultyHard
	default:
		return DifficultyExpert
	}
}

// routeProfileForTask 按 v4 查表顺序取 profile（cfg.Roles 是 cfg.TaskTypes 的
// 别名，plan K-3）：
//  1. 生效 task_type（显式合法值优先，缺省/未知按 role 别名推导）查
//     cfg.TaskTypes，命中 → source=task_type_override；
//  2. 未命中回落 cfg.Roles[role] → source=role_override：迁移窗口内"配置还没搬
//     到 task_types、但任务已开始带 task_type"时仍命中旧 profile，保证未迁移
//     旧配置、无别名自定义 role、只读 writer 行为不变；
//  3. 最终兜底 cfg.Levels[difficulty] → source=difficulty_level。
func routeProfileForTask(cfg *agentconfig.AICLISubagentRoutingConfig, task TaskHint, difficulty string) (agentconfig.AICLISubagentRouteProfile, string, bool) {
	if cfg == nil {
		return agentconfig.AICLISubagentRouteProfile{}, "", false
	}
	if taskType, ok := ValidateTaskType(task.TaskType); ok {
		for configuredType, levels := range cfg.TaskTypes {
			if NormalizeTaskType(configuredType) != taskType {
				continue
			}
			if profile, ok := routeProfileFromMap(levels, difficulty); ok {
				return profile, SourceTaskTypeOverride, true
			}
		}
	} else if derived := TaskTypeFromRole(task.Role, task.ReadOnly); derived != "" {
		for configuredType, levels := range cfg.TaskTypes {
			if NormalizeTaskType(configuredType) != derived {
				continue
			}
			if profile, ok := routeProfileFromMap(levels, difficulty); ok {
				return profile, SourceTaskTypeOverride, true
			}
		}
	}
	role := NormalizeRole(task.Role)
	if role != "" {
		for configuredRole, levels := range cfg.Roles {
			if NormalizeRole(configuredRole) != role {
				continue
			}
			if profile, ok := routeProfileFromMap(levels, difficulty); ok {
				return profile, SourceRoleOverride, true
			}
		}
	}
	if profile, ok := routeProfileFromMap(cfg.Levels, difficulty); ok {
		return profile, SourceDifficultyLevel, true
	}
	return agentconfig.AICLISubagentRouteProfile{}, "", false
}

func routeProfileFromMap(levels map[string]agentconfig.AICLISubagentRouteProfile, difficulty string) (agentconfig.AICLISubagentRouteProfile, bool) {
	keys := make([]string, 0, len(levels))
	for key := range levels {
		keys = append(keys, key)
	}
	// 排序后扫描：即使校验被绕过（例如别名撞车未走 Validate），命中哪个 profile
	// 也是确定的，不再依赖 Go map 的随机迭代顺序（G5）。
	sort.Strings(keys)
	for _, key := range keys {
		normalized, ok := NormalizeDifficulty(key)
		if ok && normalized == difficulty {
			return levels[key], true
		}
	}
	return agentconfig.AICLISubagentRouteProfile{}, false
}

// applyRouteProfile applies a difficulty/role profile. When the profile's
// primary target is gated out by an availability or prompt-cache annotation,
// the same-difficulty fallback chain takes over. The chain is resolved exactly
// once, here, so a subagent keeps a single model for its whole lifetime and its
// own prompt cache stays warm.
func (r Resolver) applyRouteProfile(
	decision *RouteDecision,
	profile agentconfig.AICLISubagentRouteProfile,
	source string,
	label string,
) error {
	applyProfileSettings(decision, profile)

	target, evaluations, err := r.selectRouteTarget(profile, label)
	decision.Candidates = evaluations
	if errors.Is(err, errRouteHealthExhausted) {
		r.degradeToParentForHealth(decision)
		return nil
	}
	if err != nil {
		return err
	}

	if provider := strings.TrimSpace(target.Provider); provider != "" {
		if strings.TrimSpace(target.Model) == "" && !strings.EqualFold(provider, decision.Provider) {
			decision.Model = ""
		}
		decision.Provider = provider
	}
	if model := strings.TrimSpace(target.Model); model != "" {
		decision.Model = model
	}

	decision.Source = source
	if !target.Primary {
		decision.Source = SourceFailoverCandidate
		decision.Warnings = append(decision.Warnings, "route_failover_candidate_used")
		markFallback(decision, target.FallbackReason)
	} else if hasGatedFallback(evaluations) {
		// The primary carried the route, but a configured fallback was gated
		// out. Surface it now rather than at the moment failover is needed.
		decision.Warnings = append(decision.Warnings, "route_fallback_chain_degraded")
	}
	if availability, ok := NormalizeAvailability(target.Availability); ok && availability == AvailabilityDegraded {
		decision.Warnings = append(decision.Warnings, "route_candidate_degraded")
	}
	return nil
}

// degradeToParentForHealth 在整条候选链都被动态健康门禁拦下时，把决策退回父
// Agent 的 provider/model。
//
// 这里刻意不改 decision.Provider/Model：它们在 Resolve 开头已初始化为父 Agent 的
// 取值，保持不动即为继承。父 Agent 此刻正在运行，是当前唯一可证可用的目标；
// 若连它也不健康，也没有更好的选择，但至少子任务还能推进——把子 Agent 的构造
// 直接判失败，只会让「某个后端凌晨挂了」升级成「所有委派都不可用」。
//
// profile 里的 max_tokens/timeout/temperature 仍然保留：它们是按难度调的预算，
// 与落在哪个 provider 无关。
func (r Resolver) degradeToParentForHealth(decision *RouteDecision) {
	decision.Source = SourceFallback
	decision.Warnings = append(decision.Warnings, "route_health_exhausted_parent")
	markFallback(decision, "route_health_exhausted_parent")
}

// applyProfileSettings applies the profile fields shared by every candidate of
// a route: only the target provider/model varies between candidates.
func applyProfileSettings(decision *RouteDecision, profile agentconfig.AICLISubagentRouteProfile) {
	if effort := ProfileReasoningEffort(profile); effort != "" {
		decision.ReasoningEffort = effort
	}
	if profile.MaxTokens > 0 {
		decision.MaxTokens = profile.MaxTokens
	}
	if profile.Timeout > 0 {
		decision.Timeout = profile.Timeout
	}
	if profile.Temperature != nil {
		value := *profile.Temperature
		decision.Temperature = &value
	}
}

func (r Resolver) applyExplicitOverrides(d *RouteDecision, task TaskHint) {
	cfg := r.Config
	if provider := strings.TrimSpace(task.Provider); provider != "" {
		if AllowExplicitProviderOverride(cfg) {
			resolvedProvider := r.resolveProviderName(provider)
			if overrideValueAllowed(provider, cfg.AllowedProviderOverrides, resolvedProvider) {
				nextProvider := firstNonEmptyString(resolvedProvider, provider)
				if strings.TrimSpace(task.Model) == "" && !strings.EqualFold(nextProvider, d.Provider) {
					d.Model = ""
				}
				d.Provider = nextProvider
				d.Source = SourceExplicitOverride
			} else {
				d.Warnings = append(d.Warnings, "explicit_provider_override_not_allowed")
			}
		} else {
			d.Warnings = append(d.Warnings, "explicit_provider_override_denied")
		}
	}
	if model := strings.TrimSpace(task.Model); model != "" {
		if AllowExplicitModelOverride(cfg) {
			if overrideValueAllowed(model, cfg.AllowedModelOverrides) {
				d.Model = model
				d.Source = SourceExplicitOverride
			} else {
				d.Warnings = append(d.Warnings, "explicit_model_override_not_allowed")
			}
		} else {
			d.Warnings = append(d.Warnings, "explicit_model_override_denied")
		}
	}
	if effort := NormalizeReasoningEffort(task.ReasoningEffort); effort != "" {
		if AllowExplicitReasoningOverride(cfg) {
			d.ReasoningEffort = effort
			d.Source = SourceExplicitOverride
		} else {
			d.Warnings = append(d.Warnings, "explicit_reasoning_override_denied")
		}
	}
	if task.BudgetTokens > 0 {
		if d.MaxTokens > 0 && task.BudgetTokens > d.MaxTokens {
			d.Warnings = append(d.Warnings, "budget_tokens_capped_by_route")
		} else {
			d.MaxTokens = task.BudgetTokens
		}
	}
	if task.Timeout > 0 {
		d.Timeout = task.Timeout
	}
}

func (r Resolver) resolveProviderName(provider string) string {
	provider = strings.TrimSpace(provider)
	if provider == "" || r.Catalog == nil {
		return ""
	}
	return strings.TrimSpace(r.Catalog.ResolveProviderName(provider))
}

func overrideValueAllowed(value string, allowlist []string, aliases ...string) bool {
	if len(allowlist) == 0 {
		return true
	}
	candidates := append([]string{strings.TrimSpace(value)}, aliases...)
	for _, allowed := range allowlist {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		for _, candidate := range candidates {
			if strings.EqualFold(strings.TrimSpace(candidate), allowed) {
				return true
			}
		}
	}
	return false
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (r Resolver) finalizeDecision(parent ParentDefaults, decision *RouteDecision) error {
	inherit := InheritParentWhenMissing(r.Config)
	if strings.TrimSpace(decision.Provider) == "" {
		if inherit {
			decision.Provider = strings.TrimSpace(parent.Provider)
			decision.Warnings = append(decision.Warnings, "provider_missing_inherited_parent")
			markFallback(decision, "provider_missing_inherited_parent")
		} else {
			return fmt.Errorf("subagent route missing provider")
		}
	}

	if r.Catalog != nil && strings.TrimSpace(decision.Provider) != "" {
		if resolved := strings.TrimSpace(r.Catalog.ResolveProviderName(decision.Provider)); resolved != "" {
			decision.Provider = resolved
		} else {
			decision.Warnings = append(decision.Warnings, "provider_unresolved")
			if inherit && strings.TrimSpace(parent.Provider) != "" {
				decision.Provider = strings.TrimSpace(parent.Provider)
				if strings.TrimSpace(parent.Model) != "" {
					decision.Model = strings.TrimSpace(parent.Model)
					decision.Warnings = append(decision.Warnings, "model_fallback_parent")
				}
				decision.Source = SourceFallback
				decision.Warnings = append(decision.Warnings, "provider_fallback_parent")
				markFallback(decision, "provider_unresolved_parent")
			} else {
				return fmt.Errorf("subagent route provider unavailable: %s", decision.Provider)
			}
		}
	}
	if strings.TrimSpace(decision.Model) == "" {
		if r.Catalog != nil && strings.TrimSpace(decision.Provider) != "" {
			if model := strings.TrimSpace(r.Catalog.DefaultModel(decision.Provider)); model != "" {
				decision.Model = model
				decision.Warnings = append(decision.Warnings, "model_default_provider")
			}
		}
	}
	if strings.TrimSpace(decision.Model) == "" {
		if inherit {
			decision.Model = strings.TrimSpace(parent.Model)
			decision.Warnings = append(decision.Warnings, "model_missing_inherited_parent")
			markFallback(decision, "model_missing_inherited_parent")
		} else {
			return fmt.Errorf("subagent route missing model")
		}
	}

	if err := r.validateResolvedModel(parent, decision); err != nil {
		return err
	}

	if err := r.applyReasoningCompatibility(decision); err != nil {
		return err
	}
	return nil
}

func (r Resolver) validateResolvedModel(parent ParentDefaults, decision *RouteDecision) error {
	if !ValidateModelCapabilities(r.Config) || r.Catalog == nil || decision == nil {
		return nil
	}
	if decision.Source == SourceDisabled || decision.Source == SourceParentInherit {
		return nil
	}
	provider := strings.TrimSpace(decision.Provider)
	model := strings.TrimSpace(decision.Model)
	if provider == "" || model == "" {
		return nil
	}
	supported, known := r.Catalog.SupportsModel(provider, model)
	if !known || supported {
		return nil
	}

	decision.Warnings = append(decision.Warnings, "model_unsupported")
	if InheritParentWhenMissing(r.Config) && strings.TrimSpace(parent.Provider) != "" && strings.TrimSpace(parent.Model) != "" {
		decision.Provider = strings.TrimSpace(parent.Provider)
		decision.Model = strings.TrimSpace(parent.Model)
		decision.Source = SourceFallback
		decision.Warnings = append(decision.Warnings, "model_fallback_parent")
		markFallback(decision, "model_unsupported_parent")
		return nil
	}
	return fmt.Errorf("subagent route model unavailable: %s/%s", provider, model)
}

func (r Resolver) applyReasoningCompatibility(decision *RouteDecision) error {
	if !ValidateModelCapabilities(r.Config) || r.Catalog == nil || decision == nil || decision.ReasoningEffort == "" {
		return nil
	}
	supported, known := r.Catalog.SupportsReasoningEffort(decision.Provider, decision.Model, decision.ReasoningEffort)
	if supported {
		return nil
	}
	if !known {
		decision.Warnings = append(decision.Warnings, "reasoning_effort_capability_unknown")
		return nil
	}

	switch UnsupportedReasoningPolicy(r.Config) {
	case UnsupportedReasoningFail:
		return fmt.Errorf("subagent route reasoning_effort unsupported: %s/%s reasoning_effort=%s",
			strings.TrimSpace(decision.Provider), strings.TrimSpace(decision.Model), strings.TrimSpace(decision.ReasoningEffort))
	case UnsupportedReasoningDowngrade:
		if downgraded, ok := r.downgradeReasoningEffort(decision.Provider, decision.Model, decision.ReasoningEffort); ok {
			decision.Warnings = append(decision.Warnings, "reasoning_effort_unsupported_downgraded")
			decision.ReasoningEffort = downgraded
			return nil
		}
		decision.Warnings = append(decision.Warnings, "reasoning_effort_unsupported_downgrade_unavailable")
	}

	decision.Warnings = append(decision.Warnings, "reasoning_effort_unsupported_ignored")
	decision.ReasoningEffort = ""
	return nil
}

func (r Resolver) downgradeReasoningEffort(provider, model, requested string) (string, bool) {
	if r.Catalog == nil {
		return "", false
	}
	efforts, known := r.Catalog.SupportedReasoningEfforts(provider, model)
	if !known || len(efforts) == 0 {
		return "", false
	}
	return closestLowerReasoningEffort(requested, efforts)
}

func closestLowerReasoningEffort(requested string, supported []string) (string, bool) {
	requestedRank, ok := reasoningEffortRank(requested)
	if !ok {
		return "", false
	}
	bestRank := -1
	best := ""
	for _, candidate := range supported {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		rank, ok := reasoningEffortRank(candidate)
		if !ok || rank > requestedRank || rank <= bestRank {
			continue
		}
		bestRank = rank
		best = candidate
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func reasoningEffortRank(raw string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "none":
		return 0, true
	case "low":
		return 1, true
	case "medium":
		return 2, true
	case "high":
		return 3, true
	case "xhigh", "max":
		return 4, true
	default:
		return 0, false
	}
}

func markFallback(decision *RouteDecision, reason string) {
	if decision == nil {
		return
	}
	decision.FallbackUsed = true
	if strings.TrimSpace(decision.FallbackReason) == "" {
		decision.FallbackReason = strings.TrimSpace(reason)
	}
}

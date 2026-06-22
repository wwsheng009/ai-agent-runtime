package modelrouting

import (
	"fmt"
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
	if err := ValidateConfig(r.Config); err != nil {
		return RouteDecision{}, err
	}

	decision := RouteDecision{
		DifficultyRationale: strings.TrimSpace(task.DifficultyRationale),
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
	decision.Warnings = append(decision.Warnings, difficultyWarnings...)

	profile, source, ok := routeProfileForTask(r.Config, task.Role, difficulty)
	if ok {
		applyProfile(&decision, profile)
		decision.Source = source
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
	if difficulty, ok := NormalizeDifficulty(task.Difficulty); ok {
		return difficulty, "explicit", nil, nil
	}

	warnings := []string{}
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
	if promoted := promotedDifficulty(task, difficulty); promoted != difficulty {
		warnings = append(warnings, "difficulty_promoted_by_heuristic")
		difficulty = promoted
		source = "inferred"
	}
	return difficulty, source, warnings, nil
}

func promotedDifficulty(task TaskHint, difficulty string) string {
	rank := difficultyRank(difficulty)
	role := NormalizeRole(task.Role)
	if (role == "verifier" || (role == "writer" && !task.ReadOnly)) && rank < difficultyRank(DifficultyNormal) {
		rank = difficultyRank(DifficultyNormal)
	}
	lowerGoal := strings.ToLower(task.Goal)
	for _, keyword := range []string{"security", "permission", "migration", "architecture", "provider", "protocol"} {
		if strings.Contains(lowerGoal, keyword) && rank < difficultyRank(DifficultyHard) {
			rank = difficultyRank(DifficultyHard)
			break
		}
	}
	return difficultyForRank(rank)
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

func routeProfileForTask(cfg *agentconfig.AICLISubagentRoutingConfig, role, difficulty string) (agentconfig.AICLISubagentRouteProfile, string, bool) {
	if cfg == nil {
		return agentconfig.AICLISubagentRouteProfile{}, "", false
	}
	role = NormalizeRole(role)
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
	for key, profile := range levels {
		normalized, ok := NormalizeDifficulty(key)
		if ok && normalized == difficulty {
			return profile, true
		}
	}
	return agentconfig.AICLISubagentRouteProfile{}, false
}

func applyProfile(decision *RouteDecision, profile agentconfig.AICLISubagentRouteProfile) {
	profileModel := strings.TrimSpace(profile.Model)
	if provider := strings.TrimSpace(profile.Provider); provider != "" {
		if profileModel == "" && !strings.EqualFold(provider, decision.Provider) {
			decision.Model = ""
		}
		decision.Provider = provider
	}
	if profileModel != "" {
		decision.Model = profileModel
	}
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

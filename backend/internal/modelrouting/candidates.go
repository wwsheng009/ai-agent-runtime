package modelrouting

import (
	"errors"
	"fmt"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
)

// routeCandidate is the normalized view of one entry in a route's target chain.
type routeCandidate struct {
	provider     string
	model        string
	availability string
	promptCache  *bool
	primary      bool
}

// routeTarget is the candidate the resolver settled on.
type routeTarget struct {
	Provider       string
	Model          string
	Availability   string
	Primary        bool
	FallbackReason string
}

// routeCandidates normalizes a profile into its ordered target chain. The
// primary target always occupies slot 0, so a profile that carries neither
// annotations nor candidates behaves exactly as it did before failover existed.
func routeCandidates(profile agentconfig.AICLISubagentRouteProfile, failover bool) []routeCandidate {
	candidates := []routeCandidate{{
		provider:     strings.TrimSpace(profile.Provider),
		model:        strings.TrimSpace(profile.Model),
		availability: profile.Availability,
		promptCache:  profile.PromptCache,
		primary:      true,
	}}
	if !failover {
		return candidates
	}
	for _, entry := range profile.Candidates {
		candidates = append(candidates, routeCandidate{
			provider:     strings.TrimSpace(entry.Provider),
			model:        strings.TrimSpace(entry.Model),
			availability: entry.Availability,
			promptCache:  entry.PromptCache,
		})
	}
	return candidates
}

// candidateSkipReason returns a non-empty reason when the candidate must be
// gated out; an empty result means the candidate is eligible.
//
// Only the availability and prompt-cache gates apply to the primary, so an
// existing config carrying no annotations keeps its legacy behavior of letting
// finalizeDecision repair an unresolvable primary. Fallback entries are new
// surface, so they must additionally resolve in the catalog: a failover must
// never land on a target that does not exist.
func (r Resolver) candidateSkipReason(candidate routeCandidate) string {
	if AvailabilityPolicy(r.Config) == AvailabilityPolicySkip {
		if availability, ok := NormalizeAvailability(candidate.availability); ok && availability == AvailabilityUnavailable {
			return "candidate_unavailable"
		}
	}
	if RequirePromptCache(r.Config) && candidate.promptCache != nil && !*candidate.promptCache {
		return "candidate_prompt_cache_required"
	}
	if candidate.primary {
		return ""
	}
	if candidate.provider == "" {
		return "candidate_provider_missing"
	}
	if r.Catalog != nil {
		if strings.TrimSpace(r.Catalog.ResolveProviderName(candidate.provider)) == "" {
			return "candidate_provider_unresolved"
		}
		if ValidateModelCapabilities(r.Config) && candidate.model != "" {
			if supported, known := r.Catalog.SupportsModel(candidate.provider, candidate.model); known && !supported {
				return "candidate_model_unsupported"
			}
		}
	}
	return ""
}

// errRouteHealthExhausted 表示整条候选链都被动态健康门禁拦下。
//
// 它必须与「配置缺陷导致的链耗尽」区分开：前者是暂态故障（provider 正在挂），
// 调用方应退回父 Agent；后者是运维把配置写错了，必须大声报错。用哨兵错误而不是
// 匹配描述文案，避免文案一改就悄悄改变行为。
var errRouteHealthExhausted = errors.New("route health exhausted")

// candidateHealthGate 查询动态健康源，判定该候选当前是否应被跳过。
//
// 只有 unhealthy 会被拦下；degraded 照常路由（与静态 availability: degraded 的
// 语义一致），healthy 以及「健康源没观测过这个目标」都不拦——未知不等于不可用，
// 否则新配的 provider 会因为还没被调用过而永远拿不到第一次调用，形成死锁。
//
// 门禁复用 availability_policy：运维显式写成 ignore，等于声明「不要依据观测健康
// 摘除候选」，动态健康也应一并让路，否则一个开关只关掉了半个行为。
func (r Resolver) candidateHealthGate(candidate routeCandidate, now time.Time) (state, reason string, gated bool) {
	if r.Health == nil {
		return "", "", false
	}
	health, ok := r.Health.Lookup(candidate.provider, candidate.model, now)
	if !ok {
		return "", "", false
	}
	if health.State != providerhealth.StateUnhealthy {
		return health.State, health.Reason, false
	}
	if AvailabilityPolicy(r.Config) != AvailabilityPolicySkip {
		return health.State, health.Reason, false
	}
	return health.State, health.Reason, true
}

// selectRouteTarget walks the ordered chain and returns the first eligible
// candidate together with the audited outcome of every entry.
//
// The whole chain is evaluated even after an eligible target is found. A
// fallback that cannot resolve is a configuration defect, and the cheapest
// moment to surface it is here - once, at construction - rather than at the
// moment the primary actually fails and the fallback is suddenly needed.
// Selection still stops at the first eligible entry, so the chosen target is
// identical to a short-circuiting walk.
//
// 静态门禁（availability 标注 / prompt cache / 可解析性）与动态健康门禁分开判定，
// 因为二者耗尽的含义不同：前者是配置缺陷，后者是暂态故障。混在一起会让「provider
// 凌晨挂掉」表现为一条看起来像配置写错的报错。
func (r Resolver) selectRouteTarget(profile agentconfig.AICLISubagentRouteProfile, label string) (*routeTarget, []RouteCandidateEvaluation, error) {
	candidates := routeCandidates(profile, RoutingFailoverEnabled(r.Config))
	evaluations := make([]RouteCandidateEvaluation, 0, len(candidates))
	now := time.Now()
	var selected *routeTarget
	healthBlocked := false
	for _, candidate := range candidates {
		evaluation := RouteCandidateEvaluation{
			Provider:     candidate.provider,
			Model:        candidate.model,
			Primary:      candidate.primary,
			Availability: candidate.availability,
			PromptCache:  candidate.promptCache,
		}
		if reason := r.candidateSkipReason(candidate); reason != "" {
			evaluation.SkipReason = reason
			evaluations = append(evaluations, evaluation)
			continue
		}
		state, healthReason, gated := r.candidateHealthGate(candidate, now)
		evaluation.HealthState = state
		evaluation.HealthReason = healthReason
		if gated {
			evaluation.SkipReason = "candidate_unhealthy"
			evaluations = append(evaluations, evaluation)
			healthBlocked = true
			continue
		}
		evaluation.Eligible = true
		evaluations = append(evaluations, evaluation)
		if selected != nil {
			continue
		}
		target := &routeTarget{
			Provider:     candidate.provider,
			Model:        candidate.model,
			Availability: candidate.availability,
			Primary:      candidate.primary,
		}
		if !candidate.primary {
			target.FallbackReason = "route_primary_gated"
		}
		selected = target
	}
	if selected != nil {
		return selected, evaluations, nil
	}
	if healthBlocked {
		return nil, evaluations, errRouteHealthExhausted
	}
	return nil, evaluations, fmt.Errorf(
		"subagent route %s has no eligible candidate: %s", label, describeSkippedCandidates(evaluations))
}

// hasGatedFallback reports whether the primary was selected while at least one
// fallback was gated out. Routing still works, but the fallback capacity the
// operator configured is not actually available, which is worth surfacing
// before it is needed.
func hasGatedFallback(evaluations []RouteCandidateEvaluation) bool {
	for _, evaluation := range evaluations {
		if !evaluation.Primary && !evaluation.Eligible {
			return true
		}
	}
	return false
}

// describeSkippedCandidates renders the exhausted chain so the operator can see
// exactly which annotation removed every target.
func describeSkippedCandidates(evaluations []RouteCandidateEvaluation) string {
	parts := make([]string, 0, len(evaluations))
	for _, evaluation := range evaluations {
		name := strings.TrimSpace(evaluation.Provider)
		model := strings.TrimSpace(evaluation.Model)
		switch {
		case name != "" && model != "":
			name += "/" + model
		case model != "":
			name = "(inherited provider)/" + model
		case name == "":
			name = "(inherited)"
		}
		parts = append(parts, name+"="+evaluation.SkipReason)
	}
	if len(parts) == 0 {
		return "chain is empty"
	}
	return strings.Join(parts, ", ")
}

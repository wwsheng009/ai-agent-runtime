package modelrouting

import (
	"strconv"
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	"github.com/wwsheng009/ai-agent-runtime/internal/providerhealth"
)

const (
	DifficultyEasy   = "easy"
	DifficultyNormal = "normal"
	DifficultyHard   = "hard"
	DifficultyExpert = "expert"
)

const (
	SourceDisabled         = "disabled"
	SourceExplicitOverride = "explicit_override"
	SourceRoleOverride     = "role_override"
	SourceDifficultyLevel  = "difficulty_level"
	SourceParentInherit    = "parent_inherit"
	SourceFallback         = "fallback"
	// SourceExplicitPromoted 表示显式声明的难度被启发式提升覆盖（G3）：档位取
	// 「显式值」与「提升值」的 rank 最大值，因此只会升、不会降。
	SourceExplicitPromoted = "explicit_promoted"
	// SourceFailoverCandidate means the configured primary was gated out and a
	// same-difficulty fallback candidate took over.
	SourceFailoverCandidate = "failover_candidate"
)

// promote_explicit_difficulty 三态（方案 §5.3）：
//   - off：回到历史行为，显式声明短路提升，不告警；
//   - warn：不改变档位，但把"本该提升"的事实写进 route_warnings；
//   - enforce（默认）：真正提升档位（单调，永不降级）。
const (
	PromoteExplicitOff     = "off"
	PromoteExplicitWarn    = "warn"
	PromoteExplicitEnforce = "enforce"
)

// NormalizePromoteExplicitMode 归一三态取值。空值返回 enforce：安全网默认生效，
// off/warn 是显式回退开关（见 docs/plan/task-difficulty-routing-audit-hardening-plan-20260921.md §5.3）。
func NormalizePromoteExplicitMode(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return PromoteExplicitEnforce, true
	}
	switch key {
	case PromoteExplicitOff, "false", "disabled", "none":
		return PromoteExplicitOff, true
	case PromoteExplicitWarn, "warning", "dry_run":
		return PromoteExplicitWarn, true
	case PromoteExplicitEnforce, "on", "true", "enabled":
		return PromoteExplicitEnforce, true
	default:
		return "", false
	}
}

// PromoteExplicitMode 返回生效的三态值；非法值按默认 enforce 处理（校验期已报错）。
func PromoteExplicitMode(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if cfg == nil {
		return PromoteExplicitEnforce
	}
	if mode, ok := NormalizePromoteExplicitMode(cfg.PromoteExplicitDifficulty); ok {
		return mode
	}
	return PromoteExplicitEnforce
}

// HeuristicsDisabled 报告关键词启发式是否被显式关闭。
func HeuristicsDisabled(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.Heuristics != nil && cfg.Heuristics.Disabled
}

// PromoteKeywords 返回生效的高信号词表（内置 + 配置追加）。
func PromoteKeywords(cfg *agentconfig.AICLISubagentRoutingConfig) []string {
	keywords := append([]string(nil), builtinPromoteKeywords...)
	if cfg != nil && cfg.Heuristics != nil {
		keywords = append(keywords, cfg.Heuristics.PromoteKeywords...)
	}
	return keywords
}

// PromoteComboKeywords 返回生效的弱信号词表（内置 + 配置追加）。
func PromoteComboKeywords(cfg *agentconfig.AICLISubagentRoutingConfig) []string {
	keywords := append([]string(nil), builtinPromoteComboKeywords...)
	if cfg != nil && cfg.Heuristics != nil {
		keywords = append(keywords, cfg.Heuristics.PromoteKeywordsCombo...)
	}
	return keywords
}

// ExpertLimitLabel 返回 expert 并发闸门的审计标签：闸门关闭（路由未启用、nil
// 配置、0 或 -1）时为 "unlimited"，否则是十进制上限。写入路由审计载荷后，
// "限流是否真的生效"永远可见（G6）。
func ExpertLimitLabel(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if !RoutingEnabled(cfg) {
		return "unlimited"
	}
	if cfg.MaxExpertConcurrency <= 0 {
		return "unlimited"
	}
	return strconv.Itoa(cfg.MaxExpertConcurrency)
}

const (
	CompatibilityPermissive = "permissive"
	CompatibilityStrict     = "strict"
)

const (
	UnsupportedReasoningIgnore    = "ignore"
	UnsupportedReasoningDowngrade = "downgrade"
	UnsupportedReasoningFail      = "fail"
)

// Availability annotations describe observed provider health, not declared
// capability: they exist so an empirically dead family can be kept out of the
// routing targets without deleting it from the config.
const (
	AvailabilityAvailable   = "available"
	AvailabilityDegraded    = "degraded"
	AvailabilityUnavailable = "unavailable"
)

const (
	AvailabilityPolicySkip   = "skip"
	AvailabilityPolicyIgnore = "ignore"
)

// RouteCandidateEvaluation records the gate outcome for one entry of a route's
// target chain, in the order the resolver considered them.
type RouteCandidateEvaluation struct {
	Provider     string `json:"provider,omitempty"`
	Model        string `json:"model,omitempty"`
	Primary      bool   `json:"primary,omitempty"`
	Eligible     bool   `json:"eligible"`
	SkipReason   string `json:"skip_reason,omitempty"`
	Availability string `json:"availability,omitempty"`
	PromptCache  *bool  `json:"prompt_cache,omitempty"`
	// HealthState 记录动态健康源对该候选的观测结论（未接入或未观测时为空），
	// HealthReason 是熔断原因。二者即使候选仍被选中也会写入：degraded 候选照常
	// 路由，但运维需要能看出它已经在抖动。
	HealthState  string `json:"health_state,omitempty"`
	HealthReason string `json:"health_reason,omitempty"`
}

// ParentDefaults describes the active parent agent settings used as fallback.
type ParentDefaults struct {
	Provider        string
	Model           string
	ReasoningEffort string
	MaxTokens       int
	Timeout         time.Duration
	Temperature     *float64
}

// TaskHint is the routing-relevant subset of a subagent task.
type TaskHint struct {
	ID                  string
	Role                string
	Goal                string
	Difficulty          string
	DifficultyRationale string
	Provider            string
	Model               string
	ReasoningEffort     string
	BudgetTokens        int
	Timeout             time.Duration
	ReadOnly            bool
	Warnings            []string
}

// RouteDecision is the audited result of resolving a child-agent route.
type RouteDecision struct {
	Difficulty          string
	DifficultySource    string
	DifficultyRationale string
	Provider            string
	Model               string
	ReasoningEffort     string
	MaxTokens           int
	Timeout             time.Duration
	Temperature         *float64
	Source              string
	Warnings            []string
	FallbackUsed        bool
	FallbackReason      string
	// Candidates is the audited gate outcome for every candidate the resolver
	// considered, including the ones it skipped.
	Candidates []RouteCandidateEvaluation
}

// ProviderCatalog exposes optional runtime knowledge to the resolver.
type ProviderCatalog interface {
	ResolveProviderName(name string) string
	DefaultModel(provider string) string
	SupportsModel(provider, model string) (supported bool, known bool)
	SupportsReasoningEffort(provider, model, effort string) (supported bool, known bool)
	SupportedReasoningEfforts(provider, model string) (efforts []string, known bool)
}

// Resolver maps task hints and local configuration to an effective route.
type Resolver struct {
	Config  *agentconfig.AICLISubagentRoutingConfig
	Catalog ProviderCatalog
	// Health 是 provider/model 粒度的动态健康源。nil 表示未接入：路由只依据
	// 静态标注，行为与引入健康门禁之前逐字一致，因此既有测试与不关心健康的
	// 调用方无需构造它。
	Health *providerhealth.Registry
}

func RoutingEnabled(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.Enabled != nil && *cfg.Enabled
}

// RoutingFailoverEnabled reports whether a route's candidate chain may be used.
// nil and true both enable it, so adding candidates to an existing config is
// enough to turn failover on.
func RoutingFailoverEnabled(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg == nil || cfg.Failover == nil || *cfg.Failover
}

// RequirePromptCache reports whether candidates annotated as not participating
// in prompt caching must be skipped.
func RequirePromptCache(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.RequirePromptCache
}

// NormalizeAvailability normalizes an availability annotation. An empty value
// means "available", so an unannotated candidate is always eligible.
func NormalizeAvailability(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return AvailabilityAvailable, true
	}
	switch key {
	case AvailabilityAvailable, "ok", "healthy", "up":
		return AvailabilityAvailable, true
	case AvailabilityDegraded, "warn", "warning", "flaky":
		return AvailabilityDegraded, true
	case AvailabilityUnavailable, "down", "error", "errored", "dead", "disabled":
		return AvailabilityUnavailable, true
	default:
		return "", false
	}
}

// NormalizeAvailabilityPolicy normalizes the availability policy. An empty
// value means "skip".
func NormalizeAvailabilityPolicy(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return AvailabilityPolicySkip, true
	}
	switch key {
	case AvailabilityPolicySkip, "exclude":
		return AvailabilityPolicySkip, true
	case AvailabilityPolicyIgnore, "keep", "legacy":
		return AvailabilityPolicyIgnore, true
	default:
		return "", false
	}
}

// AvailabilityPolicy returns the effective availability policy.
func AvailabilityPolicy(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if cfg == nil {
		return AvailabilityPolicySkip
	}
	if policy, ok := NormalizeAvailabilityPolicy(cfg.AvailabilityPolicy); ok {
		return policy
	}
	return AvailabilityPolicySkip
}

func AllowExplicitModelOverride(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.AllowExplicitModelOverride
}

func AllowExplicitProviderOverride(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.AllowExplicitProviderOverride
}

func AllowExplicitReasoningOverride(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.AllowExplicitReasoningOverride
}

func InheritParentWhenMissing(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	if cfg == nil || cfg.InheritParentWhenMissing == nil {
		return true
	}
	return *cfg.InheritParentWhenMissing
}

func ValidateModelCapabilities(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	if cfg == nil || cfg.ValidateModelCapabilities == nil {
		return true
	}
	return *cfg.ValidateModelCapabilities
}

func NormalizeCompatibilityMode(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return CompatibilityPermissive, true
	}
	switch key {
	case CompatibilityPermissive, CompatibilityStrict:
		return key, true
	default:
		return "", false
	}
}

func CompatibilityMode(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if cfg == nil {
		return CompatibilityPermissive
	}
	if mode, ok := NormalizeCompatibilityMode(cfg.CompatibilityMode); ok {
		return mode
	}
	return CompatibilityPermissive
}

func StrictCompatibilityMode(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return CompatibilityMode(cfg) == CompatibilityStrict
}

func NormalizeUnsupportedReasoningPolicy(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return UnsupportedReasoningIgnore, true
	}
	switch key {
	case UnsupportedReasoningIgnore, "warn", "clear":
		return UnsupportedReasoningIgnore, true
	case UnsupportedReasoningDowngrade:
		return UnsupportedReasoningDowngrade, true
	case UnsupportedReasoningFail, "reject":
		return UnsupportedReasoningFail, true
	default:
		return "", false
	}
}

func UnsupportedReasoningPolicy(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if cfg == nil {
		return UnsupportedReasoningIgnore
	}
	if strings.TrimSpace(cfg.UnsupportedReasoningPolicy) != "" {
		policy, ok := NormalizeUnsupportedReasoningPolicy(cfg.UnsupportedReasoningPolicy)
		if ok {
			return policy
		}
	}
	if strings.TrimSpace(cfg.OnReasoningUnsupported) != "" {
		policy, ok := NormalizeUnsupportedReasoningPolicy(cfg.OnReasoningUnsupported)
		if ok {
			return policy
		}
	}
	if policy, ok := NormalizeUnsupportedReasoningPolicy(""); ok && policy != "" {
		return policy
	}
	return UnsupportedReasoningIgnore
}

func DefaultDifficulty(cfg *agentconfig.AICLISubagentRoutingConfig) string {
	if cfg != nil {
		if difficulty, ok := NormalizeDifficulty(cfg.DefaultDifficulty); ok {
			return difficulty
		}
	}
	return DifficultyNormal
}

func NormalizeDifficulty(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", false
	}
	key = strings.NewReplacer("-", "_", " ", "_").Replace(key)
	switch key {
	case DifficultyEasy, "simple", "low", "trivial":
		return DifficultyEasy, true
	case DifficultyNormal, "medium", "standard", "default":
		return DifficultyNormal, true
	case DifficultyHard, "complex", "high", "difficult":
		return DifficultyHard, true
	case DifficultyExpert, "critical", "very_hard", "architectural":
		return DifficultyExpert, true
	default:
		return "", false
	}
}

func NormalizeRole(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func NormalizeReasoningEffort(raw string) string {
	return strings.TrimSpace(raw)
}

func ProfileReasoningEffort(profile agentconfig.AICLISubagentRouteProfile) string {
	if effort := NormalizeReasoningEffort(profile.ReasoningEffort); effort != "" {
		return effort
	}
	return NormalizeReasoningEffort(profile.ThinkingEffort)
}

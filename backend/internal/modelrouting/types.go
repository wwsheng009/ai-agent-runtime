package modelrouting

import (
	"strings"
	"time"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
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
)

const (
	CompatibilityPermissive = "permissive"
	CompatibilityStrict     = "strict"
)

const (
	UnsupportedReasoningIgnore    = "ignore"
	UnsupportedReasoningDowngrade = "downgrade"
	UnsupportedReasoningFail      = "fail"
)

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
}

func RoutingEnabled(cfg *agentconfig.AICLISubagentRoutingConfig) bool {
	return cfg != nil && cfg.Enabled != nil && *cfg.Enabled
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

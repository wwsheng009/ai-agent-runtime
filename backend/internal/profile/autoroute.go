package profile

import (
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
)

// FR-11（Batch 6）：`--profile auto` 的提示词自动路由。本文件是**唯一权威**：
// server（internal/api/runtimeapi）与 CLI（cmd/aicli/commands）共用同一份规则匹配与
// 兜底语义，禁止出现第二套解析逻辑。未配置 `profiles.auto` 时逐字保留历史
// 启发式（write→executor / plan→planner / search→explore，未命中→executor），
// 因此"从不开 auto"的既有部署零变化（NFR-1）。

const (
	// AutoProfileRef 是触发提示词路由的保留引用（trim + 大小写不敏感）。
	AutoProfileRef = "auto"
	// AutoRouteDefaultFallback 是规则未命中时的兜底 profile（与历史启发式一致）。
	AutoRouteDefaultFallback = "executor"
)

// AutoRouteRule 是一条"关键词 → profile"映射：规则内任一关键词命中即胜出，
// 规则之间按声明顺序短路（首个命中者胜）。
type AutoRouteRule struct {
	Profile  string
	Keywords []string
}

// AutoRouteConfig 是解析后的路由表（规则 + 兜底）。零值（Rules 空且 Fallback
// 空）等价于内置默认，保证"未配置即历史行为"。
type AutoRouteConfig struct {
	Rules    []AutoRouteRule
	Fallback string
}

// DefaultAutoRouteRules 返回内置映射（顺序敏感，关键词一律小写）。匹配前提示词
// 同样小写化，故整体大小写不敏感。
func DefaultAutoRouteRules() []AutoRouteRule {
	return []AutoRouteRule{
		{Profile: "executor", Keywords: []string{"write", "implement", "fix", "add", "edit", "refactor", "patch", "update", "change"}},
		{Profile: "planner", Keywords: []string{"plan", "break down", "design", "compare", "proposal", "approach"}},
		{Profile: "explore", Keywords: []string{"search", "inspect", "understand", "locate", "find", "investigate", "look up"}},
	}
}

// IsAutoProfileRef 判断 value 是否为保留的 auto 引用（trim + 大小写不敏感）。
func IsAutoProfileRef(value string) bool {
	return strings.EqualFold(strings.TrimSpace(value), AutoProfileRef)
}

// NewAutoRouteConfig 把配置段映射为路由表；未配置（nil）返回零值 —— 零值在
// RouteProfileForPrompt 中等价于内置默认，故不区分"未配置"与"空配置"。
func NewAutoRouteConfig(cfg *agentconfig.ProfilesConfig) AutoRouteConfig {
	if cfg == nil || cfg.Auto == nil {
		return AutoRouteConfig{}
	}
	routeCfg := AutoRouteConfig{Fallback: strings.TrimSpace(cfg.Auto.Fallback)}
	for _, rule := range cfg.Auto.Rules {
		routeCfg.Rules = append(routeCfg.Rules, AutoRouteRule{
			Profile:  strings.TrimSpace(rule.Profile),
			Keywords: append([]string(nil), rule.Keywords...),
		})
	}
	return routeCfg
}

// NormalizeAutoRouteRules 清洗规则：profile/keyword 去空白、关键词小写化、丢弃
// 空规则与空关键词，保持声明顺序。返回 nil 表示没有可用规则（调用方回落内置默认）。
func NormalizeAutoRouteRules(rules []AutoRouteRule) []AutoRouteRule {
	var out []AutoRouteRule
	for _, rule := range rules {
		profileRef := strings.TrimSpace(rule.Profile)
		if profileRef == "" {
			continue
		}
		keywords := make([]string, 0, len(rule.Keywords))
		for _, keyword := range rule.Keywords {
			keyword = strings.ToLower(strings.TrimSpace(keyword))
			if keyword == "" {
				continue
			}
			keywords = append(keywords, keyword)
		}
		if len(keywords) == 0 {
			continue
		}
		out = append(out, AutoRouteRule{Profile: profileRef, Keywords: keywords})
	}
	return out
}

// effectiveRules 返回本次路由生效的规则：显式配置优先，否则内置默认。
func (c AutoRouteConfig) effectiveRules() []AutoRouteRule {
	if rules := NormalizeAutoRouteRules(c.Rules); len(rules) > 0 {
		return rules
	}
	return DefaultAutoRouteRules()
}

// RouteProfileForPrompt 把提示词解析为 profile 引用：
//   - 提示词为空 → ""（不猜：调用方保留自身默认/兜底语义）；
//   - 首个命中规则胜出；未命中 → Fallback；Fallback 为空 → executor。
//
// 匹配语义与历史 server 实现逐字一致（小写化 + 子串包含，非词边界），以保证
// 开 auto 的既有部署行为不变；需要更精确的匹配请用 `profiles.auto.rules` 覆盖。
func RouteProfileForPrompt(prompt string, cfg AutoRouteConfig) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	if lower == "" {
		return ""
	}
	for _, rule := range cfg.effectiveRules() {
		if autoRouteContainsAny(lower, rule.Keywords) {
			return rule.Profile
		}
	}
	if fallback := strings.TrimSpace(cfg.Fallback); fallback != "" {
		return fallback
	}
	return AutoRouteDefaultFallback
}

// ResolveAutoProfileRef 是 auto 引用的统一解析入口：非 auto 引用原样返回
// (ref, false)；auto 引用按提示词路由并返回 (routed, true)。提示词缺失时
// routed 为空串，调用方必须显式处理该情形（禁止静默降级为某个猜测 profile）。
func ResolveAutoProfileRef(ref, prompt string, cfg AutoRouteConfig) (string, bool) {
	trimmed := strings.TrimSpace(ref)
	if !IsAutoProfileRef(trimmed) {
		return trimmed, false
	}
	return RouteProfileForPrompt(prompt, cfg), true
}

func autoRouteContainsAny(text string, keywords []string) bool {
	for _, keyword := range keywords {
		if keyword != "" && strings.Contains(text, keyword) {
			return true
		}
	}
	return false
}

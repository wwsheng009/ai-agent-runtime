// Package agentconfig provides configuration types for aicli and agent runtime.
// These types are extracted from ai-gateway/internal/config, keeping only agent/aicli relevant fields.
package agentconfig

import (
	"fmt"
	"math/rand"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"gopkg.in/yaml.v3"
)

// DatabaseConfig holds basic database connection info.
type DatabaseConfig struct {
	Driver string `yaml:"driver" mapstructure:"driver"`
	DSN    string `yaml:"dsn" mapstructure:"dsn"`
}

// Config holds the aicli-relevant subset of the gateway config.
type Config struct {
	Server         ServerConfig    `yaml:"server" mapstructure:"server"`
	Database       DatabaseConfig  `yaml:"database" mapstructure:"database"`
	Providers      ProvidersConfig `yaml:"providers" mapstructure:"providers"`
	ProviderGroups []ProviderGroup `yaml:"provider_groups" mapstructure:"provider_groups"`
	Retry          *RetryConfig    `yaml:"retry" mapstructure:"retry"`
	// CircuitBreaker 描述 provider 级健康熔断策略，是子 Agent 路由动态健康源
	// 的策略来源。该块此前只存在于配置模板中而无 Go 结构消费；缺失或字段不可
	// 解析时由 providerhealth 回落缺省值，不影响加载。
	CircuitBreaker *CircuitBreakerConfig `yaml:"circuit_breaker" mapstructure:"circuit_breaker"`
	AICLI          *AICLIConfig          `yaml:"aicli" mapstructure:"aicli"`
	Profiles       *ProfilesConfig       `yaml:"profiles" mapstructure:"profiles"`
	SkillsRuntime  *SkillsRuntimeConfig  `yaml:"skills_runtime" mapstructure:"skills_runtime"`
	Supervision    supervision.Config    `yaml:"supervision" mapstructure:"supervision"`
	Log            logger.LogConfig      `yaml:"log" mapstructure:"log"`
	ConfigFilePath string                `yaml:"-" mapstructure:"-"`
	// ConfigLayers, ConfigOrigins and ConfigMergeMode describe how this config
	// was assembled when layered merging is active. They are diagnostics only
	// and never participate in YAML decoding.
	ConfigLayers  []ConfigLayer     `yaml:"-" mapstructure:"-"`
	ConfigOrigins map[string]string `yaml:"-" mapstructure:"-"`
	// ConfigOriginFiles maps the same key paths to the layer file that supplied
	// them, which is what write routing (see config_write_route.go) needs.
	ConfigOriginFiles map[string]string `yaml:"-" mapstructure:"-"`
	ConfigMergeMode   MergeMode         `yaml:"-" mapstructure:"-"`
}

// ProvidersConfig holds the provider collection configuration.
type ProvidersConfig struct {
	DefaultProvider string            `yaml:"default_provider" mapstructure:"default_provider" env:"PROVIDERS_DEFAULT"`
	Headers         map[string]string `yaml:"headers" mapstructure:"headers"`
	Timeout         time.Duration     `yaml:"timeout" mapstructure:"timeout" env:"PROVIDERS_TIMEOUT"`
	MaxRetries      int               `yaml:"max_retries" mapstructure:"max_retries" env:"PROVIDERS_MAX_RETRIES"`
	// TransportMaxRetries bounds transport-layer retries (connection /
	// response-header timeout / TLS failures). Zero falls back to the default
	// (4); a negative value means unlimited. The response-header streak guard
	// is used only when no finite business or transport budget is available.
	TransportMaxRetries int                 `yaml:"transport_max_retries" mapstructure:"transport_max_retries" env:"PROVIDERS_TRANSPORT_MAX_RETRIES"`
	Backoff             BackoffConfig       `yaml:"backoff" mapstructure:"backoff"`
	HTTPTimeout         HTTPTimeout         `yaml:"http_timeout" mapstructure:"http_timeout"`
	Proxy               ProxyConfig         `yaml:"proxy" mapstructure:"proxy"`
	Items               map[string]Provider `yaml:"items" mapstructure:"items"`
}

// EffectiveProviderHeaders merges global and provider-specific request headers.
// Header names are matched case-insensitively, and provider values take priority.
func EffectiveProviderHeaders(globalHeaders, providerHeaders map[string]string) map[string]string {
	if len(globalHeaders) == 0 && len(providerHeaders) == 0 {
		return nil
	}

	merged := make(map[string]string, len(globalHeaders)+len(providerHeaders))
	merge := func(headers map[string]string) {
		for key, value := range headers {
			canonicalKey := textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(key))
			if canonicalKey == "" {
				canonicalKey = key
			}
			merged[canonicalKey] = value
		}
	}
	merge(globalHeaders)
	merge(providerHeaders)
	return merged
}

// BackoffConfig holds retry backoff configuration.
type BackoffConfig struct {
	InitialInterval time.Duration `yaml:"initial_interval" mapstructure:"initial_interval"`
	MaxInterval     time.Duration `yaml:"max_interval" mapstructure:"max_interval"`
	MaxElapsedTime  time.Duration `yaml:"max_elapsed_time" mapstructure:"max_elapsed_time"`
	Multiplier      float64       `yaml:"multiplier" mapstructure:"multiplier"`
	// Randomization jitters each backoff delay by ±Randomization*100%.
	// Unset (0) defaults to 0.1 (±10%, matching codex-rs); set a negative
	// value to disable jitter.
	Randomization float64         `yaml:"randomization" mapstructure:"randomization"`
	Schedule      []time.Duration `yaml:"schedule" mapstructure:"schedule"`
}

// RetryConfig holds fine-grained retry rule configuration.
type RetryConfig struct {
	Enabled                  bool              `yaml:"enabled" mapstructure:"enabled"`
	DefaultBackoffMultiplier float64           `yaml:"default_backoff_multiplier" mapstructure:"default_backoff_multiplier"`
	DefaultMaxRetries        int               `yaml:"default_max_retries" mapstructure:"default_max_retries"`
	DefaultRetryDelayMS      int               `yaml:"default_retry_delay_ms" mapstructure:"default_retry_delay_ms"`
	Rules                    []RetryRuleConfig `yaml:"rules" mapstructure:"rules"`
}

type RetryRuleConfig struct {
	Name              string                `yaml:"name" mapstructure:"name"`
	Description       string                `yaml:"description" mapstructure:"description"`
	Enabled           bool                  `yaml:"enabled" mapstructure:"enabled"`
	Action            string                `yaml:"action" mapstructure:"action"`
	MaxRetries        int                   `yaml:"max_retries" mapstructure:"max_retries"`
	RetryDelayMS      int                   `yaml:"retry_delay_ms" mapstructure:"retry_delay_ms"`
	BackoffMultiplier float64               `yaml:"backoff_multiplier" mapstructure:"backoff_multiplier"`
	Keyword           RetryKeywordConfig    `yaml:"keyword" mapstructure:"keyword"`
	ErrorCode         RetryErrorCodeConfig  `yaml:"error_code" mapstructure:"error_code"`
	StatusCode        RetryStatusCodeConfig `yaml:"status_code" mapstructure:"status_code"`
}

type RetryKeywordConfig struct {
	CaseSensitive bool     `yaml:"case_sensitive" mapstructure:"case_sensitive"`
	Values        []string `yaml:"values" mapstructure:"values"`
	Patterns      []string `yaml:"patterns" mapstructure:"patterns"`
}

type RetryErrorCodeConfig struct {
	Codes   []string `yaml:"codes" mapstructure:"codes"`
	Pattern string   `yaml:"pattern" mapstructure:"pattern"`
}

type RetryStatusCodeConfig struct {
	Codes []int  `yaml:"codes" mapstructure:"codes"`
	Range string `yaml:"range" mapstructure:"range"`
}

// HTTPTimeout holds HTTP timeout configuration.
type HTTPTimeout struct {
	DialTimeout              time.Duration `yaml:"dial_timeout" mapstructure:"dial_timeout"`
	TLSHandshakeTimeout      time.Duration `yaml:"tls_handshake_timeout" mapstructure:"tls_handshake_timeout"`
	ResponseHeaderTimeout    time.Duration `yaml:"response_header_timeout" mapstructure:"response_header_timeout"`
	BodyReadTimeout          time.Duration `yaml:"body_read_timeout" mapstructure:"body_read_timeout"`
	StreamReadTimeout        time.Duration `yaml:"stream_read_timeout" mapstructure:"stream_read_timeout"`
	IdleConnTimeout          time.Duration `yaml:"idle_conn_timeout" mapstructure:"idle_conn_timeout"`
	MaxIdleConns             int           `yaml:"max_idle_conns" mapstructure:"max_idle_conns"`
	MaxIdleConnsPerHost      int           `yaml:"max_idle_conns_per_host" mapstructure:"max_idle_conns_per_host"`
	MaxConnsPerHost          int           `yaml:"max_conns_per_host" mapstructure:"max_conns_per_host"`
	KeepAlive                time.Duration `yaml:"keep_alive" mapstructure:"keep_alive"`
	DisableConnectionPooling bool          `yaml:"disable_connection_pooling" mapstructure:"disable_connection_pooling"`
	FallbackDelay            time.Duration `yaml:"fallback_delay" mapstructure:"fallback_delay"`
	PreferIPv4               bool          `yaml:"prefer_ipv4" mapstructure:"prefer_ipv4"`
	DNSCacheTTL              time.Duration `yaml:"dns_cache_ttl" mapstructure:"dns_cache_ttl"`
	DNSServer                string        `yaml:"dns_server" mapstructure:"dns_server"`
	HTTPVersion              string        `yaml:"http_version" mapstructure:"http_version"`
	UpstreamAcceptEncoding   string        `yaml:"upstream_accept_encoding" mapstructure:"upstream_accept_encoding"`
}

// NativeToolCapabilities declares provider-native tool support for one model.
type NativeToolCapabilities struct {
	ImageGeneration      bool `yaml:"image_generation" mapstructure:"image_generation" json:"image_generation"`
	ImagesGenerationsAPI bool `yaml:"images_generations_api" mapstructure:"images_generations_api" json:"images_generations_api"`
}

// ModelCapabilitySpec declares per-model input modalities and native tool support.
type ModelCapabilitySpec struct {
	InputModalities []string               `yaml:"input_modalities" mapstructure:"input_modalities" json:"input_modalities"`
	NativeTools     NativeToolCapabilities `yaml:"native_tools" mapstructure:"native_tools" json:"native_tools"`
	// ReasoningModel 显式声明该模型是否属于 reasoning/thinking 模型。
	// 运行时不再根据 reasoning_efforts / budgets 做隐式推断。
	ReasoningModel bool `yaml:"reasoning_model" mapstructure:"reasoning_model" json:"reasoning_model"`
	// ReplayReasoningContent 显式声明该 (provider, model) 端点是否要求把
	// assistant 消息的 reasoning_content 回传给 API（thinking 模式的回传契约）。
	//
	// 该契约属于端点行为而不是厂商身份：第三方网关/聚合站即使名字和域名都
	// 不含 deepseek，只要后端是 thinking 模式，同样会因为缺少该字段而返回
	// HTTP 400。因此这里提供显式声明，优先级高于内置的名称启发式：
	//   - nil   = 未声明，回退到 provider/model 名称启发式（保持既有行为）；
	//   - true  = 强制回传（用于名字里不含 deepseek 的第三方站点）；
	//   - false = 禁止注入（用于名字里含 deepseek 但并不强制该契约的站点）。
	ReplayReasoningContent *bool          `yaml:"replay_reasoning_content" mapstructure:"replay_reasoning_content" json:"replay_reasoning_content,omitempty"`
	ReasoningEfforts       []string       `yaml:"reasoning_efforts" mapstructure:"reasoning_efforts" json:"reasoning_efforts"`
	ReasoningEffortBudgets map[string]int `yaml:"reasoning_effort_budgets" mapstructure:"reasoning_effort_budgets" json:"reasoning_effort_budgets"`
	// DefaultReasoningEffort 保留兼容字段；当前运行时不再依赖它做默认推断。
	DefaultReasoningEffort string  `yaml:"default_reasoning_effort" mapstructure:"default_reasoning_effort" json:"default_reasoning_effort"`
	MaxContextTokens       int     `yaml:"max_context_tokens" mapstructure:"max_context_tokens" json:"max_context_tokens"`
	MaxTokens              int     `yaml:"max_tokens" mapstructure:"max_tokens" json:"max_tokens"`
	AutoCompactRatio       float64 `yaml:"auto_compact_ratio" mapstructure:"auto_compact_ratio" json:"auto_compact_ratio"`
	AutoCompactTokenLimit  int     `yaml:"auto_compact_token_limit" mapstructure:"auto_compact_token_limit" json:"auto_compact_token_limit"`
	AutoCompactMode        string  `yaml:"auto_compact_mode" mapstructure:"auto_compact_mode" json:"auto_compact_mode"`
	SupportsRemoteCompact  bool    `yaml:"supports_remote_compact" mapstructure:"supports_remote_compact" json:"supports_remote_compact"`
	CompactReasoningEffort string  `yaml:"compact_reasoning_effort" mapstructure:"compact_reasoning_effort" json:"compact_reasoning_effort"`
}

// Provider holds provider configuration.
type Provider struct {
	Enabled            bool                           `yaml:"enabled" mapstructure:"enabled" json:"enabled"`
	Type               string                         `yaml:"type" mapstructure:"type" json:"type"`
	Protocol           string                         `yaml:"protocol" mapstructure:"protocol" json:"protocol"`
	Compatibility      CompatibilityConfig            `yaml:"compatibility,omitempty" mapstructure:"compatibility" json:"compatibility,omitempty"`
	BaseURL            string                         `yaml:"base_url" mapstructure:"base_url" json:"base_url"`
	APIPath            string                         `yaml:"api_path" mapstructure:"api_path" json:"api_path"`
	ForwardURL         string                         `yaml:"forward_url" mapstructure:"forward_url" json:"forward_url"`
	APIKey             string                         `yaml:"api_key" mapstructure:"api_key" json:"api_key"`
	APIKeys            []string                       `yaml:"api_keys" mapstructure:"api_keys" json:"api_keys"`
	APIKeyRef          string                         `yaml:"api_key_ref" mapstructure:"api_key_ref" json:"api_key_ref"`
	AuthMode           string                         `yaml:"auth_mode,omitempty" mapstructure:"auth_mode" json:"auth_mode,omitempty"`
	AuthRef            string                         `yaml:"auth_ref,omitempty" mapstructure:"auth_ref" json:"auth_ref,omitempty"`
	ModelsPath         string                         `yaml:"models_path,omitempty" mapstructure:"models_path" json:"models_path,omitempty"`
	ModelsVerifiedAt   string                         `yaml:"models_verified_at,omitempty" mapstructure:"models_verified_at" json:"models_verified_at,omitempty"`
	DefaultModel       string                         `yaml:"default_model" mapstructure:"default_model" json:"default_model"`
	SupportedModels    []string                       `yaml:"supported_models" mapstructure:"supported_models" json:"supported_models"`
	Headers            map[string]string              `yaml:"headers" mapstructure:"headers" json:"headers"`
	HeaderMappings     map[string]string              `yaml:"header_mappings" mapstructure:"header_mappings" json:"header_mappings"`
	HeaderMappingRules []HeaderMappingRule            `yaml:"header_mapping_rules" mapstructure:"header_mapping_rules" json:"header_mapping_rules"`
	SupportTypes       []string                       `yaml:"support_types" mapstructure:"support_types" json:"support_types"`
	ModelMappings      map[string]string              `yaml:"model_mappings" mapstructure:"model_mappings" json:"model_mappings"`
	ModelCapabilities  map[string]ModelCapabilitySpec `yaml:"model_capabilities" mapstructure:"model_capabilities" json:"model_capabilities"`
	// ResponseMarkerRules lists provider-scoped response marker strip rules.
	// Each rule matches a set of models (glob, case-insensitive) and strips
	// the configured literal markers from streamed assistant content before
	// tool-call parsing and history persistence.
	ResponseMarkerRules []ResponseMarkerRule `yaml:"response_marker_rules" mapstructure:"response_marker_rules" json:"response_marker_rules"`
	// EnableImageGeneration is the provider-level opt-in for Codex native
	// image_generation tool injection. When nil or false, requests never
	// auto-append image_generation even if a model capability advertises it.
	// Some third-party Codex-compatible sites reject that tool by default.
	EnableImageGeneration   *bool         `yaml:"enable_image_generation,omitempty" mapstructure:"enable_image_generation" json:"enable_image_generation,omitempty"`
	MaxTokensLimit          int           `yaml:"max_tokens_limit" mapstructure:"max_tokens_limit" json:"max_tokens_limit"`
	MaxToken                int           `yaml:"max_token" mapstructure:"max_token" json:"max_token"`
	SupportsMaxOutputTokens *bool         `yaml:"supports_max_output_tokens" mapstructure:"supports_max_output_tokens" json:"supports_max_output_tokens"`
	Timeout                 time.Duration `yaml:"timeout" mapstructure:"timeout" json:"timeout"`
	Proxy                   *ProxyConfig  `yaml:"proxy" mapstructure:"proxy" json:"proxy"`
	// RequestsPerMinute caps the number of provider API calls per rolling minute.
	// Zero means no client-side rate limiting.
	RequestsPerMinute int `yaml:"requests_per_minute" mapstructure:"requests_per_minute" json:"requests_per_minute"`

	// Site / account snapshot fields (best-effort cache; not billing authority).
	SiteType           string                   `yaml:"site_type,omitempty" mapstructure:"site_type" json:"site_type,omitempty"`
	SiteTypeConfidence string                   `yaml:"site_type_confidence,omitempty" mapstructure:"site_type_confidence" json:"site_type_confidence,omitempty"`
	SiteTypeDetectedAt string                   `yaml:"site_type_detected_at,omitempty" mapstructure:"site_type_detected_at" json:"site_type_detected_at,omitempty"`
	SiteTypeScores     map[string]int           `yaml:"site_type_scores,omitempty" mapstructure:"site_type_scores" json:"site_type_scores,omitempty"`
	AccountAuthRef     string                   `yaml:"account_auth_ref,omitempty" mapstructure:"account_auth_ref" json:"account_auth_ref,omitempty"`
	Account            *ProviderAccountSnapshot `yaml:"account,omitempty" mapstructure:"account" json:"account,omitempty"`
}

// ResponseMarkerRule describes one response marker strip rule: Models are
// model glob patterns (path.Match style, case-insensitive; empty matches all
// models of the provider) and Markers are literal tokens to strip from
// streamed assistant content (delta content and reasoning content) before
// tool-call markup parsing and history persistence.
type ResponseMarkerRule struct {
	Models  []string `yaml:"models" mapstructure:"models" json:"models"`
	Markers []string `yaml:"markers" mapstructure:"markers" json:"markers"`
}

// CompatibilityConfig selects a versioned, built-in wire dialect profile for a
// provider endpoint. It intentionally contains no arbitrary request rewrite
// expression: compatibility behavior remains reviewed Go code.
type CompatibilityConfig struct {
	Profile string `yaml:"profile,omitempty" mapstructure:"profile" json:"profile,omitempty"`
}

const (
	// CompatibilityProfileStandard explicitly opts out of non-standard wire
	// transformations. Empty has the same standard behavior until automatic
	// profile detection is introduced.
	CompatibilityProfileStandard = "standard"
	// CompatibilityProfileOpenCodeConsoleGo is the Console Go dialect observed
	// at the OpenCode endpoint in July 2026.
	CompatibilityProfileOpenCodeConsoleGo = "opencode-console-go-2026-07"
)

// ValidateCompatibilityProfile validates the explicitly selected wire dialect
// against the provider protocol. Empty means the standard protocol dialect.
func ValidateCompatibilityProfile(protocol, profile string) error {
	normalizedProfile := strings.ToLower(strings.TrimSpace(profile))
	switch normalizedProfile {
	case "", CompatibilityProfileStandard:
		return nil
	case CompatibilityProfileOpenCodeConsoleGo:
		switch strings.ToLower(strings.TrimSpace(protocol)) {
		case "openai", "codex":
			return nil
		default:
			return fmt.Errorf("profile %q requires protocol openai or codex", profile)
		}
	default:
		return fmt.Errorf("unknown profile %q", profile)
	}
}

// ProviderAccountSnapshot is a non-sensitive cached account/balance summary.
type ProviderAccountSnapshot struct {
	Source                 string                        `yaml:"source,omitempty" mapstructure:"source" json:"source,omitempty"`
	Mode                   string                        `yaml:"mode,omitempty" mapstructure:"mode" json:"mode,omitempty"`
	Currency               string                        `yaml:"currency,omitempty" mapstructure:"currency" json:"currency,omitempty"`
	WalletBalance          *float64                      `yaml:"wallet_balance,omitempty" mapstructure:"wallet_balance" json:"wallet_balance,omitempty"`
	IsAvailable            *bool                         `yaml:"is_available,omitempty" mapstructure:"is_available" json:"is_available,omitempty"`
	BalanceDetails         []ProviderBalanceDetail       `yaml:"balance_details,omitempty" mapstructure:"balance_details" json:"balance_details,omitempty"`
	QuotaBalance           *float64                      `yaml:"quota_balance,omitempty" mapstructure:"quota_balance" json:"quota_balance,omitempty"`
	QuotaRemaining         *float64                      `yaml:"quota_remaining,omitempty" mapstructure:"quota_remaining" json:"quota_remaining,omitempty"`
	QuotaUsed              *float64                      `yaml:"quota_used,omitempty" mapstructure:"quota_used" json:"quota_used,omitempty"`
	QuotaLimit             *float64                      `yaml:"quota_limit,omitempty" mapstructure:"quota_limit" json:"quota_limit,omitempty"`
	QuotaDisplayType       string                        `yaml:"quota_display_type,omitempty" mapstructure:"quota_display_type" json:"quota_display_type,omitempty"`
	QuotaDisplayUnit       string                        `yaml:"quota_display_unit,omitempty" mapstructure:"quota_display_unit" json:"quota_display_unit,omitempty"`
	QuotaDisplayScale      *float64                      `yaml:"quota_display_scale,omitempty" mapstructure:"quota_display_scale" json:"quota_display_scale,omitempty"`
	PlanName               string                        `yaml:"plan_name,omitempty" mapstructure:"plan_name" json:"plan_name,omitempty"`
	ExternalUserID         string                        `yaml:"external_user_id,omitempty" mapstructure:"external_user_id" json:"external_user_id,omitempty"`
	ExternalUsernameMasked string                        `yaml:"external_username_masked,omitempty" mapstructure:"external_username_masked" json:"external_username_masked,omitempty"`
	Subscriptions          []ProviderAccountSubscription `yaml:"subscriptions,omitempty" mapstructure:"subscriptions" json:"subscriptions,omitempty"`
	Usage                  *ProviderAccountUsage         `yaml:"usage,omitempty" mapstructure:"usage" json:"usage,omitempty"`
	FetchedAt              string                        `yaml:"fetched_at,omitempty" mapstructure:"fetched_at" json:"fetched_at,omitempty"`
	Partial                bool                          `yaml:"partial,omitempty" mapstructure:"partial" json:"partial,omitempty"`
	LastError              string                        `yaml:"last_error,omitempty" mapstructure:"last_error" json:"last_error,omitempty"`
}

// ProviderBalanceDetail preserves a provider's per-currency balance breakdown.
type ProviderBalanceDetail struct {
	Currency        string  `yaml:"currency" mapstructure:"currency" json:"currency"`
	TotalBalance    float64 `yaml:"total_balance" mapstructure:"total_balance" json:"total_balance"`
	GrantedBalance  float64 `yaml:"granted_balance" mapstructure:"granted_balance" json:"granted_balance"`
	ToppedUpBalance float64 `yaml:"topped_up_balance" mapstructure:"topped_up_balance" json:"topped_up_balance"`
}

// ProviderAccountSubscription is a compact subscription cache entry.
type ProviderAccountSubscription struct {
	Name      string   `yaml:"name,omitempty" mapstructure:"name" json:"name,omitempty"`
	Status    string   `yaml:"status,omitempty" mapstructure:"status" json:"status,omitempty"`
	Remaining *float64 `yaml:"remaining,omitempty" mapstructure:"remaining" json:"remaining,omitempty"`
	PeriodEnd string   `yaml:"period_end,omitempty" mapstructure:"period_end" json:"period_end,omitempty"`
}

// ProviderAccountUsage is a compact usage cache entry.
type ProviderAccountUsage struct {
	TotalRequests *int64   `yaml:"total_requests,omitempty" mapstructure:"total_requests" json:"total_requests,omitempty"`
	TotalCost     *float64 `yaml:"total_cost,omitempty" mapstructure:"total_cost" json:"total_cost,omitempty"`
	TodayRequests *int64   `yaml:"today_requests,omitempty" mapstructure:"today_requests" json:"today_requests,omitempty"`
	TodayCost     *float64 `yaml:"today_cost,omitempty" mapstructure:"today_cost" json:"today_cost,omitempty"`
}

// AllowsCodexImageGeneration reports whether this provider explicitly opts into
// Codex native image_generation. Unset/false means disabled (default off).
func (p Provider) AllowsCodexImageGeneration() bool {
	return p.EnableImageGeneration != nil && *p.EnableImageGeneration
}

// HeaderMappingRule defines a conditional header rewrite rule.
type HeaderMappingRule struct {
	Name         string `yaml:"name" mapstructure:"name" json:"name"`
	Enabled      *bool  `yaml:"enabled" mapstructure:"enabled" json:"enabled"`
	Header       string `yaml:"header" mapstructure:"header" json:"header"`
	TargetHeader string `yaml:"target_header" mapstructure:"target_header" json:"target_header"`
	MatchType    string `yaml:"match_type" mapstructure:"match_type" json:"match_type"`
	Match        string `yaml:"match" mapstructure:"match" json:"match"`
	Value        string `yaml:"value" mapstructure:"value" json:"value"`
}

// GetType returns the provider type (alias for GetProtocol for compatibility).
func (p *Provider) GetType() string { return p.GetProtocol() }

// GetProtocol returns the provider's normalized protocol type.
func (p *Provider) GetProtocol() string {
	if p.Protocol != "" {
		return strings.ToLower(strings.TrimSpace(p.Protocol))
	}
	if p.Type != "" {
		return strings.ToLower(strings.TrimSpace(p.Type))
	}
	return ""
}

// GetAPIPath returns the API path prefix.
func (p *Provider) GetAPIPath() string {
	return p.APIPath
}

// GetMaxTokensLimit returns the preferred max-token budget for provider requests.
// max_token is treated as the new preferred alias, while max_tokens_limit remains
// as a backward-compatible fallback.
func (p *Provider) GetMaxTokensLimit() int {
	if p == nil {
		return 0
	}
	if p.MaxToken > 0 {
		return p.MaxToken
	}
	if p.MaxTokensLimit > 0 {
		return p.MaxTokensLimit
	}
	return 0
}

// ProxyConfig holds proxy configuration.
type ProxyConfig struct {
	HTTP    string `yaml:"http" mapstructure:"http" env:"HTTP_PROXY" json:"http"`
	HTTPS   string `yaml:"https" mapstructure:"https" env:"HTTPS_PROXY" json:"https"`
	NoProxy string `yaml:"no_proxy" mapstructure:"no_proxy" env:"NO_PROXY" json:"no_proxy"`
	Enabled bool   `yaml:"enabled" mapstructure:"enabled" env:"PROXY_ENABLED" json:"enabled"`
}

// Clone returns a detached copy of the proxy config.
func (c *ProxyConfig) Clone() *ProxyConfig {
	if c == nil {
		return nil
	}
	cloned := *c
	return &cloned
}

// IsEmpty reports whether the proxy config is empty.
func (c *ProxyConfig) IsEmpty() bool {
	return c.HTTP == "" && c.HTTPS == "" && !c.Enabled
}

// String returns a string representation of the proxy config (passwords masked).
func (c *ProxyConfig) String() string {
	if c == nil || !c.Enabled {
		return "disabled"
	}
	if c.HTTP == "" && c.HTTPS == "" {
		return "disabled"
	}
	var parts []string
	if c.HTTP != "" {
		parts = append(parts, fmt.Sprintf("http=%s", maskProxyURL(c.HTTP)))
	}
	if c.HTTPS != "" {
		parts = append(parts, fmt.Sprintf("https=%s", maskProxyURL(c.HTTPS)))
	}
	if c.NoProxy != "" {
		parts = append(parts, fmt.Sprintf("no_proxy=%s", c.NoProxy))
	}
	return strings.Join(parts, ", ")
}

// Merge merges two proxy configs, other takes precedence.
func (c *ProxyConfig) Merge(other *ProxyConfig) *ProxyConfig {
	if other == nil {
		return c
	}
	result := *c
	if other.HTTP != "" {
		result.HTTP = other.HTTP
	}
	if other.HTTPS != "" {
		result.HTTPS = other.HTTPS
	}
	if other.NoProxy != "" {
		result.NoProxy = other.NoProxy
	}
	if other.Enabled {
		result.Enabled = true
	}
	return &result
}

// EffectiveProxyConfig clones and merges global/provider proxy settings.
// When both sides are empty and disabled, it returns nil so callers can
// continue falling back to environment proxy behavior.
func EffectiveProxyConfig(base *ProxyConfig, override *ProxyConfig) *ProxyConfig {
	switch {
	case base == nil && override == nil:
		return nil
	case base == nil:
		if override == nil {
			return nil
		}
		if override.IsEmpty() && override.NoProxy == "" {
			return nil
		}
		return override.Clone()
	case override == nil:
		if base.IsEmpty() && base.NoProxy == "" {
			return nil
		}
		return base.Clone()
	default:
		result := base.Merge(override)
		if result == nil {
			return nil
		}
		if result.IsEmpty() && result.NoProxy == "" {
			return nil
		}
		return result
	}
}

// ProxyFromEnv creates a ProxyConfig from environment variables.
func ProxyFromEnv() *ProxyConfig {
	return &ProxyConfig{
		HTTP:    os.Getenv("HTTP_PROXY"),
		HTTPS:   os.Getenv("HTTPS_PROXY"),
		NoProxy: os.Getenv("NO_PROXY"),
		Enabled: os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "",
	}
}

func maskProxyURL(proxyURL string) string {
	parsedURL, err := url.Parse(proxyURL)
	if err != nil {
		return proxyURL
	}
	if parsedURL.User != nil {
		username := parsedURL.User.Username()
		_, hasPassword := parsedURL.User.Password()
		if hasPassword {
			parsedURL.User = url.UserPassword(username, "****")
		}
	}
	return parsedURL.String()
}

// AICLIConfig holds aicli configuration.
type AICLIConfig struct {
	MCP        *AICLIMCPConfig        `yaml:"mcp" mapstructure:"mcp"`
	Log        *AICLILogConfig        `yaml:"log" mapstructure:"log"`
	Retry      *AICLIRetryConfig      `yaml:"retry" mapstructure:"retry"`
	Timeout    *AICLITimeoutConfig    `yaml:"timeout" mapstructure:"timeout"`
	Balance    *AICLIBalanceConfig    `yaml:"balance" mapstructure:"balance"`
	Theme      *AICLIThemeConfig      `yaml:"theme" mapstructure:"theme"`
	Chat       *AICLIChatConfig       `yaml:"chat" mapstructure:"chat"`
	Runtime    *AICLIRuntimeConfig    `yaml:"runtime" mapstructure:"runtime"`
	ModelCards *AICLIModelCardsConfig `yaml:"model_cards" mapstructure:"model_cards"`
	Subagents  *AICLISubagentsConfig  `yaml:"subagents" mapstructure:"subagents"`
	Teams      *AICLITeamsConfig      `yaml:"teams" mapstructure:"teams"`
	// MainAgent 承载主 Agent 的动态 provider/model 切换配置（方案
	// main-agent-dynamic-provider-model-switching-plan-20260921 §6.1）。
	// 独立配置节：与 aicli.subagents.routing 互不干扰，默认关闭。
	MainAgent *AICLIMainAgentConfig `yaml:"main_agent" mapstructure:"main_agent"`
}

// AICLIMCPConfig holds aicli MCP configuration.
type AICLIMCPConfig struct {
	ConfigFile string `yaml:"config_file" mapstructure:"config_file" env:"MCP_CONFIG_FILE"`
}

// AICLILogConfig holds aicli log configuration.
type AICLILogConfig struct {
	Enabled  *bool  `yaml:"enabled" mapstructure:"enabled" env:"AICLI_LOG_ENABLED"`
	FilePath string `yaml:"file_path" mapstructure:"file_path" env:"AICLI_LOG_FILE_PATH"`
}

// AICLIRetryConfig holds aicli retry configuration.
type AICLIRetryConfig struct {
	MaxTotalTime      time.Duration `yaml:"max_total_time" mapstructure:"max_total_time"`
	FastRetryCount    int           `yaml:"fast_retry_count" mapstructure:"fast_retry_count"`
	FastRetryInterval time.Duration `yaml:"fast_retry_interval" mapstructure:"fast_retry_interval"`
	SlowRetryInterval time.Duration `yaml:"slow_retry_interval" mapstructure:"slow_retry_interval"`
}

// AICLITimeoutConfig holds aicli timeout configuration.
type AICLITimeoutConfig struct {
	RequestTimeout time.Duration `yaml:"request_timeout" mapstructure:"request_timeout"`
}

const DefaultAICLIBalanceRefreshInterval = time.Minute

// AICLIBalanceConfig controls live account balance updates in interactive chat.
type AICLIBalanceConfig struct {
	RefreshInterval time.Duration `yaml:"refresh_interval" mapstructure:"refresh_interval" env:"AICLI_BALANCE_REFRESH_INTERVAL"`
}

// EffectiveAICLIBalanceRefreshInterval returns the configured refresh interval.
// Missing and non-positive values use the one-minute default.
func EffectiveAICLIBalanceRefreshInterval(cfg *Config) time.Duration {
	if cfg == nil || cfg.AICLI == nil || cfg.AICLI.Balance == nil ||
		cfg.AICLI.Balance.RefreshInterval <= 0 {
		return DefaultAICLIBalanceRefreshInterval
	}
	return cfg.AICLI.Balance.RefreshInterval
}

// AICLIThemeConfig holds aicli terminal theme preferences.
//
// Three axes (Codex-inspired):
//   - Name: palette / color scheme (classic|focus|contrast|mono|custom-*)
//   - Mode: light/dark preference (auto|dark|light)
//   - Syntax: Chroma syntax theme name (monokai|dracula|...)
type AICLIThemeConfig struct {
	Name   string `yaml:"name" mapstructure:"name" env:"AICLI_THEME"`
	Mode   string `yaml:"mode,omitempty" mapstructure:"mode" env:"AICLI_THEME_MODE"`
	Syntax string `yaml:"syntax,omitempty" mapstructure:"syntax" env:"AICLI_THEME_SYNTAX"`
}

// AICLIChatConfig holds aicli chat preference defaults.
type AICLIChatConfig struct {
	DefaultProvider string `yaml:"default_provider,omitempty" mapstructure:"default_provider"`
	DefaultModel    string `yaml:"default_model,omitempty" mapstructure:"default_model"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
	// Stream 记录用户偏好的输出模式（流式/普通）。使用指针以便区分“未配置”与“显式 false”。
	Stream *bool `yaml:"stream,omitempty" mapstructure:"stream"`
	// FastMode 记录 Codex 协议下的 Fast（service_tier=priority）偏好。
	// 使用指针以便区分“未配置”与“显式 false”；仅对 protocol=codex 生效。
	FastMode      *bool                     `yaml:"fast_mode,omitempty" mapstructure:"fast_mode"`
	TerminalTitle *AICLITerminalTitleConfig `yaml:"terminal_title,omitempty" mapstructure:"terminal_title"`
	Notifications *AICLIChatNotifications   `yaml:"notifications,omitempty" mapstructure:"notifications"`
	// Routing 仅在工作区偏好文件（chat-prefs.yaml）中有意义（方案 §3.3）。
	// 全局配置中的 aicli.chat.routing 不参与解析——解析器只读
	// aicli.main_agent.routing / aicli.subagents.routing（见 routing_resolution.go）。
	Routing *AICLIWorkspaceRoutingPreferences `yaml:"routing,omitempty" mapstructure:"routing"`
}

// AICLITerminalTitleConfig controls the interactive chat window/tab title.
type AICLITerminalTitleConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty" mapstructure:"enabled"`
	Animations *bool    `yaml:"animations,omitempty" mapstructure:"animations"`
	Items      []string `yaml:"items,omitempty" mapstructure:"items"`
}

// AICLIChatNotifications controls interactive attention signals.
type AICLIChatNotifications struct {
	Sound *AICLIChatSoundConfig `yaml:"sound,omitempty" mapstructure:"sound"`
	// Condition controls when chat attention signals fire.
	// Supported values: "unfocused" (default, Codex-aligned) and "always".
	Condition string `yaml:"condition,omitempty" mapstructure:"condition"`
}

// AICLIChatSoundConfig controls the lightweight terminal bell notification.
type AICLIChatSoundConfig struct {
	Enabled    *bool    `yaml:"enabled,omitempty" mapstructure:"enabled"`
	Events     []string `yaml:"events,omitempty" mapstructure:"events"`
	CooldownMS *int     `yaml:"cooldown_ms,omitempty" mapstructure:"cooldown_ms"`
}

// AICLIRuntimeConfig controls whether aicli executes turns locally or via runtime-server.
type AICLIRuntimeConfig struct {
	Mode      string `yaml:"mode,omitempty" mapstructure:"mode" env:"AICLI_RUNTIME_MODE"`
	ServerURL string `yaml:"server_url,omitempty" mapstructure:"server_url" env:"AICLI_RUNTIME_SERVER_URL"`
}

// AICLIModelCardsConfig controls model card catalog loading for provider login.
type AICLIModelCardsConfig struct {
	Enabled     *bool  `yaml:"enabled" mapstructure:"enabled"`
	BuiltinPath string `yaml:"builtin_path" mapstructure:"builtin_path"`
	UserPath    string `yaml:"user_path" mapstructure:"user_path"`
	Strict      bool   `yaml:"strict" mapstructure:"strict"`
}

// AICLISubagentsConfig holds subagent execution preferences.
type AICLISubagentsConfig struct {
	Routing *AICLISubagentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

// AICLITeamsConfig holds team teammate execution preferences. When routing is
// absent, team tasks inherit aicli.subagents.routing for backward compatibility.
type AICLITeamsConfig struct {
	Routing *AICLISubagentRoutingConfig `yaml:"routing" mapstructure:"routing"`
}

// EffectiveTeamRoutingConfig returns the team-specific routing policy when it
// exists and otherwise falls back to the shared subagent routing policy.
func EffectiveTeamRoutingConfig(cfg *Config) *AICLISubagentRoutingConfig {
	if cfg == nil || cfg.AICLI == nil {
		return nil
	}
	if cfg.AICLI.Teams != nil && cfg.AICLI.Teams.Routing != nil {
		return cfg.AICLI.Teams.Routing
	}
	if cfg.AICLI.Subagents == nil {
		return nil
	}
	return cfg.AICLI.Subagents.Routing
}

// AICLISubagentRoutingConfig maps subtask difficulty and role to model routes.
type AICLISubagentRoutingConfig struct {
	Enabled                        *bool    `yaml:"enabled" mapstructure:"enabled"`
	CompatibilityMode              string   `yaml:"compatibility_mode" mapstructure:"compatibility_mode"`
	DefaultDifficulty              string   `yaml:"default_difficulty" mapstructure:"default_difficulty"`
	AllowExplicitProviderOverride  bool     `yaml:"allow_explicit_provider_override" mapstructure:"allow_explicit_provider_override"`
	AllowExplicitModelOverride     bool     `yaml:"allow_explicit_model_override" mapstructure:"allow_explicit_model_override"`
	AllowExplicitReasoningOverride bool     `yaml:"allow_explicit_reasoning_override" mapstructure:"allow_explicit_reasoning_override"`
	AllowedProviderOverrides       []string `yaml:"allowed_provider_overrides" mapstructure:"allowed_provider_overrides"`
	AllowedModelOverrides          []string `yaml:"allowed_model_overrides" mapstructure:"allowed_model_overrides"`
	InheritParentWhenMissing       *bool    `yaml:"inherit_parent_when_missing" mapstructure:"inherit_parent_when_missing"`
	ValidateModelCapabilities      *bool    `yaml:"validate_model_capabilities" mapstructure:"validate_model_capabilities"`
	UnsupportedReasoningPolicy     string   `yaml:"unsupported_reasoning_policy" mapstructure:"unsupported_reasoning_policy"`
	OnReasoningUnsupported         string   `yaml:"on_reasoning_unsupported" mapstructure:"on_reasoning_unsupported"`
	MaxExpertConcurrency           int      `yaml:"max_expert_concurrency" mapstructure:"max_expert_concurrency"`
	// PromoteExplicitDifficulty 控制启发式提升与显式难度声明的交互（G3）：
	// "off" 回到历史行为（显式声明短路提升）；"warn" 只写告警不改档；
	// "enforce"（默认）真正按 rank 最大值提升，且永不降级。
	PromoteExplicitDifficulty string `yaml:"promote_explicit_difficulty" mapstructure:"promote_explicit_difficulty"`
	// Heuristics 是可追加的启发式词表（G4）。nil 表示只用内置词表。
	Heuristics *AICLISubagentRoutingHeuristics `yaml:"heuristics" mapstructure:"heuristics"`
	// Failover 控制难度级别的候选链是否生效：nil/true 启用，显式 false 只保留主选。
	// 候选链在子 Agent 构造时一次性解析完成，因此不会引入请求级改道。
	Failover *bool `yaml:"failover" mapstructure:"failover"`
	// AvailabilityPolicy 控制被标注为不可用的候选如何处置：
	// "skip"（默认）跳过该候选并前移到下一个；"ignore" 保持历史行为（照常使用）。
	AvailabilityPolicy string `yaml:"availability_policy" mapstructure:"availability_policy"`
	// RequirePromptCache 为 true 时，显式标注 prompt_cache=false 的候选会被跳过。
	// 用于把实测不参与 prompt 缓存的免费档排除出以缓存经济性为前提的路由目标。
	RequirePromptCache bool                                 `yaml:"require_prompt_cache" mapstructure:"require_prompt_cache"`
	Levels             map[string]AICLISubagentRouteProfile `yaml:"levels" mapstructure:"levels"`
	// TaskTypes 是 v4 的路由覆盖表：task_type → difficulty → profile（plan K-3）。
	// Roles 保留为兼容别名（保留一个 release），读入时映射到 TaskTypes 并给
	// deprecation warning；显式 TaskTypes 优先。键归一语义见 modelrouting。
	TaskTypes map[string]map[string]AICLISubagentRouteProfile `yaml:"task_types" mapstructure:"task_types"`
	Roles     map[string]map[string]AICLISubagentRouteProfile `yaml:"roles" mapstructure:"roles"`
}

// AICLISubagentRoutingHeuristics 是可追加的启发式词表配置（G4）。
// 词表为**追加**语义：内置词表始终生效，这里补充的条目只在命中时多产生一条
// route_warnings，不会替换内置词表。Disabled 关闭全部关键词启发式（角色提升
// 仍然生效），用于排除误报。
type AICLISubagentRoutingHeuristics struct {
	Disabled             bool     `yaml:"disabled" mapstructure:"disabled"`
	PromoteKeywords      []string `yaml:"promote_keywords" mapstructure:"promote_keywords"`
	PromoteKeywordsCombo []string `yaml:"promote_keywords_combo" mapstructure:"promote_keywords_combo"`
}

// AICLISubagentRouteProfile defines the runtime settings for one route.
type AICLISubagentRouteProfile struct {
	Provider        string        `yaml:"provider,omitempty" mapstructure:"provider"`
	Model           string        `yaml:"model,omitempty" mapstructure:"model"`
	ReasoningEffort string        `yaml:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
	ThinkingEffort  string        `yaml:"thinking_effort,omitempty" mapstructure:"thinking_effort"`
	MaxTokens       int           `yaml:"max_tokens,omitempty" mapstructure:"max_tokens"`
	Timeout         time.Duration `yaml:"timeout,omitempty" mapstructure:"timeout"`
	Temperature     *float64      `yaml:"temperature,omitempty" mapstructure:"temperature"`
	// Availability 标注主选的实测可用性：available（默认）、degraded、unavailable。
	Availability string `yaml:"availability,omitempty" mapstructure:"availability"`
	// AvailabilityReason 记录该标注的依据，便于审计与 doctor 输出。
	AvailabilityReason string `yaml:"availability_reason,omitempty" mapstructure:"availability_reason"`
	// PromptCache 标注主选后端是否参与 prompt 缓存；nil 表示未知（视为可用）。
	PromptCache *bool `yaml:"prompt_cache,omitempty" mapstructure:"prompt_cache"`
	// Candidates 是同一难度级别的有序后备链（不含主选）。仅当主选或更靠前的候选
	// 被可用性/缓存门禁拦下时才生效，因此未配置时行为与历史完全一致。
	Candidates []AICLISubagentRouteCandidate `yaml:"candidates,omitempty" mapstructure:"candidates"`
}

// AICLISubagentRouteCandidate is one ordered fallback entry of a route.
// The route's primary target lives on the profile itself; candidates are only
// consulted when an earlier entry is gated out by the availability or prompt
// cache annotation.
type AICLISubagentRouteCandidate struct {
	Provider string `yaml:"provider,omitempty" mapstructure:"provider"`
	Model    string `yaml:"model,omitempty" mapstructure:"model"`
	// Availability annotates observed provider health:
	// "available" (default), "degraded", "unavailable".
	Availability string `yaml:"availability,omitempty" mapstructure:"availability"`
	// AvailabilityReason records the evidence behind the annotation.
	AvailabilityReason string `yaml:"availability_reason,omitempty" mapstructure:"availability_reason"`
	// PromptCache annotates whether the backend participates in prompt caching.
	// nil means unknown and is treated as eligible.
	PromptCache *bool `yaml:"prompt_cache,omitempty" mapstructure:"prompt_cache"`
}

// ProfilesConfig holds profile topology configuration.
type ProfilesConfig struct {
	Root           string                   `yaml:"root" mapstructure:"root" env:"PROFILES_ROOT"`
	DefaultProfile string                   `yaml:"default_profile" mapstructure:"default_profile" env:"DEFAULT_PROFILE"`
	Items          map[string]ProfileConfig `yaml:"items" mapstructure:"items"`
}

// ProfileConfig defines a named profile root override.
type ProfileConfig struct {
	Root string `yaml:"root" mapstructure:"root"`
}

type SkillsRuntimeQuotaLimit struct {
	MaxRequests *int `yaml:"max_requests" mapstructure:"max_requests"`
	MaxTokens   *int `yaml:"max_tokens" mapstructure:"max_tokens"`
}

type SkillsRuntimeQuotaPolicies struct {
	Tenants  map[string]SkillsRuntimeQuotaLimit `yaml:"tenants" mapstructure:"tenants"`
	Projects map[string]SkillsRuntimeQuotaLimit `yaml:"projects" mapstructure:"projects"`
	Users    map[string]SkillsRuntimeQuotaLimit `yaml:"users" mapstructure:"users"`
}

type SkillsRuntimeScopeBinding struct {
	TenantID  string `yaml:"tenant_id" mapstructure:"tenant_id"`
	ProjectID string `yaml:"project_id" mapstructure:"project_id"`
	UserID    string `yaml:"user_id" mapstructure:"user_id"`
}

// ResolveRoot resolves a named profile to a root path when configured.
func (c *ProfilesConfig) ResolveRoot(name string) string {
	if c == nil {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	if item, ok := c.Items[name]; ok && item.Root != "" {
		return item.Root
	}
	if c.Root != "" {
		return filepath.Join(c.Root, name)
	}
	return ""
}

// SkillsRuntimeConfig holds the skills runtime integration config (aicli-relevant fields only).
type SkillsRuntimeConfig struct {
	ConfigFile             string                               `yaml:"config_file" mapstructure:"config_file" env:"SKILLS_RUNTIME_CONFIG_FILE"`
	Enabled                bool                                 `yaml:"enabled" mapstructure:"enabled" env:"SKILLS_RUNTIME_ENABLED"`
	SkillDir               string                               `yaml:"skill_dir" mapstructure:"skill_dir" env:"SKILLS_RUNTIME_SKILL_DIR"`
	SkillDirs              []string                             `yaml:"skill_dirs" mapstructure:"skill_dirs"`
	ExtraSkillDirs         []string                             `yaml:"extra_skill_dirs" mapstructure:"extra_skill_dirs"`
	AICLISkillExposureTopK int                                  `yaml:"aicli_skill_exposure_top_k" mapstructure:"aicli_skill_exposure_top_k" env:"SKILLS_RUNTIME_AICLI_SKILL_EXPOSURE_TOP_K"`
	AICLISkillExposureMode string                               `yaml:"aicli_skill_exposure_mode" mapstructure:"aicli_skill_exposure_mode" env:"SKILLS_RUNTIME_AICLI_SKILL_EXPOSURE_MODE"`
	GatewayProviderName    string                               `yaml:"gateway_provider_name" mapstructure:"gateway_provider_name" env:"SKILLS_RUNTIME_GATEWAY_PROVIDER_NAME"`
	AdminToken             string                               `yaml:"admin_token" mapstructure:"admin_token" env:"SKILLS_RUNTIME_ADMIN_TOKEN"`
	ReindexCooldown        time.Duration                        `yaml:"reindex_cooldown" mapstructure:"reindex_cooldown" env:"SKILLS_RUNTIME_REINDEX_COOLDOWN"`
	ReadOnly               bool                                 `yaml:"read_only" mapstructure:"read_only" env:"SKILLS_RUNTIME_READ_ONLY"`
	DisableImport          bool                                 `yaml:"disable_import" mapstructure:"disable_import" env:"SKILLS_RUNTIME_DISABLE_IMPORT"`
	DisablePersist         bool                                 `yaml:"disable_persist" mapstructure:"disable_persist" env:"SKILLS_RUNTIME_DISABLE_PERSIST"`
	DisableReloadOps       bool                                 `yaml:"disable_reload_ops" mapstructure:"disable_reload_ops" env:"SKILLS_RUNTIME_DISABLE_RELOAD_OPS"`
	DisableHotReloadOps    bool                                 `yaml:"disable_hot_reload_ops" mapstructure:"disable_hot_reload_ops" env:"SKILLS_RUNTIME_DISABLE_HOT_RELOAD_OPS"`
	UsageTrackingEnabled   bool                                 `yaml:"usage_tracking_enabled" mapstructure:"usage_tracking_enabled" env:"SKILLS_RUNTIME_USAGE_TRACKING_ENABLED"`
	UsageLedgerEnabled     bool                                 `yaml:"usage_ledger_enabled" mapstructure:"usage_ledger_enabled" env:"SKILLS_RUNTIME_USAGE_LEDGER_ENABLED"`
	QuotaEnabled           bool                                 `yaml:"quota_enabled" mapstructure:"quota_enabled" env:"SKILLS_RUNTIME_QUOTA_ENABLED"`
	DefaultMaxRequests     int                                  `yaml:"default_max_requests" mapstructure:"default_max_requests" env:"SKILLS_RUNTIME_DEFAULT_MAX_REQUESTS"`
	DefaultMaxTokens       int                                  `yaml:"default_max_tokens" mapstructure:"default_max_tokens" env:"SKILLS_RUNTIME_DEFAULT_MAX_TOKENS"`
	QuotaPolicies          SkillsRuntimeQuotaPolicies           `yaml:"quota_policies" mapstructure:"quota_policies"`
	ScopeResolverEnabled   bool                                 `yaml:"scope_resolver_enabled" mapstructure:"scope_resolver_enabled" env:"SKILLS_RUNTIME_SCOPE_RESOLVER_ENABLED"`
	TenantHeaders          []string                             `yaml:"tenant_headers" mapstructure:"tenant_headers"`
	ProjectHeaders         []string                             `yaml:"project_headers" mapstructure:"project_headers"`
	UserHeaders            []string                             `yaml:"user_headers" mapstructure:"user_headers"`
	RoleHeaders            []string                             `yaml:"role_headers" mapstructure:"role_headers"`
	JWTClaimsEnabled       bool                                 `yaml:"jwt_claims_enabled" mapstructure:"jwt_claims_enabled" env:"SKILLS_RUNTIME_JWT_CLAIMS_ENABLED"`
	JWTSecret              string                               `yaml:"jwt_secret" mapstructure:"jwt_secret" env:"SKILLS_RUNTIME_JWT_SECRET"`
	TenantClaims           []string                             `yaml:"tenant_claims" mapstructure:"tenant_claims"`
	ProjectClaims          []string                             `yaml:"project_claims" mapstructure:"project_claims"`
	UserClaims             []string                             `yaml:"user_claims" mapstructure:"user_claims"`
	RoleClaims             []string                             `yaml:"role_claims" mapstructure:"role_claims"`
	AdminRoles             []string                             `yaml:"admin_roles" mapstructure:"admin_roles"`
	APIKeyScopes           map[string]SkillsRuntimeScopeBinding `yaml:"api_key_scopes" mapstructure:"api_key_scopes"`
	// SK-7/SK-10（P0/P2）：文档模式与目录预算灰度开关。
	// DocumentMode 控制 Codex 兼容技能的自动文档模式识别：
	//   - "auto"：按 IsDocumentMode() 规则（Codex 格式、无 handler/workflow → 文档模式）；
	//   - "off"（默认）：保持现状，Codex 技能走 executeDefault 子调用。
	// 显式声明 execution_mode: document 的技能始终为文档模式，与本开关无关。
	DocumentMode string `yaml:"document_mode" mapstructure:"document_mode" env:"SKILLS_RUNTIME_DOCUMENT_MODE"`
	// CatalogBudgetChars 约束 catalog（技能目录）在上下文中的字符开销。
	// 默认 8000（镜像 Codex min(8000 字符, 上下文 2%)），0 表示使用默认。
	CatalogBudgetChars int `yaml:"catalog_budget_chars" mapstructure:"catalog_budget_chars" env:"SKILLS_RUNTIME_CATALOG_BUDGET_CHARS"`
	// DisciplineBlock 控制是否在 catalog 后附加"How to use skills"纪律块（SK-2）。
	// 默认开启（*bool 便于显式关闭）；nil/true 开启，显式 false 关闭。
	DisciplineBlock *bool `yaml:"discipline_block" mapstructure:"discipline_block" env:"SKILLS_RUNTIME_DISCIPLINE_BLOCK"`
}

// DisciplineBlockEnabled 报告是否应在 catalog 后附加纪律块。
// nil 或 *true → 开启；显式 false → 关闭。
func (c *SkillsRuntimeConfig) DisciplineBlockEnabled() bool {
	if c == nil || c.DisciplineBlock == nil {
		return true
	}
	return *c.DisciplineBlock
}

// DocumentModeAuto 报告是否开启 Codex 技能的自动文档模式识别。
// 仅当 DocumentMode=="auto" 时，Codex 兼容技能（无 handler/workflow）才进入文档模式。
func (c *SkillsRuntimeConfig) DocumentModeAuto() bool {
	if c == nil {
		return false
	}
	return strings.TrimSpace(c.DocumentMode) == "auto"
}

// CatalogBudget 返回 catalog 字符预算；未配置时返回默认 8000。
func (c *SkillsRuntimeConfig) CatalogBudget() int {
	if c == nil || c.CatalogBudgetChars <= 0 {
		return 8000
	}
	return c.CatalogBudgetChars
}

// ServerConfig holds basic server info (used by aicli config command).
type ServerConfig struct {
	Name        string `yaml:"name" mapstructure:"name"`
	Host        string `yaml:"host" mapstructure:"host"`
	Port        int    `yaml:"port" mapstructure:"port"`
	Development bool   `yaml:"development" mapstructure:"development"`
}

// TruncationConfig holds context truncation settings.
type TruncationConfig struct {
	Enabled    bool   `yaml:"enabled" mapstructure:"enabled"`
	MaxRetries int    `yaml:"max_retries" mapstructure:"max_retries"`
	Strategy   string `yaml:"strategy" mapstructure:"strategy"`
	Step       int    `yaml:"step" mapstructure:"step"`
}

// HealthCheckConfig holds health check settings.
type HealthCheckConfig struct {
	UnhealthyThreshold int           `yaml:"unhealthy_threshold" mapstructure:"unhealthy_threshold"`
	HealthyThreshold   int           `yaml:"healthy_threshold" mapstructure:"healthy_threshold"`
	HealthExpireTime   time.Duration `yaml:"health_expire_time" mapstructure:"health_expire_time"`
}

// ProviderFailoverConfig holds failover settings.
type ProviderFailoverConfig struct {
	Enabled bool   `yaml:"enabled" mapstructure:"enabled"`
	Mode    string `yaml:"mode" mapstructure:"mode"`
	Scope   string `yaml:"scope" mapstructure:"scope"`
}

// ProviderGroup holds a named group of providers for load balancing.
type ProviderGroup struct {
	Name        string                  `yaml:"name" mapstructure:"name"`
	Providers   []GroupProvider         `yaml:"providers" mapstructure:"providers"`
	Strategy    string                  `yaml:"strategy" mapstructure:"strategy"`
	MaxRetries  int                     `yaml:"max_retries" mapstructure:"max_retries"`
	RetryDelay  time.Duration           `yaml:"retry_delay" mapstructure:"retry_delay"`
	Truncation  *TruncationConfig       `yaml:"truncation" mapstructure:"truncation"`
	HealthCheck *HealthCheckConfig      `yaml:"health_check" mapstructure:"health_check"`
	Failover    *ProviderFailoverConfig `yaml:"failover" mapstructure:"failover"`
}

// GroupProvider is a provider reference within a group.
type GroupProvider struct {
	Name    string `yaml:"name" mapstructure:"name"`
	Weight  int    `yaml:"weight" mapstructure:"weight"`
	Role    string `yaml:"role" mapstructure:"role"`
	Enabled bool   `yaml:"enabled" mapstructure:"enabled"`
}

// GetAPIKey returns a randomly selected API key from the provider's key pool.
func (p *Provider) GetAPIKey() string {
	keys := p.GetAllAPIKeys()
	if len(keys) == 0 {
		return ""
	}
	return keys[rand.Intn(len(keys))]
}

// GetAllAPIKeys returns all configured API keys.
func (p *Provider) GetAllAPIKeys() []string {
	if p == nil {
		return nil
	}
	if strings.EqualFold(strings.TrimSpace(p.AuthMode), AuthKeyTypeOAuth) && strings.TrimSpace(p.AuthRef) != "" {
		if record, err := LoadProviderAuth(strings.TrimSpace(p.AuthRef)); err == nil && record != nil && strings.TrimSpace(record.AccessToken) != "" {
			return []string{strings.TrimSpace(record.AccessToken)}
		}
	}
	if strings.TrimSpace(p.APIKeyRef) != "" {
		if secret, err := LoadProviderAuthSecret(strings.TrimSpace(p.APIKeyRef), AuthKeyTypeAPIKey); err == nil && strings.TrimSpace(secret) != "" {
			return []string{strings.TrimSpace(secret)}
		}
	}
	if len(p.APIKeys) > 0 {
		return p.APIKeys
	}
	if p.APIKey != "" {
		if strings.Contains(p.APIKey, ",") {
			parts := strings.Split(p.APIKey, ",")
			result := make([]string, 0, len(parts))
			for _, k := range parts {
				if k = strings.TrimSpace(k); k != "" {
					result = append(result, k)
				}
			}
			return result
		}
		return []string{p.APIKey}
	}
	return nil
}

// ApplyModelMapping applies the provider's model mapping to the requested model name.
func ApplyModelMapping(provider *Provider, requestedModel string) string {
	if provider == nil || provider.ModelMappings == nil {
		return requestedModel
	}
	if mapped, ok := provider.ModelMappings[requestedModel]; ok && mapped != "" {
		return mapped
	}
	return requestedModel
}

// NormalizeRequestPath trims a trailing slash while preserving the root path.
func NormalizeRequestPath(path string) string {
	if path == "/" || path == "" {
		return path
	}
	return strings.TrimRight(path, "/")
}

// NormalizeProtocol lowercases and trims a protocol string.
func NormalizeProtocol(proto string) string {
	return strings.ToLower(strings.TrimSpace(proto))
}

// JoinBaseURLAndPath appends requestPath to baseURL while collapsing duplicated
// path segments at the boundary, for example https://host/v1 + /v1/models.
func JoinBaseURLAndPath(baseURL, requestPath string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	requestPath = strings.TrimSpace(requestPath)
	if requestPath == "" {
		return baseURL
	}
	if parsed, err := url.Parse(requestPath); err == nil && parsed.IsAbs() {
		return requestPath
	}
	if baseURL == "" {
		if strings.HasPrefix(requestPath, "/") {
			return requestPath
		}
		return "/" + requestPath
	}
	if strings.HasPrefix(requestPath, "?") || strings.HasPrefix(requestPath, "#") {
		return baseURL + requestPath
	}

	pathPart, suffix := splitURLPathSuffix(requestPath)
	if strings.TrimSpace(pathPart) == "" {
		return baseURL + suffix
	}

	parsedBase, err := url.Parse(baseURL)
	if err != nil || parsedBase.Scheme == "" || parsedBase.Host == "" {
		return baseURL + "/" + strings.TrimLeft(requestPath, "/")
	}

	baseSegments := splitURLPathSegments(parsedBase.Path)
	requestSegments := splitURLPathSegments(pathPart)
	overlap := longestPathSegmentOverlap(baseSegments, requestSegments)
	finalSegments := append(append([]string(nil), baseSegments...), requestSegments[overlap:]...)
	if len(finalSegments) == 0 {
		parsedBase.Path = ""
	} else {
		parsedBase.Path = "/" + strings.Join(finalSegments, "/")
	}
	return parsedBase.String() + suffix
}

func splitURLPathSuffix(path string) (string, string) {
	for i, r := range path {
		if r == '?' || r == '#' {
			return path[:i], path[i:]
		}
	}
	return path, ""
}

func splitURLPathSegments(path string) []string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			segments = append(segments, part)
		}
	}
	return segments
}

func longestPathSegmentOverlap(left, right []string) int {
	maxOverlap := len(left)
	if len(right) < maxOverlap {
		maxOverlap = len(right)
	}
	for overlap := maxOverlap; overlap > 0; overlap-- {
		matched := true
		for i := 0; i < overlap; i++ {
			if left[len(left)-overlap+i] != right[i] {
				matched = false
				break
			}
		}
		if matched {
			return overlap
		}
	}
	return 0
}

// BuildUpstreamURLWithPath builds an upstream URL from the provider config and request path.
func BuildUpstreamURLWithPath(provider Provider, transformedPath, queryString, model string) string {
	if provider.ForwardURL != "" {
		apiKey := provider.GetAPIKey()
		u := provider.ForwardURL
		u = strings.Replace(u, "{model}", model, -1)
		u = strings.Replace(u, "{api_key}", apiKey, -1)
		u = strings.Replace(u, "{path}", transformedPath, -1)
		// relative forward_url: prepend base_url
		if strings.HasPrefix(u, "/") {
			u = JoinBaseURLAndPath(provider.BaseURL, u)
		}
		if queryString != "" {
			if strings.Contains(u, "?") {
				u += "&" + strings.TrimPrefix(queryString, "?")
			} else {
				u += queryString
			}
		}
		return u
	}
	baseURL := strings.TrimSuffix(provider.BaseURL, "/")
	apiPath := strings.TrimSuffix(provider.APIPath, "/")
	var finalPath string
	if apiPath != "" && !strings.HasPrefix(transformedPath, apiPath+"/") && transformedPath != apiPath {
		finalPath = "/" + strings.TrimPrefix(apiPath+"/"+strings.TrimPrefix(transformedPath, "/"), "/")
	} else {
		finalPath = transformedPath
	}
	u := JoinBaseURLAndPath(baseURL, finalPath)
	if queryString != "" {
		u += queryString
	}
	return u
}

var (
	globalConfig *Config
)

// Manager holds a loaded Config and provides accessor methods.
type Manager struct {
	config *Config
}

// NewManager loads configuration from the given YAML file path and returns a Manager.
func NewManager(configPath string) (*Manager, error) {
	cfg, err := InitGlobalConfig(configPath)
	if err != nil {
		return nil, err
	}
	return &Manager{config: cfg}, nil
}

// Config returns the loaded configuration.
func (m *Manager) Config() *Config {
	return m.config
}

// InitGlobalConfig loads configuration from the given YAML file path.
// System presets (built-in defaults plus any files under the system preset
// directory) are merged under the user config: the user file always wins.
// When no preset files exist this is a plain single-file load.
func InitGlobalConfig(configPath string) (*Config, error) {
	cfg := &Config{}
	var userYAML []byte
	if configPath != "" {
		if absPath, err := filepath.Abs(configPath); err == nil && absPath != "" {
			configPath = absPath
		}
		data, err := os.ReadFile(configPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
		}
		if err == nil {
			data = []byte(expandEnvVars(string(data)))
			userYAML = data
			if err := unmarshalYAML(data, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
			}
			if err := validateLoadedConfig(cfg); err != nil {
				return nil, fmt.Errorf("invalid config file %s: %w", configPath, err)
			}
		}
	}
	if merged, err := applySystemPresetLayer(userYAML, cfg); err != nil {
		return nil, err
	} else {
		cfg = merged
	}
	cfg.ConfigFilePath = configPath
	globalConfig = cfg
	return cfg, nil
}

// applySystemPresetLayer merges matching enabled system presets below the
// user config and re-validates the final result. When no preset layer is
// deployed (no system preset directory, no ~/.aicli/presets.yaml) the user
// config is returned unchanged. userYAML is the raw (env-expanded) user
// config document, which keeps the merge sparse: keys the user did not
// write cannot shadow preset values.
func applySystemPresetLayer(userYAML []byte, userConfig *Config) (*Config, error) {
	mergedYAML, err := MergeWithPresets(userYAML)
	if err != nil {
		return nil, fmt.Errorf("failed to apply system presets: %w", err)
	}
	if mergedYAML == nil {
		return userConfig, nil
	}
	merged := &Config{}
	if err := unmarshalYAML(mergedYAML, merged); err != nil {
		return nil, fmt.Errorf("failed to decode merged config: %w", err)
	}
	if err := validateLoadedConfig(merged); err != nil {
		return nil, fmt.Errorf("invalid config after system preset merge: %w", err)
	}
	return merged, nil
}

// GetGlobalConfig returns the current global config.
func GetGlobalConfig() *Config {
	return globalConfig
}

// SetGlobalConfig replaces the process-wide global config reference.
func SetGlobalConfig(cfg *Config) {
	if cfg == nil {
		globalConfig = &Config{}
		return
	}
	globalConfig = cfg
}

// expandEnvVars replaces ${VAR} and ${VAR:-default} patterns with environment variable values.
func expandEnvVars(content string) string {
	re := regexp.MustCompile(`\$\{([^}:]+)(:-([^}]*))?\}`)
	return re.ReplaceAllStringFunc(content, func(match string) string {
		parts := re.FindStringSubmatch(match)
		if len(parts) < 2 {
			return match
		}
		if v := os.Getenv(parts[1]); v != "" {
			return v
		}
		if len(parts) >= 4 && parts[3] != "" {
			return parts[3]
		}
		return match
	})
}

func validateLoadedConfig(cfg *Config) error {
	return ValidateConfig(cfg)
}

// ValidateConfig validates user-facing configuration that must be checked by
// both process startup and the runtime-server configuration document API.
func ValidateConfig(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	for name, provider := range cfg.Providers.Items {
		if err := validateProviderCompatibilityConfig(name, provider); err != nil {
			return err
		}
	}
	if cfg.AICLI == nil {
		return nil
	}
	if cfg.AICLI.Subagents != nil && cfg.AICLI.Subagents.Routing != nil {
		if err := validateAgentRoutingConfig("aicli.subagents.routing", cfg.AICLI.Subagents.Routing); err != nil {
			return err
		}
	}
	if cfg.AICLI.Teams != nil && cfg.AICLI.Teams.Routing != nil {
		if err := validateAgentRoutingConfig("aicli.teams.routing", cfg.AICLI.Teams.Routing); err != nil {
			return err
		}
	}
	if cfg.AICLI.MainAgent != nil && cfg.AICLI.MainAgent.Routing != nil {
		// warnings（expensive_levels 越界项）已由校验函数就地剔除；这里没有
		// warning 通道，需要展示告警的宿主应直接调用
		// ValidateMainAgentRoutingConfig 取返回值。
		if _, err := ValidateMainAgentRoutingConfig(cfg.AICLI.MainAgent.Routing); err != nil {
			return err
		}
	}
	return nil
}

func validateProviderCompatibilityConfig(providerName string, provider Provider) error {
	if err := ValidateCompatibilityProfile(provider.GetProtocol(), provider.Compatibility.Profile); err != nil {
		return fmt.Errorf(
			"invalid providers.items.%s.compatibility.profile: %w",
			strings.TrimSpace(providerName),
			err,
		)
	}
	return nil
}

func validateAgentRoutingConfig(configPath string, cfg *AICLISubagentRoutingConfig) error {
	_, err := validateSubagentRoutingConfig(configPath, cfg)
	return err
}

// ValidateSubagentRoutingConfig 返回子代理路由配置的告警与错误（G5/G6）。
// Config.Validate() 只取 error；需要展示告警的宿主（例如
// `aicli doctor subagent-route`）应直接调用本函数，以免告警只停留在代码里。
// configPath 为空时按 "aicli.subagents.routing" 输出。
func ValidateSubagentRoutingConfig(configPath string, cfg *AICLISubagentRoutingConfig) ([]string, error) {
	if strings.TrimSpace(configPath) == "" {
		configPath = "aicli.subagents.routing"
	}
	return validateSubagentRoutingConfig(configPath, cfg)
}

func validateSubagentRoutingConfig(configPath string, cfg *AICLISubagentRoutingConfig) ([]string, error) {
	if cfg == nil || cfg.Enabled == nil || !*cfg.Enabled {
		return nil, nil
	}
	warnings := []string{}
	if strings.TrimSpace(cfg.CompatibilityMode) != "" {
		if _, ok := normalizeSubagentCompatibilityMode(cfg.CompatibilityMode); !ok {
			return nil, fmt.Errorf("invalid %s.compatibility_mode %q", configPath, cfg.CompatibilityMode)
		}
	}
	if strings.TrimSpace(cfg.DefaultDifficulty) != "" {
		if _, ok := normalizeSubagentDifficulty(cfg.DefaultDifficulty); !ok {
			return nil, fmt.Errorf("invalid %s.default_difficulty %q", configPath, cfg.DefaultDifficulty)
		}
	}
	if strings.TrimSpace(cfg.UnsupportedReasoningPolicy) != "" {
		if _, ok := normalizeSubagentUnsupportedReasoningPolicy(cfg.UnsupportedReasoningPolicy); !ok {
			return nil, fmt.Errorf("invalid %s.unsupported_reasoning_policy %q", configPath, cfg.UnsupportedReasoningPolicy)
		}
	}
	if strings.TrimSpace(cfg.OnReasoningUnsupported) != "" {
		if _, ok := normalizeSubagentUnsupportedReasoningPolicy(cfg.OnReasoningUnsupported); !ok {
			return nil, fmt.Errorf("invalid %s.on_reasoning_unsupported %q", configPath, cfg.OnReasoningUnsupported)
		}
	}
	if strings.TrimSpace(cfg.PromoteExplicitDifficulty) != "" {
		if _, ok := normalizeSubagentPromoteExplicitMode(cfg.PromoteExplicitDifficulty); !ok {
			return nil, fmt.Errorf("invalid %s.promote_explicit_difficulty %q (want off|warn|enforce)", configPath, cfg.PromoteExplicitDifficulty)
		}
	}
	if cfg.Heuristics != nil {
		if err := validateSubagentKeywordList(configPath+".heuristics.promote_keywords", cfg.Heuristics.PromoteKeywords); err != nil {
			return nil, err
		}
		if err := validateSubagentKeywordList(configPath+".heuristics.promote_keywords_combo", cfg.Heuristics.PromoteKeywordsCombo); err != nil {
			return nil, err
		}
	}
	if err := validateSubagentRouteAliases(configPath+".levels", cfg.Levels, &warnings); err != nil {
		return nil, err
	}
	for _, key := range sortedSubagentRouteKeys(cfg.Levels) {
		profile := cfg.Levels[key]
		difficulty, ok := normalizeSubagentDifficulty(key)
		if !ok {
			return nil, fmt.Errorf("invalid %s.levels key %q", configPath, key)
		}
		if err := validateSubagentRouteProfile(configPath+".levels."+difficulty, profile, cfg); err != nil {
			return nil, err
		}
	}
	// v4（plan K-3/K-4）：roles.<role> 是 task_types.<task_type> 的兼容别名，
	// 读入时映射（显式 task_types 优先，不覆盖已有项）并给 deprecation warning；
	// 未知 task_type 键 → task_type_unknown warning + 忽略（不回退猜词）。
	if len(cfg.Roles) > 0 && cfg.TaskTypes == nil {
		cfg.TaskTypes = map[string]map[string]AICLISubagentRouteProfile{}
	}
	for _, role := range sortedSubagentRoleKeys(cfg.Roles) {
		mapped := subagentRoleTaskTypeAlias(role)
		if mapped == "" {
			// 无别名的自定义 role 保留在 Roles 里兜底（routeProfileForTask 仍查），
			// 不强行搬进 task_types——封闭枚举放不进未知类别。
			continue
		}
		warnings = append(warnings, fmt.Sprintf(
			"%s.roles.%s deprecated: mapped to %s.task_types.%s (alias kept for one release)",
			configPath, strings.TrimSpace(role), configPath, mapped,
		))
		if _, exists := cfg.TaskTypes[mapped]; !exists {
			cfg.TaskTypes[mapped] = cfg.Roles[role]
		}
	}
	for _, taskType := range sortedSubagentTaskTypeKeys(cfg.TaskTypes) {
		levels := cfg.TaskTypes[taskType]
		normalized, ok := normalizeSubagentTaskType(taskType)
		if !ok {
			warnings = append(warnings, fmt.Sprintf(
				"%s.task_types key %q is unknown (task_type_unknown:%s); entry ignored for routing",
				configPath, taskType, normalized,
			))
		}
		if err := validateSubagentRouteAliases(configPath+".task_types."+normalized, levels, &warnings); err != nil {
			return nil, err
		}
		for _, key := range sortedSubagentRouteKeys(levels) {
			profile := levels[key]
			difficulty, ok := normalizeSubagentDifficulty(key)
			if !ok {
				return nil, fmt.Errorf("invalid %s.task_types.%s key %q", configPath, taskType, key)
			}
			if err := validateSubagentRouteProfile(configPath+".task_types."+normalized+"."+difficulty, profile, cfg); err != nil {
				return nil, err
			}
		}
	}
	for _, role := range sortedSubagentRoleKeys(cfg.Roles) {
		levels := cfg.Roles[role]
		trimmedRole := strings.TrimSpace(role)
		if trimmedRole == "" {
			return nil, fmt.Errorf("%s.roles key cannot be empty", configPath)
		}
		if err := validateSubagentRouteAliases(configPath+".roles."+trimmedRole, levels, &warnings); err != nil {
			return nil, err
		}
		for _, key := range sortedSubagentRouteKeys(levels) {
			profile := levels[key]
			difficulty, ok := normalizeSubagentDifficulty(key)
			if !ok {
				return nil, fmt.Errorf("invalid %s.roles.%s key %q", configPath, trimmedRole, key)
			}
			if err := validateSubagentRouteProfile(configPath+".roles."+trimmedRole+"."+difficulty, profile, cfg); err != nil {
				return nil, err
			}
		}
	}
	if cfg.MaxExpertConcurrency < -1 {
		return nil, fmt.Errorf("%s.max_expert_concurrency must be -1 (explicit unlimited) or a positive limit, got %d", configPath, cfg.MaxExpertConcurrency)
	}
	if cfg.MaxExpertConcurrency == 0 {
		warnings = append(warnings, configPath+".max_expert_concurrency=0 means unlimited (no expert gate); set -1 to say so explicitly, or a positive value to bound expert concurrency")
	}
	return warnings, nil
}

// normalizeSubagentPromoteExplicitMode 归一 promote_explicit_difficulty 三态。
// 空值返回 enforce：本仓库选择"安全网默认生效"，off/warn 作为显式回退开关。
func normalizeSubagentPromoteExplicitMode(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "enforce", true
	}
	switch key {
	case "off", "false", "disabled", "none":
		return "off", true
	case "warn", "warning", "dry_run":
		return "warn", true
	case "enforce", "on", "true", "enabled":
		return "enforce", true
	default:
		return "", false
	}
}

// validateSubagentKeywordList 拒绝空条目与归一后重复的词，避免同一命中产生
// 多条重复告警、以及无法解释的"空关键词"。
func validateSubagentKeywordList(label string, keywords []string) error {
	seen := map[string]string{}
	for _, keyword := range keywords {
		trimmed := strings.TrimSpace(keyword)
		if trimmed == "" {
			return fmt.Errorf("%s contains an empty keyword", label)
		}
		folded := strings.ToLower(trimmed)
		if prev, dup := seen[folded]; dup {
			return fmt.Errorf("%s contains duplicate keyword %q (already declared as %q)", label, trimmed, prev)
		}
		seen[folded] = trimmed
	}
	return nil
}

// validateSubagentRouteAliases 检测归一后撞车的难度键（G5）：profile 完全一致时
// 允许并记 warning（无行为风险），不一致时返回 error 并指明两个原始键与归一结果。
func validateSubagentRouteAliases(label string, levels map[string]AICLISubagentRouteProfile, warnings *[]string) error {
	seen := map[string]string{}
	for _, key := range sortedSubagentRouteKeys(levels) {
		difficulty, ok := normalizeSubagentDifficulty(key)
		if !ok {
			return fmt.Errorf("invalid %s key %q", label, key)
		}
		prev, dup := seen[difficulty]
		if !dup {
			seen[difficulty] = key
			continue
		}
		if reflect.DeepEqual(levels[prev], levels[key]) {
			if warnings != nil {
				*warnings = append(*warnings, fmt.Sprintf(
					"%s alias keys %q and %q both normalize to %q with identical profiles",
					label, prev, key, difficulty,
				))
			}
			continue
		}
		return fmt.Errorf(
			"%s keys %q and %q both normalize to %q with different profiles; keep one spelling (accepted: easy|normal|hard|expert, plus synonyms such as medium|complex) or make both profiles identical",
			label, prev, key, difficulty,
		)
	}
	return nil
}

func sortedSubagentRouteKeys(levels map[string]AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(levels))
	for key := range levels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSubagentRoleKeys(roles map[string]map[string]AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(roles))
	for key := range roles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedSubagentTaskTypeKeys(taskTypes map[string]map[string]AICLISubagentRouteProfile) []string {
	keys := make([]string, 0, len(taskTypes))
	for key := range taskTypes {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// subagentTaskTypes 是 task_type 封闭枚举在 agentconfig 侧的镜像（modelrouting
// 反向依赖本包，无法互 import；按仓库既有惯例——difficulty 归一同样两包各持一份）。
// 与 modelrouting.taskTypeFloors 键集合必须一致，由 modelrouting 的枚举测试守住。
var subagentTaskTypes = map[string]struct{}{
	"explore": {}, "understand": {}, "modify": {}, "implement": {},
	"refactor": {}, "test": {}, "verify": {}, "migrate": {},
	"security": {}, "config": {}, "integration": {}, "generate": {},
}

// normalizeSubagentTaskType 归一并校验 task_type 是否为已知类别。
func normalizeSubagentTaskType(raw string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return "", false
	}
	_, ok := subagentTaskTypes[normalized]
	return normalized, ok
}

// subagentRoleTaskTypeAlias 返回配置键 roles.<role> 对应的 task_type 别名；
// 无别名（自定义 role）返回 ""，保留在 Roles 兜底。只覆盖有路由层语义的三个
// 历史 role（plan §1）；writer 的 readonly 维度在配置层不存在，统一映射 implement
// ——只读 writer 的任务级回落仍走 cfg.Roles（任务未声明 task_type 时查得到）。
func subagentRoleTaskTypeAlias(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "verifier":
		return "verify"
	case "writer":
		return "implement"
	case "researcher":
		return "explore"
	default:
		return ""
	}
}

func validateSubagentRouteProfile(label string, profile AICLISubagentRouteProfile, cfg *AICLISubagentRoutingConfig) error {
	if profile.MaxTokens < 0 {
		return fmt.Errorf("%s.max_tokens cannot be negative", label)
	}
	if profile.Timeout < 0 {
		return fmt.Errorf("%s.timeout cannot be negative", label)
	}
	if !subagentRoutingInheritParentWhenMissing(cfg) && (strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "") {
		return fmt.Errorf("%s must set provider and model when inherit_parent_when_missing=false", label)
	}
	return nil
}

func subagentRoutingInheritParentWhenMissing(cfg *AICLISubagentRoutingConfig) bool {
	if cfg == nil || cfg.InheritParentWhenMissing == nil {
		return true
	}
	return *cfg.InheritParentWhenMissing
}

func normalizeSubagentCompatibilityMode(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "permissive", true
	}
	switch key {
	case "permissive", "strict":
		return key, true
	default:
		return "", false
	}
}

func normalizeSubagentUnsupportedReasoningPolicy(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "ignore", true
	}
	switch key {
	case "ignore", "warn", "clear":
		return "ignore", true
	case "downgrade":
		return "downgrade", true
	case "fail", "reject":
		return "fail", true
	default:
		return "", false
	}
}

func normalizeSubagentDifficulty(raw string) (string, bool) {
	key := strings.ToLower(strings.TrimSpace(raw))
	if key == "" {
		return "", false
	}
	key = strings.NewReplacer("-", "_", " ", "_").Replace(key)
	switch key {
	case "easy", "simple", "low", "trivial":
		return "easy", true
	case "normal", "medium", "standard", "default":
		return "normal", true
	case "hard", "complex", "high", "difficult":
		return "hard", true
	case "expert", "critical", "very_hard", "architectural":
		return "expert", true
	default:
		return "", false
	}
}

func unmarshalYAML(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

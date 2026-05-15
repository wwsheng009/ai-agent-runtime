// Package agentconfig provides configuration types for aicli and agent runtime.
// These types are extracted from ai-gateway/internal/config, keeping only agent/aicli relevant fields.
package agentconfig

import (
	"fmt"
	"math/rand"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
	"gopkg.in/yaml.v3"
)

// DatabaseConfig holds basic database connection info.
type DatabaseConfig struct {
	Driver string `yaml:"driver" mapstructure:"driver"`
	DSN    string `yaml:"dsn" mapstructure:"dsn"`
}

// Config holds the aicli-relevant subset of the gateway config.
type Config struct {
	Server         ServerConfig         `yaml:"server" mapstructure:"server"`
	Database       DatabaseConfig       `yaml:"database" mapstructure:"database"`
	Providers      ProvidersConfig      `yaml:"providers" mapstructure:"providers"`
	ProviderGroups []ProviderGroup      `yaml:"provider_groups" mapstructure:"provider_groups"`
	Retry          *RetryConfig         `yaml:"retry" mapstructure:"retry"`
	AICLI          *AICLIConfig         `yaml:"aicli" mapstructure:"aicli"`
	Profiles       *ProfilesConfig      `yaml:"profiles" mapstructure:"profiles"`
	SkillsRuntime  *SkillsRuntimeConfig `yaml:"skills_runtime" mapstructure:"skills_runtime"`
	Log            logger.LogConfig     `yaml:"log" mapstructure:"log"`
	ConfigFilePath string               `yaml:"-" mapstructure:"-"`
}

// ProvidersConfig holds the provider collection configuration.
type ProvidersConfig struct {
	DefaultProvider string              `yaml:"default_provider" mapstructure:"default_provider" env:"PROVIDERS_DEFAULT"`
	Timeout         time.Duration       `yaml:"timeout" mapstructure:"timeout" env:"PROVIDERS_TIMEOUT"`
	MaxRetries      int                 `yaml:"max_retries" mapstructure:"max_retries" env:"PROVIDERS_MAX_RETRIES"`
	Backoff         BackoffConfig       `yaml:"backoff" mapstructure:"backoff"`
	HTTPTimeout     HTTPTimeout         `yaml:"http_timeout" mapstructure:"http_timeout"`
	Proxy           ProxyConfig         `yaml:"proxy" mapstructure:"proxy"`
	Items           map[string]Provider `yaml:"items" mapstructure:"items"`
}

// BackoffConfig holds retry backoff configuration.
type BackoffConfig struct {
	InitialInterval time.Duration `yaml:"initial_interval" mapstructure:"initial_interval"`
	MaxInterval     time.Duration `yaml:"max_interval" mapstructure:"max_interval"`
	MaxElapsedTime  time.Duration `yaml:"max_elapsed_time" mapstructure:"max_elapsed_time"`
	Multiplier      float64       `yaml:"multiplier" mapstructure:"multiplier"`
	Randomization   float64       `yaml:"randomization" mapstructure:"randomization"`
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
	ReasoningModel         bool           `yaml:"reasoning_model" mapstructure:"reasoning_model" json:"reasoning_model"`
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
	Enabled                 bool                           `yaml:"enabled" mapstructure:"enabled" json:"enabled"`
	Type                    string                         `yaml:"type" mapstructure:"type" json:"type"`
	Protocol                string                         `yaml:"protocol" mapstructure:"protocol" json:"protocol"`
	BaseURL                 string                         `yaml:"base_url" mapstructure:"base_url" json:"base_url"`
	APIPath                 string                         `yaml:"api_path" mapstructure:"api_path" json:"api_path"`
	ForwardURL              string                         `yaml:"forward_url" mapstructure:"forward_url" json:"forward_url"`
	APIKey                  string                         `yaml:"api_key" mapstructure:"api_key" json:"api_key"`
	APIKeys                 []string                       `yaml:"api_keys" mapstructure:"api_keys" json:"api_keys"`
	APIKeyRef               string                         `yaml:"api_key_ref" mapstructure:"api_key_ref" json:"api_key_ref"`
	AuthMode                string                         `yaml:"auth_mode,omitempty" mapstructure:"auth_mode" json:"auth_mode,omitempty"`
	AuthRef                 string                         `yaml:"auth_ref,omitempty" mapstructure:"auth_ref" json:"auth_ref,omitempty"`
	ModelsPath              string                         `yaml:"models_path,omitempty" mapstructure:"models_path" json:"models_path,omitempty"`
	ModelsVerifiedAt        string                         `yaml:"models_verified_at,omitempty" mapstructure:"models_verified_at" json:"models_verified_at,omitempty"`
	DefaultModel            string                         `yaml:"default_model" mapstructure:"default_model" json:"default_model"`
	SupportedModels         []string                       `yaml:"supported_models" mapstructure:"supported_models" json:"supported_models"`
	Headers                 map[string]string              `yaml:"headers" mapstructure:"headers" json:"headers"`
	HeaderMappings          map[string]string              `yaml:"header_mappings" mapstructure:"header_mappings" json:"header_mappings"`
	HeaderMappingRules      []HeaderMappingRule            `yaml:"header_mapping_rules" mapstructure:"header_mapping_rules" json:"header_mapping_rules"`
	SupportTypes            []string                       `yaml:"support_types" mapstructure:"support_types" json:"support_types"`
	ModelMappings           map[string]string              `yaml:"model_mappings" mapstructure:"model_mappings" json:"model_mappings"`
	ModelCapabilities       map[string]ModelCapabilitySpec `yaml:"model_capabilities" mapstructure:"model_capabilities" json:"model_capabilities"`
	MaxTokensLimit          int                            `yaml:"max_tokens_limit" mapstructure:"max_tokens_limit" json:"max_tokens_limit"`
	MaxToken                int                            `yaml:"max_token" mapstructure:"max_token" json:"max_token"`
	SupportsMaxOutputTokens *bool                          `yaml:"supports_max_output_tokens" mapstructure:"supports_max_output_tokens" json:"supports_max_output_tokens"`
	Timeout                 time.Duration                  `yaml:"timeout" mapstructure:"timeout" json:"timeout"`
	Proxy                   *ProxyConfig                   `yaml:"proxy" mapstructure:"proxy" json:"proxy"`
	// RequestsPerMinute caps the number of provider API calls per rolling minute.
	// Zero means no client-side rate limiting.
	RequestsPerMinute int `yaml:"requests_per_minute" mapstructure:"requests_per_minute" json:"requests_per_minute"`
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
	Theme      *AICLIThemeConfig      `yaml:"theme" mapstructure:"theme"`
	Chat       *AICLIChatConfig       `yaml:"chat" mapstructure:"chat"`
	Runtime    *AICLIRuntimeConfig    `yaml:"runtime" mapstructure:"runtime"`
	ModelCards *AICLIModelCardsConfig `yaml:"model_cards" mapstructure:"model_cards"`
}

// AICLIMCPConfig holds aicli MCP configuration.
type AICLIMCPConfig struct {
	ConfigFile  string `yaml:"config_file" mapstructure:"config_file" env:"MCP_CONFIG_FILE"`
	AutoConnect bool   `yaml:"auto_connect" mapstructure:"auto_connect" env:"MCP_AUTO_CONNECT"`
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

// AICLIThemeConfig holds aicli terminal theme preferences.
type AICLIThemeConfig struct {
	Name string `yaml:"name" mapstructure:"name" env:"AICLI_THEME"`
}

// AICLIChatConfig holds aicli chat preference defaults.
type AICLIChatConfig struct {
	DefaultProvider string `yaml:"default_provider,omitempty" mapstructure:"default_provider"`
	DefaultModel    string `yaml:"default_model,omitempty" mapstructure:"default_model"`
	ReasoningEffort string `yaml:"reasoning_effort,omitempty" mapstructure:"reasoning_effort"`
	// Stream 记录用户偏好的输出模式（流式/普通）。使用指针以便区分“未配置”与“显式 false”。
	Stream *bool `yaml:"stream,omitempty" mapstructure:"stream"`
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
func InitGlobalConfig(configPath string) (*Config, error) {
	cfg := &Config{}
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
			if err := unmarshalYAML(data, cfg); err != nil {
				return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
			}
		}
	}
	cfg.ConfigFilePath = configPath
	globalConfig = cfg
	return cfg, nil
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

func unmarshalYAML(data []byte, v interface{}) error {
	return yaml.Unmarshal(data, v)
}

package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	"github.com/wwsheng009/ai-agent-runtime/internal/types"
)

// RuntimeConfig Runtime 配置
type RuntimeConfig struct {
	DefaultProvider string            `yaml:"defaultProvider,omitempty" json:"defaultProvider,omitempty"`
	DefaultModel    string            `yaml:"defaultModel" json:"defaultModel"`
	DefaultTimeout  time.Duration     `yaml:"defaultTimeout" json:"defaultTimeout"`
	MaxRetries      int               `yaml:"maxRetries" json:"maxRetries"`
	RetryTuning     RetryTuning       `yaml:"retryTuning,omitempty" json:"retryTuning,omitempty"`
	RetryRules      []RetryRule       `yaml:"retryRules,omitempty" json:"retryRules,omitempty"`
	Providers       map[string]string `yaml:"providers" json:"providers"` // provider name -> type
	HealthCheck     HealthCheckConfig `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`

	// StreamReadTimeout 流式读取的空闲超时；<=0 表示不启用（默认）。
	// 只对"该有数据却没有数据"的空闲窗口生效，不影响持续产出数据的长任务。
	StreamReadTimeout time.Duration `yaml:"streamReadTimeout,omitempty" json:"streamReadTimeout,omitempty"`
}

// LLMRuntime LLM 运行时
type LLMRuntime struct {
	providers       map[string]Provider
	aliases         map[string]string
	providerAliases map[string]map[string]struct{}
	router          *ModelRouter
	config          *RuntimeConfig
	tokenizer       *Tokenizer
	health          *ProviderHealthTracker
	mu              sync.RWMutex
}

// LLMProvider ?????????????? Provider
type LLMProvider = Provider

// ModelCapabilities 模型能力
type ModelCapabilities struct {
	MaxContextTokens  int  `json:"maxContextTokens" yaml:"maxContextTokens"`
	MaxOutputTokens   int  `json:"maxOutputTokens" yaml:"maxOutputTokens"`
	SupportsVision    bool `json:"supportsVision" yaml:"supportsVision"`
	SupportsTools     bool `json:"supportsTools" yaml:"supportsTools"`
	SupportsStreaming bool `json:"supportsStreaming" yaml:"supportsStreaming"`
	SupportsJSONMode  bool `json:"supportsJSONMode" yaml:"supportsJSONMode"`
}

// LLMRequest LLM 请求
type LLMRequest struct {
	Provider        string                 `json:"provider,omitempty" yaml:"provider,omitempty"`
	Model           string                 `json:"model" yaml:"model"`
	Messages        []types.Message        `json:"messages" yaml:"messages"`
	Tools           []types.ToolDefinition `json:"tools,omitempty" yaml:"tools,omitempty"`
	MaxTokens       int                    `json:"maxTokens,omitempty" yaml:"maxTokens,omitempty"`
	Temperature     float64                `json:"temperature,omitempty" yaml:"temperature,omitempty"`
	ReasoningEffort string                 `json:"reasoning_effort,omitempty" yaml:"reasoning_effort,omitempty"`
	ReasoningModel  bool                   `json:"reasoning_model,omitempty" yaml:"reasoning_model,omitempty"`
	Thinking        *ThinkingConfig        `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Stream          bool                   `json:"stream,omitempty" yaml:"stream,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// LLMResponse LLM 响应
type LLMResponse struct {
	Content        string                 `json:"content" yaml:"content"`
	ToolCalls      []types.ToolCall       `json:"toolCalls,omitempty" yaml:"toolCalls,omitempty"`
	Usage          *types.TokenUsage      `json:"usage,omitempty" yaml:"usage,omitempty"`
	Model          string                 `json:"model" yaml:"model"`
	FinishReason   string                 `json:"finish_reason,omitempty" yaml:"finish_reason,omitempty"`
	Reasoning      string                 `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	ReasoningBlock *types.ReasoningBlock  `json:"reasoning_block,omitempty" yaml:"reasoning_block,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// StreamChunk 流式响应块
type StreamChunk struct {
	Type     StreamEventType        `json:"type" yaml:"type"`
	Content  string                 `json:"content,omitempty" yaml:"content,omitempty"`
	ToolCall *types.ToolCall        `json:"toolCall,omitempty" yaml:"toolCall,omitempty"`
	Delta    *types.ToolCall        `json:"delta,omitempty" yaml:"delta,omitempty"`
	Done     bool                   `json:"done,omitempty" yaml:"done,omitempty"`
	Error    string                 `json:"error,omitempty" yaml:"error,omitempty"`
	Metadata map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
	StreamID string                 `json:"stream_id,omitempty" yaml:"stream_id,omitempty"`
	Sequence uint64                 `json:"sequence,omitempty" yaml:"sequence,omitempty"`
	TurnID   string                 `json:"turn_id,omitempty" yaml:"turn_id,omitempty"`
	Step     int                    `json:"step,omitempty" yaml:"step,omitempty"`
}

// StreamEventType 流事件类型
type StreamEventType string

const (
	EventTypeText      StreamEventType = "text"
	EventTypeToolCall  StreamEventType = "tool_call"
	EventTypeToolStart StreamEventType = "tool_start"
	EventTypeToolEnd   StreamEventType = "tool_end"
	EventTypeImage     StreamEventType = "image"
	EventTypeDone      StreamEventType = "done"
	EventTypeError     StreamEventType = "error"
	EventTypeReasoning StreamEventType = "reasoning"
)

// NewLLMRuntime 创建 LLM 运行时
func NewLLMRuntime(config *RuntimeConfig) *LLMRuntime {
	if config == nil {
		config = &RuntimeConfig{
			DefaultProvider: "",
			DefaultModel:    "gpt-4-turbo",
			DefaultTimeout:  60 * time.Second,
			MaxRetries:      10,
			RetryTuning:     RetryTuning{},
			RetryRules:      nil,
			Providers:       make(map[string]string),
			HealthCheck:     DefaultHealthCheckConfig(),
		}
	}

	return &LLMRuntime{
		providers:       make(map[string]Provider),
		aliases:         make(map[string]string),
		providerAliases: make(map[string]map[string]struct{}),
		router:          NewModelRouter(),
		config:          config,
		tokenizer:       NewTokenizer("simple"),
		health:          NewProviderHealthTracker(config.HealthCheck),
	}
}

// RegisterProvider 注册 LLM 提供者
func (r *LLMRuntime) RegisterProvider(name string, provider Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if provider == nil {
		return errors.New(errors.ErrValidationFailed, "provider cannot be nil")
	}

	if _, exists := r.providers[name]; exists {
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("provider already registered: %s", name))
	}

	r.providers[name] = provider
	r.aliases[name] = name
	r.providerAliases[name] = map[string]struct{}{name: {}}
	if r.health != nil {
		r.health.AddProvider(name)
	}

	// 不再自动注册「无条件命中」的路由规则：DefaultCondition 恒为 true，而
	// RoutingRule.Model 存的是 provider 名，于是 router.Route() 退化成
	// 「返回第一个注册的 provider」——注册顺序还取决于 map 迭代（随机）。
	// 路由规则只由 AddRoutingRule 显式注册，请求归属统一交给
	// resolveProviderForRequest 解析。
	return nil
}

// ReplaceProviderRegistration replaces or inserts a provider registration and resets its aliases atomically.
func (r *LLMRuntime) ReplaceProviderRegistration(name string, provider Provider, aliases ...string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New(errors.ErrValidationFailed, "provider name cannot be empty")
	}
	if provider == nil {
		return errors.New(errors.ErrValidationFailed, "provider cannot be nil")
	}

	r.providers[name] = provider
	r.aliases[name] = name
	r.providerAliases[name] = map[string]struct{}{name: {}}
	if r.health != nil {
		r.health.AddProvider(name)
	}
	r.router.RemoveRule(name)

	for alias, providerName := range r.aliases {
		if providerName == name && alias != name {
			delete(r.aliases, alias)
		}
	}
	for _, alias := range aliases {
		alias = strings.TrimSpace(alias)
		if alias == "" || alias == name {
			continue
		}
		r.aliases[alias] = name
		r.providerAliases[name][alias] = struct{}{}
	}

	return nil
}

// RegisterProviderAlias 注册模型/别名到 Provider 的映射
func (r *LLMRuntime) RegisterProviderAlias(alias string, providerName string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if alias == "" {
		return errors.New(errors.ErrValidationFailed, "alias cannot be empty")
	}
	if providerName == "" {
		return errors.New(errors.ErrValidationFailed, "provider name cannot be empty")
	}
	if _, exists := r.providers[providerName]; !exists {
		return errors.New(errors.ErrValidationFailed, fmt.Sprintf("provider not found: %s", providerName))
	}

	r.aliases[alias] = providerName
	if _, exists := r.providerAliases[providerName]; !exists {
		r.providerAliases[providerName] = map[string]struct{}{providerName: {}}
	}
	r.providerAliases[providerName][alias] = struct{}{}
	return nil
}

// RegisterProviderAliases 批量注册别名
func (r *LLMRuntime) RegisterProviderAliases(providerName string, aliases ...string) error {
	for _, alias := range aliases {
		if alias == "" || alias == providerName {
			continue
		}
		if err := r.RegisterProviderAlias(alias, providerName); err != nil {
			return err
		}
	}
	return nil
}

// RegisterGatewayClient 注册基于 ResourceManager 的 GatewayClient
func (r *LLMRuntime) RegisterGatewayClient(name string, resourceManager ResourceManager, defaultModel string) error {
	if resourceManager == nil {
		return errors.New(errors.ErrValidationFailed, "resource manager cannot be nil")
	}

	gatewayClient := NewGatewayClient(resourceManager, defaultModel)
	if r.config != nil {
		if r.config.DefaultTimeout > 0 {
			gatewayClient.SetTimeout(r.config.DefaultTimeout)
		}
		gatewayClient.SetMaxRetries(r.config.MaxRetries)
		gatewayClient.SetRetryTuning(r.config.RetryTuning)
		gatewayClient.SetRetryRules(r.config.RetryRules)
		gatewayClient.SetStreamReadTimeout(r.config.StreamReadTimeout)
	}

	return r.RegisterProvider(name, gatewayClient)
}

// UnregisterProvider 注销 LLM 提供者
func (r *LLMRuntime) UnregisterProvider(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.providers, name)
	delete(r.providerAliases, name)
	if r.health != nil {
		r.health.RemoveProvider(name)
	}
	for alias, providerName := range r.aliases {
		if providerName == name || alias == name {
			delete(r.aliases, alias)
		}
	}
	r.router.RemoveRule(name)
}

// GetProvider 获取指定名称的提供者
func (r *LLMRuntime) GetProvider(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	resolvedName := name
	if alias, ok := r.aliases[name]; ok {
		resolvedName = alias
	}

	provider, exists := r.providers[resolvedName]
	if !exists {
		return nil, errors.New(errors.ErrValidationFailed, fmt.Sprintf("provider not found: %s", name))
	}

	return provider, nil
}

func (r *LLMRuntime) resolveRegisteredProviderName(name string) string {
	if r == nil {
		return ""
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if alias, ok := r.aliases[name]; ok {
		return alias
	}
	if _, ok := r.providers[name]; ok {
		return name
	}
	return ""
}

// ResolveProviderName resolves an alias or provider identifier to the registered provider name.
func (r *LLMRuntime) ResolveProviderName(name string) string {
	return r.resolveRegisteredProviderName(name)
}

// configuredDefaults returns the runtime-level default provider/model.
func (r *LLMRuntime) configuredDefaults() (string, string) {
	if r == nil || r.config == nil {
		return "", ""
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.config.DefaultProvider, r.config.DefaultModel
}

// resolveProviderForRequest 解析请求应交给哪个已注册 provider，无法确定时快速失败。
//
// 解析顺序（越靠前越明确）：
//  1. 请求显式指定的 provider；
//  2. 请求的 model 命中已注册 provider 名称/别名——别名集由 provider 的
//     default_model / supported_models 注册而来，等价于「该 provider 声明支持这个模型」；
//  3. 配置的默认 provider；
//  4. 配置的默认 model 命中已注册 provider 名称/别名；
//  5. 显式注册的路由规则（AddRoutingRule）；
//  6. 只注册了一个 provider 时用它（唯一，不存在歧义）;
//  7. 以上都不成立 → 立即返回可操作的错误，不再猜 provider。
//
// 旧实现在第 6 步会把 model 名直接当 provider 名，或者退回到 RegisterProvider 自动
// 注册的「无条件命中」路由规则（DefaultCondition 恒真，等于返回第一个注册的
// provider）。两者都会把请求交给一个没有声明该模型的 provider，于是为一个无人能
// 服务的默认模型白等整轮重试（实测真后端 ~24s）才报错。
func (r *LLMRuntime) resolveProviderForRequest(req *LLMRequest) (string, error) {
	if req == nil {
		return "", errors.New(errors.ErrValidationFailed, "request cannot be nil")
	}

	defaultProvider, defaultModel := r.configuredDefaults()
	defaultProvider = strings.TrimSpace(defaultProvider)
	defaultModel = strings.TrimSpace(defaultModel)
	if strings.TrimSpace(req.Model) == "" && defaultModel != "" {
		req.Model = defaultModel
	}

	if name := strings.TrimSpace(req.Provider); name != "" {
		return name, nil
	}

	model := strings.TrimSpace(req.Model)
	if name := r.pickProviderForModel(model, defaultProvider); name != "" {
		return name, nil
	}
	if defaultProvider != "" {
		return defaultProvider, nil
	}
	if name := r.pickProviderForModel(defaultModel, defaultProvider); name != "" {
		return name, nil
	}
	if name := strings.TrimSpace(r.router.Route(req)); name != "" {
		return name, nil
	}
	if name := r.singleRegisteredProvider(); name != "" {
		return name, nil
	}

	return "", errors.New(errors.ErrValidationFailed, r.describeUnresolvedProvider(model))
}

// singleRegisteredProvider 返回唯一的已注册 provider；多于一个时返回空。
// 只有一个 provider 时归属不存在歧义，历史上依赖这一兜底。
func (r *LLMRuntime) singleRegisteredProvider() string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.providers) != 1 {
		return ""
	}
	for name := range r.providers {
		return name
	}
	return ""
}

// pickProviderForModel 从「声明了该 model」的 provider 中确定性地挑一个。
//
// 同一个 model 被多个 provider 声明是常态（真实配置里 claude-3-5-sonnet 就有 5 个
// 声明者）。别名表 r.aliases 是扁平的「最后写入者胜」，而 provider 注册顺序来自 map
// 迭代（随机），因此旧实现下同一请求可能落到任意一个声明者上，包括探活已经失败的
// 那些——这正是「默认模型没人能服务却要重试到预算耗尽」的来源之一。
//
// 选择顺序：显式配置的默认 provider（且确实声明了该 model）→ 健康度最好者 →
// 名字排序第一个（保证确定性，不再依赖 map 迭代顺序）。
func (r *LLMRuntime) pickProviderForModel(model string, preferred string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}

	candidates := r.providersDeclaringModel(model)
	if len(candidates) == 0 {
		return ""
	}
	if len(candidates) == 1 {
		return candidates[0]
	}

	if preferred := r.resolveRegisteredProviderName(preferred); preferred != "" {
		for _, name := range candidates {
			if name == preferred {
				return name
			}
		}
	}

	best, bestRank := candidates[0], r.providerHealthRank(candidates[0])
	for _, name := range candidates[1:] {
		if rank := r.providerHealthRank(name); rank < bestRank {
			best, bestRank = name, rank
		}
	}
	return best
}

// providersDeclaringModel 返回声明支持该 model 的已注册 provider（按名字排序）。
func (r *LLMRuntime) providersDeclaringModel(model string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providerAliases))
	for name, aliases := range r.providerAliases {
		if _, ok := aliases[model]; ok {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// providerHealthRank 越小越优先：健康 < 降级 < 未探测 < 不健康。
func (r *LLMRuntime) providerHealthRank(name string) int {
	if r.health == nil {
		return 0
	}
	health, ok := r.health.Get(name)
	if !ok {
		return 2
	}
	switch health.Status {
	case HealthStatusHealthy:
		return 0
	case HealthStatusDegraded:
		return 1
	case HealthStatusUnhealthy:
		return 3
	default:
		return 2
	}
}

// describeUnresolvedProvider 生成「没有 provider 能服务该请求」的可操作错误信息。
func (r *LLMRuntime) describeUnresolvedProvider(model string) string {
	names := r.ListProviders()
	sort.Strings(names)
	available := "(none registered)"
	if len(names) > 0 {
		available = strings.Join(names, ", ")
	}

	if model == "" {
		return fmt.Sprintf(
			"no provider available: set the request provider, configure providers.default_provider, or register a provider (registered providers: %s)",
			available)
	}
	return fmt.Sprintf(
		"no provider available for model %q: no registered provider declares this model and no default provider is configured; set the request provider, add the model to a provider's supported_models/default_model, or configure providers.default_provider (registered providers: %s)",
		model, available)
}

// ListProviders 列出所有提供者
func (r *LLMRuntime) ListProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}

	return names
}

// ProviderAliases returns the registered aliases for the given provider, including the provider name.
func (r *LLMRuntime) ProviderAliases(providerName string) []string {
	if r == nil {
		return nil
	}

	providerName = strings.TrimSpace(providerName)
	if providerName == "" {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	resolvedName := providerName
	if alias, ok := r.aliases[providerName]; ok {
		resolvedName = alias
	}
	if _, ok := r.providers[resolvedName]; !ok {
		return nil
	}

	aliases := make([]string, 0, len(r.providerAliases[resolvedName]))
	for alias := range r.providerAliases[resolvedName] {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	return aliases
}

// DefaultModel 返回 Runtime 配置的默认模型/Provider 名称
func (r *LLMRuntime) DefaultModel() string {
	if r == nil || r.config == nil {
		return ""
	}
	return r.config.DefaultModel
}

// DefaultProvider returns the configured default provider name.
func (r *LLMRuntime) DefaultProvider() string {
	if r == nil || r.config == nil {
		return ""
	}
	return r.config.DefaultProvider
}

// RetryConfigSnapshot returns a copy of the runtime retry settings.
func (r *LLMRuntime) RetryConfigSnapshot() (int, RetryTuning, []RetryRule) {
	if r == nil {
		return 0, RetryTuning{}, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.config == nil {
		return 0, RetryTuning{}, nil
	}
	return r.config.MaxRetries, r.config.RetryTuning, cloneRetryRules(r.config.RetryRules)
}

// UpdateRetryConfig updates runtime-level retry settings used by Call and Stream.
func (r *LLMRuntime) UpdateRetryConfig(maxRetries int, tuning RetryTuning, rules []RetryRule) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.config == nil {
		r.config = &RuntimeConfig{}
	}
	r.config.MaxRetries = maxRetries
	r.config.RetryTuning = tuning
	r.config.RetryRules = cloneRetryRules(rules)
}

// Call 调用 LLM（统一接口）
func (r *LLMRuntime) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if req == nil {
		return nil, errors.New(errors.ErrValidationFailed, "request cannot be nil")
	}

	if len(req.Messages) == 0 {
		return nil, errors.New(errors.ErrValidationFailed, "messages cannot be empty")
	}
	ctx = withHTTPDebugRequestMetadata(ctx, req.Metadata)

	providerName, err := r.resolveProviderForRequest(req)
	if err != nil {
		return nil, err
	}
	req.Provider = providerName

	// 获取提供者
	provider, err := r.GetProvider(providerName)
	if err != nil {
		return nil, err
	}

	maxRetries, retryTuning, retryRules := r.RetryConfigSnapshot()
	policy := newRuntimeRetryPolicy(maxRetries, 0, retryTuning, retryRules)
	policy = applyRequestRetryPolicy(policy, req.Metadata)
	var lastError error
	startedAt := time.Now()
	activeMaxAttempts := policy.initialMaxAttempts()
	consecutiveHandoffs := 0
	degenerateReplies := 0

	for attempt := 1; retryAttemptAllowed(policy.MaxAttempts, attempt); attempt++ {
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, activeMaxAttempts)
		response, err := provider.Call(attemptCtx, req)
		if err == nil {
			return response, nil
		}

		lastError = err
		// Consecutive-handoff guard: when the provider keeps fast-failing and
		// handing the transient error back to this outer loop, stop after a
		// few rounds instead of spending the whole runtime budget on the same
		// dead upstream. Any non-handoff error resets the streak.
		handoffToNextLayer := isNextLayerHandoffError(err)
		if handoffToNextLayer {
			consecutiveHandoffs++
			if consecutiveHandoffs >= maxConsecutiveFastFailHandoffs {
				// Use consecutiveHandoffs as the attempt count so
				// markRetryExhausted always wraps the error (it returns err
				// unchanged when attempts <= 1).  With MaxRetries=-1
				// (unlimited), policy.MaxAttempts would be -1, which is <= 1,
				// and the guard's "fast-fail" message would be lost, causing
				// DiagnoseFailure to report retryable=false for the provider's
				// raw exhaustion error.
				return nil, markRetryExhausted("provider call failed after repeated fast-fail retries", consecutiveHandoffs, err)
			}
		} else {
			consecutiveHandoffs = 0
		}

		// Bounded resampling for degenerate samples (empty / reasoning-only
		// reply, truncated or malformed tool arguments): the same prompt rarely
		// changes the outcome on the next sample, so stop after a few
		// consecutive degenerate replies instead of spending the whole runtime
		// attempt budget on the same degenerate response (same bound as the
		// provider loops). Explicit handoffs stay excluded: they are bounded by
		// the consecutive-handoff guard and may be answered by a different
		// provider/key on the next outer attempt.
		if !handoffToNextLayer && trackDegenerateOutputReply(&degenerateReplies, err) {
			return nil, markRetryExhausted("LLM call aborted after repeated degenerate replies", attempt, err)
		}

		retryResult, retryErr := prepareRetry(attemptCtx, policy, startedAt, attempt, err, retryExecutionMeta{
			Source:        "llm_runtime",
			Provider:      providerName,
			Model:         req.Model,
			PartialOutput: errHasPartialOutput(err),
		})
		if retryErr != nil {
			return nil, retryErr
		}
		activeMaxAttempts = retryResult.MaxAttempts
		if !retryResult.Decision.Retryable {
			return nil, err
		}
		if retryResult.Retry {
			continue
		}
	}

	return nil, errors.Wrap(errors.ErrNetworkUnavailable, "LLM call failed after retries", lastError)
}

// Stream 流式调用 LLM
func (r *LLMRuntime) Stream(ctx context.Context, req *LLMRequest) (<-chan StreamChunk, error) {
	if req == nil {
		return nil, errors.New(errors.ErrValidationFailed, "request cannot be nil")
	}

	if len(req.Messages) == 0 {
		return nil, errors.New(errors.ErrValidationFailed, "messages cannot be empty")
	}
	ctx = withHTTPDebugRequestMetadata(ctx, req.Metadata)

	req.Stream = true

	providerName, err := r.resolveProviderForRequest(req)
	if err != nil {
		return nil, err
	}
	req.Provider = providerName

	provider, err := r.GetProvider(providerName)
	if err != nil {
		return nil, err
	}

	maxRetries, retryTuning, retryRules := r.RetryConfigSnapshot()
	policy := newRuntimeRetryPolicy(maxRetries, 0, retryTuning, retryRules)
	policy = applyRequestRetryPolicy(policy, req.Metadata)
	startedAt := time.Now()
	meta := retryExecutionMeta{
		Source:   "llm_runtime",
		Provider: providerName,
		Model:    req.Model,
	}

	stream, attempt, err := openStreamWithRetry(ctx, provider, policy, startedAt, 1, policy.initialMaxAttempts(), req, meta)
	if err != nil {
		return nil, err
	}

	out := make(chan StreamChunk, 32)
	go forwardStreamWithRetry(ctx, out, provider, policy, startedAt, attempt, stream, req, meta)
	return out, nil
}

func openStreamWithRetry(ctx context.Context, provider Provider, policy retryPolicy, startedAt time.Time, startAttempt int, activeMaxAttempts int, req *LLMRequest, meta retryExecutionMeta) (<-chan StreamChunk, int, error) {
	if provider == nil {
		return nil, startAttempt, errors.New(errors.ErrValidationFailed, "provider cannot be nil")
	}
	if startAttempt < 1 {
		startAttempt = 1
	}
	if activeMaxAttempts < 1 {
		activeMaxAttempts = policy.initialMaxAttempts()
	}

	var lastErr error
	lastAttempt := startAttempt
	consecutiveHandoffs := 0
	degenerateReplies := 0
	for attempt := startAttempt; retryAttemptAllowed(policy.MaxAttempts, attempt); attempt++ {
		lastAttempt = attempt
		attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, activeMaxAttempts)
		stream, err := provider.Stream(attemptCtx, req)
		if err == nil && stream != nil {
			return stream, attempt, nil
		}
		if err == nil {
			err = fmt.Errorf("empty_stream_response: provider returned nil stream")
		}
		lastErr = err

		// Consecutive-handoff guard: same as Call — stop after repeated
		// fast-fail handoffs from the provider.
		handoffToNextLayer := isNextLayerHandoffError(err)
		if handoffToNextLayer {
			consecutiveHandoffs++
			if consecutiveHandoffs >= maxConsecutiveFastFailHandoffs {
				// Same guard as Call: use consecutiveHandoffs as the attempt
				// count so markRetryExhausted always wraps the error even when
				// the runtime budget is unlimited (MaxRetries=-1) and
				// policy.MaxAttempts would otherwise be <= 1.
				return nil, attempt, markRetryExhausted("LLM stream failed after repeated fast-fail retries", consecutiveHandoffs, err)
			}
		} else {
			consecutiveHandoffs = 0
		}

		// Same degenerate bound as Call: a stream that keeps answering with a
		// degenerate sample must not burn the whole runtime attempt budget.
		if !handoffToNextLayer && trackDegenerateOutputReply(&degenerateReplies, err) {
			return nil, attempt, markRetryExhausted("LLM stream aborted after repeated degenerate replies", attempt, err)
		}

		retryResult, retryErr := prepareRetry(attemptCtx, policy, startedAt, attempt, err, meta)
		if retryErr != nil {
			return nil, attempt, retryErr
		}
		activeMaxAttempts = retryResult.MaxAttempts
		if !retryResult.Decision.Retryable {
			return nil, attempt, err
		}
		if retryResult.Retry {
			continue
		}
		break
	}

	return nil, lastAttempt, markRetryExhausted("LLM stream failed after retries", policy.MaxAttempts, lastErr)
}

func forwardStreamWithRetry(ctx context.Context, out chan<- StreamChunk, provider Provider, policy retryPolicy, startedAt time.Time, attempt int, stream <-chan StreamChunk, req *LLMRequest, meta retryExecutionMeta) {
	defer close(out)
	activeMaxAttempts := policy.initialMaxAttempts()
	degenerateReplies := 0

	for stream != nil {
		emissionState := &streamEmissionState{}
		retrying := false

		for chunk := range stream {
			markRuntimeStreamEmission(emissionState, chunk)
			// Partial-output replay policy: transient stream errors keep
			// retrying even when partial text was already emitted (duplicated
			// partial output is accepted; the llm.retry event carries the
			// partial_output marker). Non-retryable errors fall through and
			// are forwarded to the consumer unchanged.
			if chunk.Type == EventTypeError && strings.TrimSpace(chunk.Error) != "" &&
				(!emissionState.emittedAnything() || !mustSuppressRetryAfterEmission(fmt.Errorf("%s", chunk.Error))) {
				err := fmt.Errorf("%s", chunk.Error)
				// Mid-stream degenerate failures (reasoning-only / empty reply,
				// malformed tool arguments) get the same bound as the open loop:
				// repeated resampling must not burn the whole attempt budget.
				if !isNextLayerHandoffError(err) && trackDegenerateOutputReply(&degenerateReplies, err) {
					exhausted := markRetryExhausted("LLM stream aborted after repeated degenerate replies", attempt, err)
					sendStreamChunk(ctx, out, StreamChunk{Type: EventTypeError, Error: exhausted.Error(), Done: true})
					return
				}
				attemptCtx := withHTTPDebugRetryAttempt(ctx, attempt, activeMaxAttempts)
				retryResult, retryErr := prepareRetry(attemptCtx, policy, startedAt, attempt, err, retryExecutionMeta{
					Source:        meta.Source,
					Provider:      meta.Provider,
					Protocol:      meta.Protocol,
					Model:         meta.Model,
					PartialOutput: emissionState.emittedAnything(),
				})
				if retryErr != nil {
					sendStreamChunk(ctx, out, StreamChunk{Type: EventTypeError, Error: retryErr.Error(), Done: true})
					return
				}
				if retryResult.Retry {
					nextStream, nextAttempt, openErr := openStreamWithRetry(ctx, provider, policy, startedAt, attempt+1, retryResult.MaxAttempts, req, meta)
					if openErr != nil {
						sendStreamChunk(ctx, out, StreamChunk{Type: EventTypeError, Error: openErr.Error(), Done: true})
						return
					}
					stream = nextStream
					attempt = nextAttempt
					activeMaxAttempts = retryResult.MaxAttempts
					retrying = true
					break
				}
			}

			if !sendStreamChunk(ctx, out, chunk) {
				return
			}
			if chunk.Done {
				return
			}
		}

		if !retrying {
			return
		}
	}
}

func markRuntimeStreamEmission(state *streamEmissionState, chunk StreamChunk) {
	if state == nil {
		return
	}
	switch chunk.Type {
	case EventTypeText:
		state.markText(chunk.Content)
	case EventTypeImage:
		state.markImage(chunk.Metadata)
	case EventTypeToolCall, EventTypeToolStart, EventTypeToolEnd:
		if chunk.Content != "" || chunk.ToolCall != nil || chunk.Delta != nil {
			state.markText("tool")
		}
	}
}

func sendStreamChunk(ctx context.Context, out chan<- StreamChunk, chunk StreamChunk) bool {
	select {
	case <-ctx.Done():
		return false
	case out <- chunk:
		return true
	}
}

// CountTokens 统计 Token 数
func (r *LLMRuntime) CountTokens(text string) int {
	return r.tokenizer.Count(text)
}

// CountMessagesTokens 统计消息的 Token 数
func (r *LLMRuntime) CountMessagesTokens(messages []types.Message) int {
	return countTypedMessagesTokens(r.tokenizer, messages)
}

func countTypedMessagesTokens(tokenizer *Tokenizer, messages []types.Message) int {
	if tokenizer == nil || len(messages) == 0 {
		return 0
	}
	converted := make([]interface{}, len(messages))
	for i, msg := range messages {
		content := msg.Content
		if len(msg.ContentParts) > 0 {
			content = ""
		}
		converted[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": content,
			"name":    "",
		}
	}

	total := tokenizer.CountMessages(converted)
	for _, message := range messages {
		total += countStructuredTokenField(tokenizer, message.ContentParts)
		total += countStructuredTokenField(tokenizer, message.ToolCalls)
		if toolCallID := strings.TrimSpace(message.ToolCallID); toolCallID != "" {
			total += tokenizer.Count(toolCallID)
		}
	}
	return total
}

func countChatMessagesTokens(tokenizer *Tokenizer, messages []Message) int {
	if tokenizer == nil || len(messages) == 0 {
		return 0
	}
	converted := make([]interface{}, len(messages))
	for index, message := range messages {
		content := message.Content
		if len(message.ContentParts) > 0 {
			content = ""
		}
		converted[index] = map[string]interface{}{
			"role":    message.Role,
			"content": content,
			"name":    "",
		}
	}

	total := tokenizer.CountMessages(converted)
	for _, message := range messages {
		total += countStructuredTokenField(tokenizer, message.ContentParts)
		total += countStructuredTokenField(tokenizer, message.ToolCalls)
		if toolCallID := strings.TrimSpace(message.ToolCallID); toolCallID != "" {
			total += tokenizer.Count(toolCallID)
		}
		if reasoning := strings.TrimSpace(message.Reasoning); reasoning != "" {
			total += tokenizer.Count(reasoning)
		}
	}
	return total
}

func countStructuredTokenField(tokenizer *Tokenizer, value interface{}) int {
	encoded, err := json.Marshal(value)
	serialized := string(encoded)
	if err != nil || serialized == "null" || serialized == "[]" {
		return 0
	}
	return tokenizer.Count(serialized)
}

// GetCapabilities 获取指定模型的能力
func (r *LLMRuntime) GetCapabilities(model string) (*ModelCapabilities, error) {
	provider, err := r.GetProvider(model)
	if err != nil {
		return nil, err
	}

	return provider.GetCapabilities(), nil
}

// CheckHealth 检查所有提供者的健康状况
func (r *LLMRuntime) CheckHealth(ctx context.Context) map[string]error {
	return r.CheckHealthWithMode(ctx, HealthCheckModeAll)
}

// CheckHealthWithMode 根据模式检查提供者健康状态
func (r *LLMRuntime) CheckHealthWithMode(ctx context.Context, mode HealthCheckMode) map[string]error {
	r.mu.RLock()
	providers := make(map[string]Provider, len(r.providers))
	for name, provider := range r.providers {
		providers[name] = provider
	}
	r.mu.RUnlock()

	results := make(map[string]error)
	var wg sync.WaitGroup
	var mu sync.Mutex

	for name, provider := range providers {
		if !r.shouldCheckProvider(name, mode) {
			continue
		}
		wg.Add(1)
		go func(n string, p Provider) {
			defer wg.Done()
			err := p.CheckHealth(ctx)
			if r.health != nil {
				r.health.RecordCheck(n, err)
			}
			mu.Lock()
			results[n] = err
			mu.Unlock()
		}(name, provider)
	}

	wg.Wait()
	return results
}

// ProviderHealthSnapshot 返回 provider 健康状态快照
func (r *LLMRuntime) ProviderHealthSnapshot() map[string]ProviderHealth {
	if r == nil || r.health == nil {
		return nil
	}
	return r.health.Snapshot()
}

func (r *LLMRuntime) shouldCheckProvider(name string, mode HealthCheckMode) bool {
	if mode == HealthCheckModeNone {
		return false
	}
	if r.health == nil {
		return true
	}

	switch mode {
	case HealthCheckModeAll:
		return true
	case HealthCheckModeUnhealthy:
		health, ok := r.health.Get(name)
		if !ok {
			return true
		}
		return health.Status != HealthStatusHealthy
	case HealthCheckModeStale:
		return r.health.IsStale(name)
	default:
		return true
	}
}

// AddRoutingRule 添加路由规则
func (r *LLMRuntime) AddRoutingRule(rule *RoutingRule) {
	r.router.AddRule(rule)
}

// RemoveRoutingRule 移除路由规则
func (r *LLMRuntime) RemoveRoutingRule(model string) {
	r.router.RemoveRule(model)
}

// SetTokenizer 设置 Token 计数器
func (r *LLMRuntime) SetTokenizer(tokenizer *Tokenizer) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tokenizer = tokenizer
}

// GetTokenizer 获取 Token 计数器
func (r *LLMRuntime) GetTokenizer() *Tokenizer {
	r.mu.RLock()
	defer r.mu.RUnlock()

	return r.tokenizer
}

// DefaultCondition 默认路由条件
type DefaultCondition struct{}

func (c *DefaultCondition) Match(req *LLMRequest) bool {
	return true
}

// RoutingCondition 路由条件接口
type RoutingCondition interface {
	Match(req *LLMRequest) bool
}

// RoutingRule 路由规则
type RoutingRule struct {
	Model     string
	Condition RoutingCondition
	Priority  int
}

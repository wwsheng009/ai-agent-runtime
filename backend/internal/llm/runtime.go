package llm

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ai-gateway/ai-agent-runtime/internal/errors"
	"github.com/ai-gateway/ai-agent-runtime/internal/types"
)

// RuntimeConfig Runtime 配置
type RuntimeConfig struct {
	DefaultProvider string            `yaml:"defaultProvider,omitempty" json:"defaultProvider,omitempty"`
	DefaultModel    string            `yaml:"defaultModel" json:"defaultModel"`
	DefaultTimeout  time.Duration     `yaml:"defaultTimeout" json:"defaultTimeout"`
	MaxRetries      int               `yaml:"maxRetries" json:"maxRetries"`
	Providers       map[string]string `yaml:"providers" json:"providers"` // provider name -> type
	HealthCheck     HealthCheckConfig `yaml:"healthCheck,omitempty" json:"healthCheck,omitempty"`
}

// LLMRuntime LLM 运行时
type LLMRuntime struct {
	providers map[string]Provider
	aliases   map[string]string
	router    *ModelRouter
	config    *RuntimeConfig
	tokenizer *Tokenizer
	health    *ProviderHealthTracker
	mu        sync.RWMutex
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
	Thinking        *ThinkingConfig        `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Stream          bool                   `json:"stream,omitempty" yaml:"stream,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
}

// LLMResponse LLM 响应
type LLMResponse struct {
	Content   string                 `json:"content" yaml:"content"`
	ToolCalls []types.ToolCall       `json:"toolCalls,omitempty" yaml:"toolCalls,omitempty"`
	Usage     *types.TokenUsage      `json:"usage,omitempty" yaml:"usage,omitempty"`
	Model     string                 `json:"model" yaml:"model"`
	Reasoning string                 `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty" yaml:"metadata,omitempty"`
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
}

// StreamEventType 流事件类型
type StreamEventType string

const (
	EventTypeText      StreamEventType = "text"
	EventTypeToolCall  StreamEventType = "tool_call"
	EventTypeToolStart StreamEventType = "tool_start"
	EventTypeToolEnd   StreamEventType = "tool_end"
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
			MaxRetries:      3,
			Providers:       make(map[string]string),
			HealthCheck:     DefaultHealthCheckConfig(),
		}
	}

	return &LLMRuntime{
		providers: make(map[string]Provider),
		aliases:   make(map[string]string),
		router:    NewModelRouter(),
		config:    config,
		tokenizer: NewTokenizer("simple"),
		health:    NewProviderHealthTracker(config.HealthCheck),
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
	if r.health != nil {
		r.health.AddProvider(name)
	}

	// 添加路由规则（如果提供了模型信息）
	if caps := provider.GetCapabilities(); caps != nil {
		r.router.AddRule(&RoutingRule{
			Model:     name,
			Condition: &DefaultCondition{},
		})
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
		if r.config.MaxRetries > 0 {
			gatewayClient.SetMaxRetries(r.config.MaxRetries)
		}
	}

	return r.RegisterProvider(name, gatewayClient)
}

// UnregisterProvider 注销 LLM 提供者
func (r *LLMRuntime) UnregisterProvider(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.providers, name)
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

// Call 调用 LLM（统一接口）
func (r *LLMRuntime) Call(ctx context.Context, req *LLMRequest) (*LLMResponse, error) {
	if req == nil {
		return nil, errors.New(errors.ErrValidationFailed, "request cannot be nil")
	}

	if len(req.Messages) == 0 {
		return nil, errors.New(errors.ErrValidationFailed, "messages cannot be empty")
	}

	providerName := strings.TrimSpace(req.Provider)
	if providerName == "" {
		providerName = r.resolveRegisteredProviderName(req.Model)
	}
	if providerName == "" && r.config != nil {
		providerName = strings.TrimSpace(r.config.DefaultProvider)
	}
	if providerName == "" {
		providerName = r.router.Route(req)
	}
	if providerName == "" {
		providerName = strings.TrimSpace(req.Model)
	}
	if providerName == "" && r.config != nil {
		providerName = r.resolveRegisteredProviderName(r.config.DefaultModel)
		if providerName == "" {
			providerName = strings.TrimSpace(r.config.DefaultModel)
		}
	}
	if req.Model == "" && r.config != nil {
		req.Model = strings.TrimSpace(r.config.DefaultModel)
	}
	req.Provider = providerName

	// 获取提供者
	provider, err := r.GetProvider(providerName)
	if err != nil {
		return nil, err
	}

	var lastError error
	maxRetries := r.config.MaxRetries

	for attempt := 0; attempt <= maxRetries; attempt++ {
		response, err := provider.Call(ctx, req)
		if err == nil {
			return response, nil
		}

		lastError = err

		if attempt == maxRetries {
			break
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
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

	req.Stream = true

	providerName := strings.TrimSpace(req.Provider)
	if providerName == "" {
		providerName = r.resolveRegisteredProviderName(req.Model)
	}
	if providerName == "" && r.config != nil {
		providerName = strings.TrimSpace(r.config.DefaultProvider)
	}
	if providerName == "" {
		providerName = r.router.Route(req)
	}
	if providerName == "" {
		providerName = strings.TrimSpace(req.Model)
	}
	if providerName == "" && r.config != nil {
		providerName = r.resolveRegisteredProviderName(r.config.DefaultModel)
		if providerName == "" {
			providerName = strings.TrimSpace(r.config.DefaultModel)
		}
	}
	if req.Model == "" && r.config != nil {
		req.Model = strings.TrimSpace(r.config.DefaultModel)
	}
	req.Provider = providerName

	provider, err := r.GetProvider(providerName)
	if err != nil {
		return nil, err
	}

	return provider.Stream(ctx, req)
}

// CountTokens 统计 Token 数
func (r *LLMRuntime) CountTokens(text string) int {
	return r.tokenizer.Count(text)
}

// CountMessagesTokens 统计消息的 Token 数
func (r *LLMRuntime) CountMessagesTokens(messages []types.Message) int {
	// 转换 []types.Message 到 []interface{} 以适配 tokenizer.CountMessages
	converted := make([]interface{}, len(messages))
	for i, msg := range messages {
		converted[i] = map[string]interface{}{
			"role":    msg.Role,
			"content": msg.Content,
			"name":    "",
		}
	}
	return r.tokenizer.CountMessages(converted)
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

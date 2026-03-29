package llm

import (
	"sync"
	"time"
)

// HealthStatus 表示 Provider 健康状态
type HealthStatus string

const (
	HealthStatusUnknown   HealthStatus = "unknown"
	HealthStatusHealthy   HealthStatus = "healthy"
	HealthStatusDegraded  HealthStatus = "degraded"
	HealthStatusUnhealthy HealthStatus = "unhealthy"
)

// HealthCheckMode 控制健康检查的触发策略
type HealthCheckMode string

const (
	HealthCheckModeAll       HealthCheckMode = "all"
	HealthCheckModeUnhealthy HealthCheckMode = "unhealthy"
	HealthCheckModeStale     HealthCheckMode = "stale"
	HealthCheckModeNone      HealthCheckMode = "none"
)

// HealthCheckConfig 健康检查配置
type HealthCheckConfig struct {
	DegradedThreshold  int           `yaml:"degradedThreshold" json:"degradedThreshold"`
	UnhealthyThreshold int           `yaml:"unhealthyThreshold" json:"unhealthyThreshold"`
	HealthyThreshold   int           `yaml:"healthyThreshold" json:"healthyThreshold"`
	StaleAfter         time.Duration `yaml:"staleAfter" json:"staleAfter"`
}

// DefaultHealthCheckConfig 默认健康检查配置
func DefaultHealthCheckConfig() HealthCheckConfig {
	return HealthCheckConfig{
		DegradedThreshold:  1,
		UnhealthyThreshold: 3,
		HealthyThreshold:   2,
		StaleAfter:         5 * time.Minute,
	}
}

func normalizeHealthCheckConfig(cfg HealthCheckConfig) HealthCheckConfig {
	defaults := DefaultHealthCheckConfig()
	if cfg.DegradedThreshold <= 0 {
		cfg.DegradedThreshold = defaults.DegradedThreshold
	}
	if cfg.UnhealthyThreshold <= 0 {
		cfg.UnhealthyThreshold = defaults.UnhealthyThreshold
	}
	if cfg.HealthyThreshold <= 0 {
		cfg.HealthyThreshold = defaults.HealthyThreshold
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = defaults.StaleAfter
	}
	return cfg
}

// ProviderHealth 记录 Provider 健康状态
type ProviderHealth struct {
	Status               HealthStatus
	ConsecutiveFailures  int
	ConsecutiveSuccesses int
	LastCheckTime        time.Time
	LastSuccessTime      time.Time
	LastFailureTime      time.Time
	LastError            string
}

// ProviderHealthTracker 跟踪 Provider 健康状态
type ProviderHealthTracker struct {
	mu       sync.RWMutex
	config   HealthCheckConfig
	providers map[string]*ProviderHealth
}

// NewProviderHealthTracker 创建健康追踪器
func NewProviderHealthTracker(config HealthCheckConfig) *ProviderHealthTracker {
	normalized := normalizeHealthCheckConfig(config)
	return &ProviderHealthTracker{
		config:   normalized,
		providers: make(map[string]*ProviderHealth),
	}
}

// AddProvider 初始化 Provider 健康状态
func (t *ProviderHealthTracker) AddProvider(name string) {
	if name == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, exists := t.providers[name]; exists {
		return
	}
	t.providers[name] = &ProviderHealth{Status: HealthStatusUnknown}
}

// RemoveProvider 删除 Provider 健康记录
func (t *ProviderHealthTracker) RemoveProvider(name string) {
	if name == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.providers, name)
}

// RecordCheck 记录一次健康检查结果
func (t *ProviderHealthTracker) RecordCheck(name string, err error) ProviderHealth {
	t.mu.Lock()
	defer t.mu.Unlock()

	if name == "" {
		return ProviderHealth{}
	}

	health := t.providers[name]
	if health == nil {
		health = &ProviderHealth{Status: HealthStatusUnknown}
		t.providers[name] = health
	}

	now := time.Now()
	health.LastCheckTime = now

	if err == nil {
		health.LastError = ""
		health.LastSuccessTime = now
		health.ConsecutiveSuccesses++
		health.ConsecutiveFailures = 0

		if health.Status == HealthStatusUnknown || health.ConsecutiveSuccesses >= t.config.HealthyThreshold {
			health.Status = HealthStatusHealthy
		}
	} else {
		health.LastError = err.Error()
		health.LastFailureTime = now
		health.ConsecutiveFailures++
		health.ConsecutiveSuccesses = 0

		switch {
		case health.ConsecutiveFailures >= t.config.UnhealthyThreshold:
			health.Status = HealthStatusUnhealthy
		case health.ConsecutiveFailures >= t.config.DegradedThreshold:
			health.Status = HealthStatusDegraded
		default:
			if health.Status == HealthStatusUnknown {
				health.Status = HealthStatusDegraded
			}
		}
	}

	return *health
}

// Snapshot 返回健康状态快照（副本）
func (t *ProviderHealthTracker) Snapshot() map[string]ProviderHealth {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]ProviderHealth, len(t.providers))
	for name, health := range t.providers {
		if health == nil {
			continue
		}
		result[name] = *health
	}
	return result
}

// Get 获取指定 Provider 的健康状态
func (t *ProviderHealthTracker) Get(name string) (ProviderHealth, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	health, ok := t.providers[name]
	if !ok || health == nil {
		return ProviderHealth{}, false
	}
	return *health, true
}

// IsStale 判断健康状态是否过期
func (t *ProviderHealthTracker) IsStale(name string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	health, ok := t.providers[name]
	if !ok || health == nil {
		return true
	}
	if health.LastCheckTime.IsZero() {
		return true
	}
	if t.config.StaleAfter <= 0 {
		return false
	}
	return time.Since(health.LastCheckTime) > t.config.StaleAfter
}

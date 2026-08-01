package supervision

import "time"

// Config 集中 P2 控制面调参（doc 6.2-6.6）。零值字段在 WithDefaults 中
// 使用语义默认值，便于 yaml/json 配置部分覆盖。
type Config struct {
	// ExecutionDeadline 是 child execution 的默认 deadline 阈值。运行时
	// 超时扫描用它判定 timed_out 并生成 critical notification。
	// 0 使用默认 30m。
	ExecutionDeadline time.Duration `json:"execution_deadline,omitempty" yaml:"execution_deadline,omitempty"`
	// HeartbeatTimeout 是 heartbeat/progress 无更新的 stall 阈值。
	// 0 使用默认 5m。
	HeartbeatTimeout time.Duration `json:"heartbeat_timeout,omitempty" yaml:"heartbeat_timeout,omitempty"`
	// DigestMaxItems 限制单个 preflight digest 的最大条目数（doc 6.4）。
	// 0 使用默认 20。
	DigestMaxItems int `json:"digest_max_items,omitempty" yaml:"digest_max_items,omitempty"`
	// DigestMaxChars 限制 digest 文本预算，避免撑爆父上下文。
	// 0 使用默认 4000。
	DigestMaxChars int `json:"digest_max_chars,omitempty" yaml:"digest_max_chars,omitempty"`
	// ActionTTL 是 pending action 的存活时间；超时后由扫描标记 failed。
	// 0 使用默认 24h。
	ActionTTL time.Duration `json:"action_ttl,omitempty" yaml:"action_ttl,omitempty"`
	// WakeRateWindow 与 WakeMaxAutoWake 传给 WakeScheduler（doc 6.5 规则 4）。
	// 0 分别使用默认 1h 与 5。
	WakeRateWindow  time.Duration `json:"wake_rate_window,omitempty" yaml:"wake_rate_window,omitempty"`
	WakeMaxAutoWake int           `json:"wake_max_auto_wake,omitempty" yaml:"wake_max_auto_wake,omitempty"`
}

// DefaultConfig 返回默认调参。
func DefaultConfig() Config {
	return Config{
		ExecutionDeadline: 30 * time.Minute,
		HeartbeatTimeout:  5 * time.Minute,
		DigestMaxItems:    20,
		DigestMaxChars:    4000,
		ActionTTL:         24 * time.Hour,
		WakeRateWindow:    time.Hour,
		WakeMaxAutoWake:   5,
	}
}

// WithDefaults 返回补齐默认值后的配置副本，调用方字段保持原值。
func (c Config) WithDefaults() Config {
	d := DefaultConfig()
	if c.ExecutionDeadline > 0 {
		d.ExecutionDeadline = c.ExecutionDeadline
	}
	if c.HeartbeatTimeout > 0 {
		d.HeartbeatTimeout = c.HeartbeatTimeout
	}
	if c.DigestMaxItems > 0 {
		d.DigestMaxItems = c.DigestMaxItems
	}
	if c.DigestMaxChars > 0 {
		d.DigestMaxChars = c.DigestMaxChars
	}
	if c.ActionTTL > 0 {
		d.ActionTTL = c.ActionTTL
	}
	if c.WakeRateWindow > 0 {
		d.WakeRateWindow = c.WakeRateWindow
	}
	if c.WakeMaxAutoWake > 0 {
		d.WakeMaxAutoWake = c.WakeMaxAutoWake
	}
	return d
}

// WakeSchedulerConfig 导出给装配层使用的 wake 调参。
func (c Config) WakeSchedulerConfig() WakeSchedulerConfig {
	cfg := c.WithDefaults()
	return WakeSchedulerConfig{
		RateWindow:           cfg.WakeRateWindow,
		MaxAutoWakePerWindow: cfg.WakeMaxAutoWake,
	}
}

package runtimeobserve

import "time"

// Config 描述 Runtime Observation Plane 的有效配置。
//
// 该配置遵循方案 Phase 0–2 的默认关闭原则：enabled=false 时不会注册任何
// 观测路由，也不会有 bus 订阅或事故 ring。所有内存/字节/客户端上限都必须
// 由服务端强制，客户端不能通过请求参数放大。
type Config struct {
	// Enabled 是否启用观测平面。生产默认 false。
	Enabled bool `yaml:"enabled" json:"enabled"`

	// RoutePrefix 观测 API 的版本化前缀，默认 /api/runtime/observe/v1。
	RoutePrefix string `yaml:"route_prefix" json:"route_prefix"`

	// RedactionProfile 使用的脱敏 profile，v1 固定为 safe_default。
	RedactionProfile string `yaml:"redaction_profile" json:"redaction_profile"`

	// HMACKeyRef 是部署级指纹密钥的引用名（仅用于观测指纹，不返回密钥本身）。
	HMACKeyRef string `yaml:"hmac_key_ref" json:"hmac_key_ref"`

	// Ring 相关限额（Phase 2）：低延迟全局流缓存。
	RetentionEvents int           `yaml:"retention_events" json:"retention_events"`
	RetentionBytes  int64         `yaml:"retention_bytes" json:"retention_bytes"`
	RetentionTTL    time.Duration `yaml:"retention_ttl" json:"retention_ttl"`

	// Ingress 限额：Bus 回调只做非阻塞入队，满则丢弃并计 gap。
	IngressQueueEvents int   `yaml:"ingress_queue_events" json:"ingress_queue_events"`
	IngressQueueBytes  int64 `yaml:"ingress_queue_bytes" json:"ingress_queue_bytes"`

	// Per-client/query 限额（Phase 3 SSE 用；query 在 Phase 2 已生效）。
	SubscriberQueueEvents int   `yaml:"subscriber_queue_events" json:"subscriber_queue_events"`
	SubscriberQueueBytes  int64 `yaml:"subscriber_queue_bytes" json:"subscriber_queue_bytes"`
	MaxClients            int   `yaml:"max_clients" json:"max_clients"`

	// 单项/整包大小上限。projection 后再检查。
	MaxEventBytes       int   `yaml:"max_event_bytes" json:"max_event_bytes"`
	MaxSnapshotBytes    int   `yaml:"max_snapshot_bytes" json:"max_snapshot_bytes"`
	DefaultQueryLimit   int   `yaml:"default_query_limit" json:"default_query_limit"`
	MaxQueryLimit       int   `yaml:"max_query_limit" json:"max_query_limit"`
	QueryTimeout        time.Duration `yaml:"query_timeout" json:"query_timeout"`

	// Heartbeat / 采样（Phase 3 SSE 用）。
	Heartbeat time.Duration `yaml:"heartbeat" json:"heartbeat"`

	// ExposeProviderRequestID 是否在事件中暴露 provider request ID（默认 false）。
	ExposeProviderRequestID bool `yaml:"expose_provider_request_id" json:"expose_provider_request_id"`

	// IncludeRenderer 保留给 Phase 4（默认 true 但当前 renderer 不可用）。
	IncludeRenderer         bool          `yaml:"include_renderer" json:"include_renderer"`
	RendererPublishInterval time.Duration `yaml:"renderer_publish_interval" json:"renderer_publish_interval"`
}

// DefaultConfig 返回 v1 建议默认值（详见方案 §10.1）。
func DefaultConfig() Config {
	return Config{
		Enabled:               false,
		RoutePrefix:           "/api/runtime/observe/v1",
		RedactionProfile:      RedactionProfileSafeDefault,
		HMACKeyRef:            "runtime-observe-fingerprint-v1",
		RetentionEvents:       4096,
		RetentionBytes:        16 * 1024 * 1024,
		RetentionTTL:          10 * time.Minute,
		IngressQueueEvents:    1024,
		IngressQueueBytes:     4 * 1024 * 1024,
		SubscriberQueueEvents: 256,
		SubscriberQueueBytes:  1 * 1024 * 1024,
		MaxClients:            32,
		MaxEventBytes:         64 * 1024,
		MaxSnapshotBytes:      256 * 1024,
		DefaultQueryLimit:     50,
		MaxQueryLimit:         200,
		QueryTimeout:          2 * time.Second,
		Heartbeat:             15 * time.Second,
		ExposeProviderRequestID: false,
		IncludeRenderer:         true,
		RendererPublishInterval: 100 * time.Millisecond,
	}
}

// WithDefaults 将零值字段补齐为默认值，并夹紧越界限额。
func WithDefaults(cfg Config) Config {
	def := DefaultConfig()
	if cfg.RoutePrefix == "" {
		cfg.RoutePrefix = def.RoutePrefix
	}
	if cfg.RedactionProfile == "" {
		cfg.RedactionProfile = def.RedactionProfile
	}
	if cfg.HMACKeyRef == "" {
		cfg.HMACKeyRef = def.HMACKeyRef
	}
	if cfg.RetentionEvents <= 0 {
		cfg.RetentionEvents = def.RetentionEvents
	}
	if cfg.RetentionBytes <= 0 {
		cfg.RetentionBytes = def.RetentionBytes
	}
	if cfg.RetentionTTL <= 0 {
		cfg.RetentionTTL = def.RetentionTTL
	}
	if cfg.IngressQueueEvents <= 0 {
		cfg.IngressQueueEvents = def.IngressQueueEvents
	}
	if cfg.IngressQueueBytes <= 0 {
		cfg.IngressQueueBytes = def.IngressQueueBytes
	}
	if cfg.SubscriberQueueEvents <= 0 {
		cfg.SubscriberQueueEvents = def.SubscriberQueueEvents
	}
	if cfg.SubscriberQueueBytes <= 0 {
		cfg.SubscriberQueueBytes = def.SubscriberQueueBytes
	}
	if cfg.MaxClients <= 0 {
		cfg.MaxClients = def.MaxClients
	}
	if cfg.MaxEventBytes <= 0 {
		cfg.MaxEventBytes = def.MaxEventBytes
	}
	if cfg.MaxSnapshotBytes <= 0 {
		cfg.MaxSnapshotBytes = def.MaxSnapshotBytes
	}
	if cfg.DefaultQueryLimit <= 0 {
		cfg.DefaultQueryLimit = def.DefaultQueryLimit
	}
	if cfg.MaxQueryLimit < cfg.DefaultQueryLimit {
		cfg.MaxQueryLimit = cfg.DefaultQueryLimit
	}
	if cfg.QueryTimeout <= 0 {
		cfg.QueryTimeout = def.QueryTimeout
	}
	if cfg.Heartbeat <= 0 {
		cfg.Heartbeat = def.Heartbeat
	}
	if cfg.RendererPublishInterval <= 0 {
		cfg.RendererPublishInterval = def.RendererPublishInterval
	}
	return cfg
}

// ClampLimit 把客户端请求的 limit 限制在默认/最大范围内。
func (c Config) ClampLimit(requested int) int {
	if requested <= 0 {
		return c.DefaultQueryLimit
	}
	if requested > c.MaxQueryLimit {
		return c.MaxQueryLimit
	}
	return requested
}

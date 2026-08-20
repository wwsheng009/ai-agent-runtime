package runtimeobserve

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync/atomic"
	"time"
)

// ProcessSource 提供进程级低敏摘要。
type ProcessSource interface {
	ObservationProcessSummary() ProcessSummary
}

// SessionSource 提供低敏 session 摘要（由 Handler 注入实现）。
type SessionSource interface {
	ObservationSessionSummaries(ctx context.Context, limit int) ([]SessionSummary, error)
	ObservationSession(ctx context.Context, sessionID string) (SessionSummary, bool, error)
}

// Service 是观测平面的查询服务：能力、快照、事件查询和 session 查询。
type Service struct {
	cfg       Config
	collector *Collector
	redactor  *Redactor
	process   ProcessSource
	sessions  SessionSource

	snapshotRevision uint64
}

// NewService 创建观测服务。collector 可为 nil（对应 enabled=false 或未启动）。
func NewService(cfg Config, collector *Collector, redactor *Redactor, process ProcessSource, sessions SessionSource) *Service {
	if redactor == nil {
		redactor = NewRedactor(nil, "", cfg.RedactionProfile)
	}
	return &Service{
		cfg:       cfg,
		collector: collector,
		redactor:  redactor,
		process:   process,
		sessions:  sessions,
	}
}

// Enabled 返回观测平面是否启用。
func (s *Service) Enabled() bool {
	return s != nil && s.cfg.Enabled && s.collector != nil
}

// Redactor 返回 redactor（用于响应 redaction 摘要）。
func (s *Service) Redactor() *Redactor {
	if s == nil {
		return nil
	}
	return s.redactor
}

// Close 停止底层 collector，释放 bus 订阅。
func (s *Service) Close() {
	if s == nil || s.collector == nil {
		return
	}
	s.collector.Stop()
}

// Capabilities 构建观测能力摘要。
func (s *Service) Capabilities() Capabilities {
	if s == nil {
		return Capabilities{Enabled: false}
	}
	epoch := ""
	oldest, latest := int64(0), int64(0)
	if s.collector != nil {
		meta := s.collector.CursorMetaInfo()
		epoch = meta.Epoch
		oldest, latest = s.collector.RingBounds()
	}
	allowlist := make([]string, 0, len(eventAllowlist))
	for etype := range eventAllowlist {
		allowlist = append(allowlist, etype)
	}
	sort.Strings(allowlist)
	return Capabilities{
		SchemaVersion: SchemaVersionResponse,
		Enabled:       s.Enabled(),
		InstanceEpoch: epoch,
		Retention: map[string]int64{
			"retention_events": int64(s.cfg.RetentionEvents),
			"retention_bytes":  s.cfg.RetentionBytes,
			"retention_ttl_ns": int64(s.cfg.RetentionTTL),
		},
		Limits: map[string]int64{
			"ingress_queue_events":   int64(s.cfg.IngressQueueEvents),
			"ingress_queue_bytes":    s.cfg.IngressQueueBytes,
			"subscriber_queue_events": int64(s.cfg.SubscriberQueueEvents),
			"subscriber_queue_bytes": s.cfg.SubscriberQueueBytes,
			"max_clients":            int64(s.cfg.MaxClients),
			"max_event_bytes":        int64(s.cfg.MaxEventBytes),
			"max_snapshot_bytes":     int64(s.cfg.MaxSnapshotBytes),
			"heartbeat_ms":           s.cfg.Heartbeat.Milliseconds(),
			"query_timeout_ms":       s.cfg.QueryTimeout.Milliseconds(),
		},
		Redaction: RedactionCapability{
			Profile:       s.cfg.RedactionProfile,
			OmittedFields: s.redactor.OmittedFields(),
			HMACScheme:    "hmac:" + hmacScheme,
			HMACKeySet:    s.redactor.KeySet(),
			KeyVersion:    s.redactor.KeyVersion(),
		},
		Query: QueryCapability{
			DefaultLimit:       s.cfg.DefaultQueryLimit,
			MaxLimit:           s.cfg.MaxQueryLimit,
			RetentionEvents:    s.cfg.RetentionEvents,
			OldestAvailableSeq: oldest,
			LatestSeq:          latest,
		},
		Stream: StreamCapability{
			Enabled:     false, // Phase 3 之前不开放 SSE。
			HeartbeatMS: int(s.cfg.Heartbeat.Milliseconds()),
			MaxClients:  s.cfg.MaxClients,
			Protocol:    "",
		},
		Renderer: RendererCapability{
			Available:       false, // Phase 4 之前不可用。
			PublisherLinked: false,
			Reason:          "renderer_observation_not_implemented",
		},
		EventAllowlist: allowlist,
	}
}

// BuildSnapshot 组装复合只读快照。
func (s *Service) BuildSnapshot(ctx context.Context, includeSessions bool) (Snapshot, error) {
	if !s.Enabled() {
		return Snapshot{}, fmt.Errorf("%s: runtime observation disabled", ErrCodeDisabled)
	}
	now := time.Now().UTC()
	runtimeSummary, llm := s.collector.Stats()
	meta := s.collector.CursorMetaInfo()
	process := s.processSummary()

	items := []SessionSummary{}
	count := 0
	partial := false
	warnings := []string{}
	var sessionsComponentRevision int64
	if includeSessions && s.sessions != nil {
		sesItems, err := s.sessions.ObservationSessionSummaries(ctx, s.cfg.MaxQueryLimit)
		if err != nil {
			partial = true
			warnings = append(warnings, "session summaries unavailable: "+err.Error())
		} else {
			items = sesItems
			count = len(sesItems)
			for _, it := range sesItems {
				if it.RuntimeStateRevision > sessionsComponentRevision {
					sessionsComponentRevision = it.RuntimeStateRevision
				}
			}
		}
	} else if includeSessions {
		partial = true
		warnings = append(warnings, "session source not configured")
	}

	revision := s.revision()
	components := map[string]ComponentMeta{
		"runtime":  {Revision: meta.Bounds.ObservationSeq, CapturedAt: now},
		"sessions": {Revision: sessionsComponentRevision, CapturedAt: now},
	}
	consistency := Consistency{Kind: "component_consistent", Partial: partial}
	if partial {
		consistency.Kind = "component_partial"
	}
	if len(warnings) > 0 {
		consistency.Warnings = warnings
	}

	return Snapshot{
		SchemaVersion:     SchemaVersionResponse,
		SnapshotRevision:  int64(revision),
		CapturedAt:        now,
		FreshnessMS:       s.freshnessMS(meta),
		Consistency:       consistency,
		Process:           process,
		Runtime:           runtimeSummary,
		LLM:               llm,
		Sessions:          SessionCollection{Items: items, Count: count, Partial: partial},
		Cursor:            meta.Bounds,
		Components:        components,
	}, nil
}

// SessionFor 查询单个 session 摘要。
func (s *Service) SessionFor(ctx context.Context, sessionID string) (SessionSummary, error) {
	if !s.Enabled() {
		return SessionSummary{}, fmt.Errorf("%s: runtime observation disabled", ErrCodeDisabled)
	}
	if s.sessions == nil {
		return SessionSummary{}, fmt.Errorf("%s: session source not configured", ErrCodeInternal)
	}
	summary, ok, err := s.sessions.ObservationSession(ctx, sessionID)
	if err != nil {
		return SessionSummary{}, err
	}
	if !ok {
		return SessionSummary{}, fmt.Errorf("%s: session not found", ErrCodeSessionNotFound)
	}
	return summary, nil
}

// QueryEvents 查询低敏事件。
func (s *Service) QueryEvents(q EventQuery) (QueryResult, error) {
	if !s.Enabled() {
		return QueryResult{}, fmt.Errorf("%s: runtime observation disabled", ErrCodeDisabled)
	}
	q.Limit = s.cfg.ClampLimit(q.Limit)
	return s.collector.Query(q)
}

// RevisionContinuation 返回可用于 ETag 的当前快照修订。
func (s *Service) RevisionContinuation() uint64 {
	return s.revision()
}

func (s *Service) revision() uint64 {
	var seq int64
	if s.collector != nil {
		meta := s.collector.CursorMetaInfo()
		seq = meta.Bounds.ObservationSeq
	}
	atomic.StoreUint64(&s.snapshotRevision, uint64(seq))
	return uint64(seq)
}

func (s *Service) freshnessMS(meta CursorMeta) int64 {
	bounds := meta.Bounds
	if bounds.ObservationSeq <= 0 {
		return 0
	}
	if s.collector != nil {
		lastEvent := s.collector.stateLastEvent()
		if lastEvent != nil {
			return time.Since(*lastEvent).Milliseconds()
		}
	}
	return 0
}

func (s *Service) processSummary() ProcessSummary {
	process := ProcessSummary{
		ObservationEnabled: s.Enabled(),
	}
	if s.process != nil {
		process = s.process.ObservationProcessSummary()
	}
	process.ObservationEnabled = s.Enabled()
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	process.Goroutines = runtime.NumGoroutine()
	process.HeapBytes = mem.HeapAlloc
	if s.collector != nil {
		meta := s.collector.CursorMetaInfo()
		if process.InstanceID == "" {
			process.InstanceID = meta.InstanceID
		}
		uptime := time.Since(meta.StartedAt).Milliseconds()
		if uptime > 0 {
			process.UptimeMS = uptime
		}
	}
	if process.PID <= 0 {
		process.PID = runtimeProcessID()
	}
	return process
}

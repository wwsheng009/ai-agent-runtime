package runtimeobserve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/llm"
)

// LLMAggregates 是 LLM 维度的内存聚合（只增计数 + 活跃计数）。
type LLMAggregates struct {
	RequestsTotal  int64
	ActiveRequests int64
	RetriesTotal   int64
	StreamCount    int64
	Usage          UsageSummary
	ByProvider     map[string]*ProviderCounters
	ByModel        map[string]int64
}

// ProviderCounters 是单 provider 的聚合计数。
type ProviderCounters struct {
	Requests int64
	Errors   int64
	Retries  int64
}

// RuntimeCounters 是 runtime 维度的自监控计数。
type RuntimeCounters struct {
	IngressDropped   uint64
	UnknownDropped   uint64
	ProjectionErrors uint64
	GapCount         uint64
	LastGapAt        *time.Time
	LastEventAt      *time.Time
	RunningTurns     int64
}

// ingressItem 是 bus → collector 的单条入队项。
type ingressItem struct {
	kind  string // "runtime" | "debug" | "retry"
	event runtimeevents.Event
	debug llm.HTTPDebugEvent
	retry llm.RetryEvent
}

// ringSlot 是 retention ring 的单槽。
type ringSlot struct {
	seq   int64
	ts    time.Time
	event Event
	bytes int
}

// Collector 负责轻量 ingress、序列分配、投影、去重和 ring retention。
// Bus 回调只做非阻塞入队；不执行 JSON 编码、磁盘 I/O 或网络写入。
type Collector struct {
	cfg        Config
	projector  *Projector
	epoch      string
	instanceID string
	startedAt  time.Time

	ingress      chan ingressItem
	ingressScope chan struct{}
	ingressBytes int64

	unsub runtimeevents.Unsubscribe

	mu        sync.RWMutex
	ring      []ringSlot
	ringHead  int
	ringLen   int
	ringBytes int64
	nextSeq   int64
	oldestSeq int64
	latestSeq int64
	ringDrops uint64

	state   collectorState
	stateMu sync.Mutex

	dedup      map[uint64]struct{}
	dedupOrder []uint64
	dedupLimit int

	startOnce sync.Once
	stopOnce  sync.Once
	closed    chan struct{}
	stopFn    context.CancelFunc
}

type collectorState struct {
	LLM     LLMAggregates
	Runtime RuntimeCounters
}

// NewCollector 创建 collector 并订阅 bus（enabled=false 或 bus 为空时返回 nil，不产生订阅）。
func NewCollector(cfg Config, bus *runtimeevents.Bus, projector *Projector) *Collector {
	if !cfg.Enabled || bus == nil {
		return nil
	}
	if projector == nil {
		projector = NewProjector(NewRedactor(nil, "", cfg.RedactionProfile), cfg.ExposeProviderRequestID, int(cfg.MaxEventBytes))
	}
	epoch := newInstanceEpoch()
	c := &Collector{
		cfg:          cfg,
		projector:    projector,
		epoch:        epoch,
		instanceID:   "runtime-" + shortIdentifier(epoch),
		startedAt:    time.Now().UTC(),
		ingress:      make(chan ingressItem, cfg.IngressQueueEvents),
		ingressScope: make(chan struct{}, cfg.IngressQueueEvents),
		ring:         make([]ringSlot, cfg.RetentionEvents),
		nextSeq:      1,
		oldestSeq:    0,
		latestSeq:    0,
		dedup:        make(map[uint64]struct{}, cfg.RetentionEvents*2),
		dedupLimit:   cfg.RetentionEvents * 4,
		closed:       make(chan struct{}),
		state: collectorState{
			LLM: LLMAggregates{
				ByProvider: make(map[string]*ProviderCounters),
				ByModel:    make(map[string]int64),
			},
		},
	}
	c.unsub = bus.SubscribeCancelable("", c.onBusEvent)
	return c
}

// Start 启动 collector 运行循环。
func (c *Collector) Start() {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		c.stopFn = cancel
		go c.run(ctx)
	})
}

// Stop 停止 collector：取消运行循环并退订 bus，随后关闭。
func (c *Collector) Stop() {
	if c == nil {
		return
	}
	c.stopOnce.Do(func() {
		if c.stopFn != nil {
			c.stopFn()
			<-c.closed
		}
		if c.unsub != nil {
			c.unsub()
			c.unsub = nil
		}
	})
}

func (c *Collector) run(ctx context.Context) {
	defer close(c.closed)
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-c.ingress:
			c.consume(item)
			select {
			case <-c.ingressScope:
			default:
			}
		}
	}
}

// onBusEvent 是 bus 订阅回调；只做非阻塞入队。
func (c *Collector) onBusEvent(event runtimeevents.Event) {
	if c == nil {
		return
	}
	c.enqueue(ingressItem{kind: "runtime", event: event})
}

// enqueue 非阻塞入队；满则丢弃并记 gap。
func (c *Collector) enqueue(item ingressItem) {
	select {
	case c.ingressScope <- struct{}{}:
		select {
		case c.ingress <- item:
			// 已占有一个 scope 令牌，由 consume/release 释放。
		default:
			// 二次竞争失败：令牌不泄漏。
			select {
			case c.ingress <- item:
			default:
				<-c.ingressScope
				c.recordIngressDrop()
			}
		}
	default:
		c.recordIngressDrop()
	}
}

func (c *Collector) recordIngressDrop() {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	now := time.Now().UTC()
	c.state.Runtime.IngressDropped++
	c.state.Runtime.GapCount++
	if c.state.Runtime.LastGapAt == nil {
		c.state.Runtime.LastGapAt = &now
	}
}

// consume 处理一条入队项：投影、去重、入 ring、更新聚合。
func (c *Collector) consume(item ingressItem) {
	var proj Event
	var dedupKey uint64
	var ok bool
	switch item.kind {
	case "runtime":
		proj, ok = c.projector.ProjectRuntimeEvent(item.event)
		if !ok {
			c.stateMu.Lock()
			c.state.Runtime.UnknownDropped++
			c.stateMu.Unlock()
			return
		}
		dedupKey = dedupKeyFor(
			"agent_loop",
			item.event.SessionID,
			payloadString(item.event.Payload["llm_request_id"]),
			proj.Type,
			payloadString(item.event.Payload["attempt_id"]),
		)
	case "debug":
		var k uint64
		proj, k, ok = c.projector.ProjectHTTPDebug(item.debug)
		dedupKey = k
	case "retry":
		proj, ok = c.projector.ProjectRetryEvent(item.retry)
	default:
		return
	}
	if !ok {
		c.stateMu.Lock()
		c.state.Runtime.UnknownDropped++
		c.stateMu.Unlock()
		return
	}
	if proj.Payload != nil {
		size, err := JSONSize(proj.Payload)
		if err != nil {
			c.stateMu.Lock()
			c.state.Runtime.ProjectionErrors++
			c.stateMu.Unlock()
			return
		}
		if int64(size) > int64(c.cfg.MaxEventBytes) {
			proj.Payload = map[string]interface{}{
				"truncated":     true,
				"payload_bytes": size,
			}
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if dedupKey != 0 {
		if _, seen := c.dedup[dedupKey]; seen {
			return
		}
		c.dedup[dedupKey] = struct{}{}
		c.dedupOrder = append(c.dedupOrder, dedupKey)
		if len(c.dedupOrder) > c.dedupLimit {
			evict := c.dedupOrder[0]
			c.dedupOrder = c.dedupOrder[1:]
			delete(c.dedup, evict)
		}
	}
	seq := c.nextSeq
	c.nextSeq++
	c.appendRingLocked(seq, time.Now().UTC(), proj)
	c.updateAggregatesLocked(proj)
}

// ringSlot 保留槽位内存。
// appendRingLocked 环状写入 retention ring，并处理 item+byte 淘汰与 TTL 淘汰。
// 调用方必须持有 c.mu。
func (c *Collector) appendRingLocked(seq int64, ts time.Time, evt Event) {
	evt.ObservationSeq = seq
	payloadBytes, _ := JSONSize(evt.Payload)
	itemBytes := payloadBytes + len(evt.Type) + 64
	slot := ringSlot{seq: seq, ts: ts, event: evt, bytes: itemBytes}
	if c.ringLen < len(c.ring) {
		c.ring[(c.ringHead+c.ringLen)%len(c.ring)] = slot
		c.ringLen++
		if c.ringLen == 1 {
			c.oldestSeq = seq
		}
		c.ringBytes += int64(itemBytes)
	} else {
		// 已满：覆盖最旧槽位。
		c.ringBytes -= int64(c.ring[c.ringHead].bytes)
		c.ring[c.ringHead] = slot
		c.ringHead = (c.ringHead + 1) % len(c.ring)
		c.oldestSeq = c.ring[c.ringHead].seq
		c.ringDrops++
		c.ringBytes += int64(itemBytes)
	}
	c.latestSeq = seq
	c.evictExpiredLocked(ts)
}

func (c *Collector) evictExpiredLocked(now time.Time) {
	ttl := c.cfg.RetentionTTL
	if ttl <= 0 || c.ringLen == 0 {
		return
	}
	for c.ringLen > 0 {
		oldest := c.ring[c.ringHead]
		if now.Sub(oldest.ts) <= ttl {
			break
		}
		c.ringBytes -= int64(oldest.bytes)
		c.ring[c.ringHead] = ringSlot{}
		c.ringHead = (c.ringHead + 1) % len(c.ring)
		c.ringLen--
		c.ringDrops++
	}
	if c.ringLen == 0 {
		c.oldestSeq = 0
	} else {
		c.oldestSeq = c.ring[c.ringHead].seq
	}
}

// updateAggregatesLocked 根据投影事件更新聚合。调用方必须持有 c.mu。
func (c *Collector) updateAggregatesLocked(proj Event) {
	now := time.Now().UTC()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	agg := &c.state
	agg.Runtime.LastEventAt = &now
	payload := proj.Payload
	switch proj.Type {
	case EventLLMRequestStart:
		agg.LLM.RequestsTotal++
		agg.LLM.ActiveRequests++
		recordProviderCounting(agg, payload)
		recordModelCounting(agg, payload)
	case EventLLMRequestDone:
		if agg.LLM.ActiveRequests > 0 {
			agg.LLM.ActiveRequests--
		}
		recordModelCounting(agg, payload)
		trackUsage(agg, payload)
		recordOutcomeCounting(agg, payload)
	case EventLLMRetry:
		agg.LLM.RetriesTotal++
		recordProviderRetryCounting(agg, payload)
	case EventLLMStreamSummary:
		agg.LLM.StreamCount++
		trackUsage(agg, payload)
	case EventAgentTurnStarted:
		agg.Runtime.RunningTurns++
	case EventAgentTurnDone:
		if agg.Runtime.RunningTurns > 0 {
			agg.Runtime.RunningTurns--
		}
	}
}

func recordProviderCounting(agg *collectorState, payload map[string]interface{}) {
	provider := stringField(payload, "provider")
	if provider == "" {
		return
	}
	pc := agg.LLM.ByProvider[provider]
	if pc == nil {
		pc = &ProviderCounters{}
		agg.LLM.ByProvider[provider] = pc
	}
	pc.Requests++
}

func recordOutcomeCounting(agg *collectorState, payload map[string]interface{}) {
	provider := stringField(payload, "provider")
	if provider == "" {
		return
	}
	if !isErrorOutcome(payload) {
		return
	}
	pc := agg.LLM.ByProvider[provider]
	if pc == nil {
		pc = &ProviderCounters{}
		agg.LLM.ByProvider[provider] = pc
	}
	pc.Errors++
}

func recordProviderRetryCounting(agg *collectorState, payload map[string]interface{}) {
	provider := stringField(payload, "provider")
	if provider == "" {
		return
	}
	pc := agg.LLM.ByProvider[provider]
	if pc == nil {
		pc = &ProviderCounters{}
		agg.LLM.ByProvider[provider] = pc
	}
	pc.Retries++
}

func recordModelCounting(agg *collectorState, payload map[string]interface{}) {
	model := stringField(payload, "model")
	if model == "" {
		return
	}
	agg.LLM.ByModel[model]++
}

func isErrorOutcome(payload map[string]interface{}) bool {
	if v, ok := payload["error_code"]; ok {
		if s, ok := v.(string); ok && s != "" && s != "none" {
			return true
		}
	}
	if v, ok := payload["retryable"]; ok {
		if b, ok := v.(bool); ok && b {
			return true
		}
	}
	return false
}

func trackUsage(agg *collectorState, payload map[string]interface{}) {
	usage, ok := payload["usage"].(map[string]interface{})
	if !ok {
		return
	}
	agg.LLM.Usage.PromptTokens += numberField(usage, "prompt_tokens")
	agg.LLM.Usage.CompletionTokens += numberField(usage, "completion_tokens")
	agg.LLM.Usage.TotalTokens += numberField(usage, "total_tokens")
	agg.LLM.Usage.CacheReadTokens += numberField(usage, "cache_read_tokens")
	agg.LLM.Usage.ReasoningTokens += numberField(usage, "reasoning_tokens")
}

func stringField(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

func numberField(m map[string]interface{}, key string) int64 {
	if m == nil {
		return 0
	}
	switch v := m[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

func payloadString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// CursorMeta 是快照需要的 cursor/进程身份信息。
type CursorMeta struct {
	Epoch      string
	InstanceID string
	StartedAt  time.Time
	Bounds     CursorInfo
}

// CursorMetaInfo 返回 collector 的实例级元信息。
func (c *Collector) CursorMetaInfo() CursorMeta {
	if c == nil {
		return CursorMeta{}
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return CursorMeta{
		Epoch:      c.epoch,
		InstanceID: c.instanceID,
		StartedAt:  c.startedAt,
		Bounds: CursorInfo{
			ObservationSeq:     c.latestSeq,
			OldestAvailableSeq: c.oldestSeq,
			InstanceEpoch:      c.epoch,
		},
	}
}

// Stats 组装 runtime/LLM 聚合视图。
func (c *Collector) Stats() (RuntimeSummary, LLMSummary) {
	if c == nil {
		return RuntimeSummary{}, LLMSummary{}
	}
	c.mu.RLock()
	runtimeSummary := RuntimeSummary{
		EventIngressDropped:  c.stateDropped(),
		UnknownEventsDropped: c.stateUnknown(),
		ProjectionErrors:     c.stateProjection(),
		GapCount:             c.stateGap(),
		LastGapAt:            c.stateLastGap(),
		LastEventAt:          c.stateLastEvent(),
		RingCurrentBytes:     c.ringBytes,
		RingOldestSeq:        c.oldestSeq,
		RingLatestSeq:        c.latestSeq,
		RunningTurns:         int(c.stateRunningTurns()),
	}
	c.mu.RUnlock()
	return runtimeSummary, c.snapshotLLM()
}

func (c *Collector) snapshotLLM() LLMSummary {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	agg := &c.state.LLM
	llm := LLMSummary{
		RequestsTotal:    agg.RequestsTotal,
		RequestsInFlight: agg.ActiveRequests,
		RetriesTotal:     agg.RetriesTotal,
		StreamCount:      agg.StreamCount,
		Usage:            agg.Usage,
		ByProvider:       make(map[string]ProviderSummary, len(agg.ByProvider)),
		ByModel:          make(map[string]int64, len(agg.ByModel)),
	}
	for provider, pc := range agg.ByProvider {
		llm.ByProvider[provider] = ProviderSummary{
			Requests: pc.Requests,
			Errors:   pc.Errors,
			Retries:  pc.Retries,
		}
	}
	for model, count := range agg.ByModel {
		llm.ByModel[model] = count
	}
	return llm
}

func (c *Collector) stateDropped() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.IngressDropped
}

func (c *Collector) stateUnknown() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.UnknownDropped
}

func (c *Collector) stateProjection() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.ProjectionErrors
}

func (c *Collector) stateGap() uint64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.GapCount
}

func (c *Collector) stateLastGap() *time.Time {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.LastGapAt
}

func (c *Collector) stateLastEvent() *time.Time {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.LastEventAt
}

func (c *Collector) stateRunningTurns() int64 {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	return c.state.Runtime.RunningTurns
}

// Query 从 retention ring 查询低敏事件。
func (c *Collector) Query(q EventQuery) (QueryResult, error) {
	if c == nil {
		return QueryResult{}, fmt.Errorf("%s: observation disabled", ErrCodeDisabled)
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.ringLen == 0 {
		return QueryResult{
			Events:             []Event{},
			AfterSeq:           c.latestSeq,
			LatestSeq:          c.latestSeq,
			OldestAvailableSeq: 0,
			Partial:            false,
			Count:              0,
		}, nil
	}
	startIndex := c.ringHead
	result := QueryResult{
		Events:             make([]Event, 0, q.Limit),
		LatestSeq:          c.latestSeq,
		OldestAvailableSeq: c.oldestSeq,
	}
	if len(q.SessionID) == 0 && len(q.TraceID) == 0 && len(q.AgentID) == 0 &&
		len(q.TurnID) == 0 && len(q.Provider) == 0 && len(q.Model) == 0 &&
		len(q.EventType) == 0 && len(q.Source) == 0 && q.AfterSeq <= 0 && q.BeforeSeq <= 0 &&
		q.Since == nil && q.Until == nil {
		// 无过滤：直接从 after_seq（或 oldest）往前扫 limit 条。
		var after int64
		if q.AfterSeq > 0 {
			after = q.AfterSeq
		} else {
			after = c.oldestSeq - 1
		}
		count := 0
		for i := 0; i < c.ringLen && count < q.Limit; i++ {
			slot := c.ring[(startIndex+i)%len(c.ring)]
			if slot.seq <= after {
				continue
			}
			result.Events = append(result.Events, slot.event)
			count++
		}
		result.AfterSeq = after
		result.Count = count
		if count < q.Limit {
			result.NextCursor = nil
		} else {
			last := result.Events[len(result.Events)-1]
			cursor := Cursor{SchemaVersion: SchemaVersionCursor, InstanceEpoch: c.epoch, Seq: last.ObservationSeq}
			enc := cursor.Encode()
			result.NextCursor = &enc
		}
		return result, nil
	}

	// 带过滤：从后向前收集 limit 条（最近优先）。
	scanned := 0
	for i := 0; i < c.ringLen && scanned < q.Limit; i++ {
		slot := c.ring[(startIndex+c.ringLen-1-i+len(c.ring))%len(c.ring)]
		if q.AfterSeq > 0 && slot.seq <= q.AfterSeq {
			continue
		}
		if q.BeforeSeq > 0 && slot.seq >= q.BeforeSeq {
			continue
		}
		if q.Since != nil && slot.ts.Before(*q.Since) {
			continue
		}
		if q.Until != nil && slot.ts.After(*q.Until) {
			continue
		}
		if !matchCorrelation(slot.event, q) {
			continue
		}
		result.Events = append([]Event{slot.event}, result.Events...)
		scanned++
	}
	result.AfterSeq = q.AfterSeq
	result.Count = len(result.Events)
	result.Partial = len(result.Events) > 0
	if result.Count >= q.Limit {
		last := result.Events[len(result.Events)-1]
		cursor := Cursor{SchemaVersion: SchemaVersionCursor, InstanceEpoch: c.epoch, Seq: last.ObservationSeq}
		enc := cursor.Encode()
		result.NextCursor = &enc
	}
	return result, nil
}

func matchCorrelation(evt Event, q EventQuery) bool {
	if q.SessionID != "" && evt.Correlation.SessionID != q.SessionID {
		return false
	}
	if q.TraceID != "" && evt.Correlation.TraceID != q.TraceID {
		return false
	}
	if q.AgentID != "" && evt.Correlation.AgentID != q.AgentID {
		return false
	}
	if q.TurnID != "" && evt.Correlation.TurnID != q.TurnID {
		return false
	}
	if q.EventType != "" && evt.Type != q.EventType {
		return false
	}
	if q.Source != "" && evt.Source != q.Source {
		return false
	}
	if q.Provider != "" || q.Model != "" {
		provider := stringField(evt.Payload, "provider")
		model := stringField(evt.Payload, "model")
		if q.Provider != "" && provider != q.Provider {
			return false
		}
		if q.Model != "" && model != q.Model {
			return false
		}
	}
	return true
}

func newInstanceEpoch() string {
	return fmt.Sprintf("%d-%s", time.Now().UTC().UnixMilli(), shortIdentifier(time.Now().UTC().String()))
}

func shortIdentifier(seed string) string {
	buf := make([]byte, 6)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(seed))[:8]
	}
	return hex.EncodeToString(buf)
}

// RingBounds 返回当前 ring 内的 seq 边界（oldest, latest）；empty 时为 0,0。
func (c *Collector) RingBounds() (int64, int64) {
	if c == nil {
		return 0, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.oldestSeq, c.latestSeq
}

// Epoch 返回实例 epoch（进程重启后不复用旧序列）。
func (c *Collector) Epoch() string {
	if c == nil {
		return ""
	}
	return c.epoch
}

// InstanceID 返回观测实例 ID。
func (c *Collector) InstanceID() string {
	if c == nil {
		return ""
	}
	return c.instanceID
}

// StartedAt 返回 collector 启动时间（用于 uptime）。
func (c *Collector) StartedAt() time.Time {
	if c == nil {
		return time.Now().UTC()
	}
	return c.startedAt
}

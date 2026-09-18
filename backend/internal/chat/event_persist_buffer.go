package chat

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	logpkg "github.com/wwsheng009/ai-agent-runtime/internal/pkg/logger"
)

// P1.5-B：桥接层有界缓冲（docs/plan/runtime-store-event-persistence-batching-and-async-plan-20260918.md §3.2）。
//
// 双宿主（aicli / runtime-server）共用的单一实现：
//   - Enqueue 非阻塞入队（block 溢出模式除外），生产者不等磁盘（P2.11 的零阻塞前提）；
//   - 单 worker 串行 flush：BatchSize / FlushInterval / CriticalTypes 三种触发；
//   - 失败保留队首批并退避重试（≤MaxRetries），绝不静默丢弃；
//   - 双上限（条数 + 字节）；溢出默认 block（同步落盘、背压还给生产者），
//     drop 仅在显式配置时启用并计数；
//   - Close 有界 flush，超时把剩余条数计入 shutdown_timeout。
type EventPersistBatchStore interface {
	AppendEvent(ctx context.Context, event runtimeevents.Event) (int64, error)
	AppendEvents(ctx context.Context, events []runtimeevents.Event) ([]int64, error)
}

// EventPersistFailMode 是队列溢出的处理策略。
type EventPersistFailMode string

const (
	// EventPersistFailBlock 溢出时同步落盘该事件（默认；不丢，产生背压）。
	EventPersistFailBlock EventPersistFailMode = "block"
	// EventPersistFailDrop 溢出时计数并丢弃（仅显式配置；用于可重建的 delta 类事件）。
	EventPersistFailDrop EventPersistFailMode = "drop"
)

const (
	defaultEventPersistBatchSize       = 64
	defaultEventPersistFlushInterval   = 25 * time.Millisecond
	defaultEventPersistQueueLimit      = 4096
	defaultEventPersistQueueBytesLimit = int64(16 << 20)
	defaultEventPersistFlushTimeout    = 10 * time.Second
	defaultEventPersistShutdownTimeout = 2 * time.Second
	defaultEventPersistMaxRetries      = 3
	// defaultEventPersistDispatchWait 是 AsyncDispatch 下队列满时等待 worker
	// 腾出空间的时长上限；超时才走计入计数的同步兜底写。
	defaultEventPersistDispatchWait = 250 * time.Millisecond
	eventPersistWarnInterval        = 5 * time.Second
	eventPersistDegradeThreshold    = 3
)

var eventPersistRetryBackoff = []time.Duration{50 * time.Millisecond, 200 * time.Millisecond, 500 * time.Millisecond}

// EventPersistBufferConfig 配置缓冲行为；零值字段取默认值。
type EventPersistBufferConfig struct {
	BatchSize       int
	FlushInterval   time.Duration
	QueueLimit      int
	QueueBytesLimit int64
	FlushTimeout    time.Duration
	ShutdownTimeout time.Duration
	FailMode        EventPersistFailMode
	// DispatchWaitTimeout 是 AsyncDispatch 下队列满时等待 worker 腾挪的时长上限（默认 250ms）。
	DispatchWaitTimeout time.Duration
	// CriticalTypes 命中时立即触发 flush（默认 nil=无关键类型）。
	CriticalTypes func(eventType string) bool
	// AsyncDispatch 开启 P2.11 语义：发布链绝不直接做磁盘 I/O；队列满时先等待
	// worker 腾挪（DispatchWaitTimeout），超时才同步兜底（计数可见）。
	AsyncDispatch bool
	MaxRetries    int
}

// EventPersistSettings 是宿主配置（internal/config）到缓冲配置的中性映射，
// 双宿主共用，避免两处字段映射漂移。
type EventPersistSettings struct {
	Enabled         bool
	BatchSize       int
	FlushInterval   time.Duration
	QueueLimit      int
	QueueBytesLimit int64
	ShutdownTimeout time.Duration
	FailMode        string
	AsyncDispatch   bool
	CriticalTypes   func(eventType string) bool
}

// BufferConfig 把宿主设置转换为缓冲配置（零值/未知 FailMode 由归一化兜底为 block）。
func (s EventPersistSettings) BufferConfig() EventPersistBufferConfig {
	return EventPersistBufferConfig{
		BatchSize:       s.BatchSize,
		FlushInterval:   s.FlushInterval,
		QueueLimit:      s.QueueLimit,
		QueueBytesLimit: s.QueueBytesLimit,
		ShutdownTimeout: s.ShutdownTimeout,
		FailMode:        EventPersistFailMode(s.FailMode),
		AsyncDispatch:   s.AsyncDispatch,
		CriticalTypes:   s.CriticalTypes,
	}
}

// ShutdownTimeoutOrDefault 返回配置的关闭 flush 上限（未配置时用默认值）。
func (s EventPersistSettings) ShutdownTimeoutOrDefault() time.Duration {
	if s.ShutdownTimeout > 0 {
		return s.ShutdownTimeout
	}
	return defaultEventPersistShutdownTimeout
}

// EventPersistBufferStats 是缓冲的统计快照（§5 指标）。
type EventPersistBufferStats struct {
	Enqueued          int64 `json:"enqueued"`
	FlushedEvents     int64 `json:"flushed_events"`
	Batches           int64 `json:"batches"`
	SkippedPersisted  int64 `json:"skipped_persisted"`
	SyncFallback      int64 `json:"sync_fallback"`
	Dropped           int64 `json:"dropped"`
	Retried           int64 `json:"retry"`
	Failed            int64 `json:"failed"`
	ShutdownRemaining int64 `json:"shutdown_timeout"`
	QueueDepth        int64 `json:"queue_depth"`
	QueueBytes        int64 `json:"queue_bytes"`
	FlushLatencyTotal int64 `json:"flush_latency_total_ns"`
	FlushLatencyMax   int64 `json:"flush_latency_max_ns"`
	FlushP95Ns        int64 `json:"flush_p95_ns"`
	// P2.11：发布链派发延迟（Enqueue 全路径，含 async 等待）与等待/超时计数。
	DispatchP95Ns        int64  `json:"dispatch_p95_ns"`
	DispatchMaxNs        int64  `json:"dispatch_max_ns"`
	DispatchWaits        int64  `json:"dispatch_waits"`
	DispatchWaitTimeouts int64  `json:"dispatch_wait_timeouts"`
	Degraded             bool   `json:"degraded"`
	AsyncDispatch        bool   `json:"async_dispatch"`
	BatchSize            int    `json:"batch_size"`
	FlushIntervalMilli   int64  `json:"flush_interval_ms"`
	QueueLimit           int    `json:"queue_limit"`
	QueueBytesLimit      int64  `json:"queue_bytes_limit"`
	FailMode             string `json:"fail_mode"`
}

// EventPersistBuffer 是有界的事件持久化缓冲。
type EventPersistBuffer struct {
	store EventPersistBatchStore
	cfg   EventPersistBufferConfig

	mu           sync.Mutex
	queue        []runtimeevents.Event
	queueBytes   int64
	pending      []runtimeevents.Event
	pendingBytes int64
	closed       bool

	wakeCh   chan struct{}
	spaceCh  chan struct{}
	stopCh   chan struct{}
	doneCh   chan struct{}
	stopOnce sync.Once

	degraded    atomic.Bool
	consecutive atomic.Int64
	lastWarnAt  atomic.Int64
	counters    eventPersistCounters

	// 延迟滑动窗口（最近 eventPersistLatencyRingSize 个样本）用于 P95。
	flushLatency    eventPersistLatencyWindow
	dispatchLatency eventPersistLatencyWindow
}

const eventPersistLatencyRingSize = 512

// eventPersistLatencyWindow 是固定容量环形延迟窗口；写入方（worker 或发布链）
// 加锁追加，读取方（Stats）加锁复制后排序取 P95。
type eventPersistLatencyWindow struct {
	mu     sync.Mutex
	ring   [eventPersistLatencyRingSize]int64
	cursor int
	filled bool
}

func (w *eventPersistLatencyWindow) record(ns int64) {
	w.mu.Lock()
	w.ring[w.cursor] = ns
	w.cursor++
	if w.cursor >= len(w.ring) {
		w.cursor = 0
		w.filled = true
	}
	w.mu.Unlock()
}

func (w *eventPersistLatencyWindow) p95() int64 {
	w.mu.Lock()
	size := w.cursor
	if w.filled {
		size = len(w.ring)
	}
	if size == 0 {
		w.mu.Unlock()
		return 0
	}
	samples := make([]int64, size)
	copy(samples, w.ring[:size])
	w.mu.Unlock()
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	index := (95*size + 99) / 100
	if index > 0 {
		index--
	}
	return samples[index]
}

type eventPersistCounters struct {
	enqueued           atomic.Int64
	flushedEvents      atomic.Int64
	batches            atomic.Int64
	skippedPersisted   atomic.Int64
	syncFallback       atomic.Int64
	dropped            atomic.Int64
	retried            atomic.Int64
	failed             atomic.Int64
	shutdownRemaining  atomic.Int64
	flushLatencyTotal  atomic.Int64
	flushLatencyMax    atomic.Int64
	dispatchWaits      atomic.Int64
	dispatchTimeouts   atomic.Int64
	dispatchLatencyMax atomic.Int64
}

// NewEventPersistBuffer 创建并启动缓冲 worker；store 为 nil 时返回 nil（调用方回退同步路径）。
func NewEventPersistBuffer(store EventPersistBatchStore, cfg EventPersistBufferConfig) *EventPersistBuffer {
	if store == nil {
		return nil
	}
	cfg = normalizeEventPersistBufferConfig(cfg)
	buffer := &EventPersistBuffer{
		store:   store,
		cfg:     cfg,
		wakeCh:  make(chan struct{}, 1),
		spaceCh: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	go buffer.loop()
	return buffer
}

func normalizeEventPersistBufferConfig(cfg EventPersistBufferConfig) EventPersistBufferConfig {
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = defaultEventPersistBatchSize
	}
	if cfg.BatchSize > maxAppendEventsBatch {
		cfg.BatchSize = maxAppendEventsBatch
	}
	if cfg.FlushInterval <= 0 {
		cfg.FlushInterval = defaultEventPersistFlushInterval
	}
	if cfg.QueueLimit <= 0 {
		cfg.QueueLimit = defaultEventPersistQueueLimit
	}
	if cfg.QueueBytesLimit <= 0 {
		cfg.QueueBytesLimit = defaultEventPersistQueueBytesLimit
	}
	if cfg.FlushTimeout <= 0 {
		cfg.FlushTimeout = defaultEventPersistFlushTimeout
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = defaultEventPersistShutdownTimeout
	}
	if cfg.DispatchWaitTimeout <= 0 {
		cfg.DispatchWaitTimeout = defaultEventPersistDispatchWait
	}
	if cfg.FailMode != EventPersistFailDrop {
		cfg.FailMode = EventPersistFailBlock
	}
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = defaultEventPersistMaxRetries
	}
	return cfg
}

// Enqueue 入队。默认（AsyncDispatch=false）路径只做队列写入+信号；队列满时按
// FailMode 处理（block=同步落盘、drop=计数丢弃）。开启 AsyncDispatch 后，队列满
// 时先等待 worker 腾挪（有界），超时才同步兜底——发布链自身绝不主动写盘。
// 已携带 payload["seq"] 的事件视为已持久化，直接跳过（§0.3 去重契约）。
func (b *EventPersistBuffer) Enqueue(event runtimeevents.Event) {
	if b == nil || b.store == nil {
		return
	}
	if event.Payload != nil {
		if _, persisted := event.Payload["seq"]; persisted {
			b.counters.skippedPersisted.Add(1)
			return
		}
	}

	start := time.Now()
	eventBytes := runtimeevents.ApproximateEventBytes(event)
	critical := b.cfg.CriticalTypes != nil && b.cfg.CriticalTypes(event.Type)
	outcome := b.tryEnqueue(event, eventBytes, critical)
	switch outcome {
	case eventPersistEnqueueOverflow:
		if b.cfg.FailMode == EventPersistFailDrop {
			b.counters.dropped.Add(1)
			b.warnRateLimited("event persist queue overflow, event dropped (type=%s session=%s)", event.Type, event.SessionID)
			break
		}
		if b.cfg.AsyncDispatch && b.waitForQueueSpace() {
			b.counters.dispatchWaits.Add(1)
			// 等到了空位：重试一次入队；仍失败则同步兜底（计数可见，不丢）。
			if retry := b.tryEnqueue(event, eventBytes, critical); retry == eventPersistEnqueueQueued {
				break
			}
		} else if b.cfg.AsyncDispatch {
			b.counters.dispatchTimeouts.Add(1)
		}
		b.syncFallbackWrite(event, "queue_full")
	case eventPersistEnqueueDegraded:
		b.syncFallbackWrite(event, "degraded")
	case eventPersistEnqueueClosed:
		b.counters.dropped.Add(1)
		b.warnRateLimited("event persist buffer is closed, event dropped (type=%s session=%s)", event.Type, event.SessionID)
	}
	if b.cfg.AsyncDispatch {
		elapsed := time.Since(start).Nanoseconds()
		b.dispatchLatency.record(elapsed)
		updateAtomicMax(&b.counters.dispatchLatencyMax, elapsed)
	}
}

type eventPersistEnqueueOutcome int

const (
	eventPersistEnqueueQueued eventPersistEnqueueOutcome = iota
	eventPersistEnqueueOverflow
	eventPersistEnqueueDegraded
	eventPersistEnqueueClosed
)

// tryEnqueue 是入队快路径：只做队列/字节计数与触发信号，不触碰 store。
func (b *EventPersistBuffer) tryEnqueue(event runtimeevents.Event, eventBytes int64, critical bool) eventPersistEnqueueOutcome {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return eventPersistEnqueueClosed
	}
	queueDepth := len(b.queue) + len(b.pending)
	queueBytes := b.queueBytes + b.pendingBytes
	if b.degraded.Load() {
		b.mu.Unlock()
		return eventPersistEnqueueDegraded
	}
	if queueDepth >= b.cfg.QueueLimit || queueBytes+eventBytes > b.cfg.QueueBytesLimit {
		b.mu.Unlock()
		return eventPersistEnqueueOverflow
	}
	b.queue = append(b.queue, event)
	b.queueBytes += eventBytes
	shouldSignal := critical || len(b.queue) >= b.cfg.BatchSize
	b.mu.Unlock()

	b.counters.enqueued.Add(1)
	if shouldSignal {
		b.signal()
	}
	return eventPersistEnqueueQueued
}

// waitForQueueSpace 等待 worker 取走一批（takeBatch 释放空间时唤醒）；
// 返回 true 表示已有空间可重试入队。缓冲关闭/进程停止时立即返回 false。
func (b *EventPersistBuffer) waitForQueueSpace() bool {
	timer := time.NewTimer(b.cfg.DispatchWaitTimeout)
	defer timer.Stop()
	select {
	case <-b.spaceCh:
		return true
	case <-timer.C:
		return false
	case <-b.stopCh:
		return false
	}
}

func (b *EventPersistBuffer) signalSpace() {
	select {
	case b.spaceCh <- struct{}{}:
	default:
	}
}

func (b *EventPersistBuffer) syncFallbackWrite(event runtimeevents.Event, reason string) {
	b.counters.syncFallback.Add(1)
	b.warnRateLimited("event persist sync fallback (%s, type=%s session=%s)", reason, event.Type, event.SessionID)
	ctx, cancel := context.WithTimeout(context.Background(), b.cfg.FlushTimeout)
	defer cancel()
	if _, err := b.store.AppendEvent(ctx, event); err != nil {
		b.counters.failed.Add(1)
		b.warnRateLimited("event persist sync fallback failed (type=%s session=%s): %v", event.Type, event.SessionID, err)
	}
}

func (b *EventPersistBuffer) signal() {
	select {
	case b.wakeCh <- struct{}{}:
	default:
	}
}

func (b *EventPersistBuffer) warnRateLimited(format string, args ...interface{}) {
	now := time.Now().UnixNano()
	if last := b.lastWarnAt.Load(); now-last < int64(eventPersistWarnInterval) {
		return
	} else if !b.lastWarnAt.CompareAndSwap(last, now) {
		return
	}
	logpkg.Warnf("[event-persist] "+format, args...)
}

func (b *EventPersistBuffer) loop() {
	defer close(b.doneCh)
	ticker := time.NewTicker(b.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-b.stopCh:
			b.drainOnClose()
			return
		case <-b.wakeCh:
		case <-ticker.C:
		}
		for b.flushOnce(context.Background()) {
		}
	}
}

// flushOnce 是 worker 的单次推进：成功落一批且「又攒满一整批」时返回 true 续刷。
func (b *EventPersistBuffer) flushOnce(parent context.Context) bool {
	if !b.flushBatch(parent) {
		return false
	}
	return b.hasFullBatch()
}

// flushBatch 取一批（优先重试中的 pending）并落盘；返回是否成功推进了一批。
func (b *EventPersistBuffer) flushBatch(parent context.Context) bool {
	batch, ok := b.takeBatch()
	if !ok {
		return false
	}
	start := time.Now()
	err := b.persistBatch(parent, batch)
	elapsed := time.Since(start).Nanoseconds()
	b.counters.flushLatencyTotal.Add(elapsed)
	b.flushLatency.record(elapsed)
	updateAtomicMax(&b.counters.flushLatencyMax, elapsed)
	if err != nil {
		// 失败批放回队首：保序、不丢，等下一轮触发重试。
		b.mu.Lock()
		b.pending = batch
		b.pendingBytes = eventBatchBytes(batch)
		b.mu.Unlock()
		b.counters.failed.Add(1)
		b.consecutive.Add(1)
		if b.consecutive.Load() >= eventPersistDegradeThreshold && !b.degraded.Swap(true) {
			b.warnRateLimited("event persist degraded to synchronous writes after %d failed batches: %v", b.consecutive.Load(), err)
		}
		return false
	}
	b.counters.batches.Add(1)
	b.counters.flushedEvents.Add(int64(len(batch)))
	b.consecutive.Store(0)
	b.degraded.Store(false)
	return true
}

// updateAtomicMax 以 CAS 循环把目标计数推进到至少 value。
func updateAtomicMax(target *atomic.Int64, value int64) {
	for {
		maxObserved := target.Load()
		if value <= maxObserved || target.CompareAndSwap(maxObserved, value) {
			return
		}
	}
}

func (b *EventPersistBuffer) takeBatch() ([]runtimeevents.Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.pending) > 0 {
		batch := b.pending
		b.pending = nil
		b.pendingBytes = 0
		return batch, true
	}
	if len(b.queue) == 0 {
		return nil, false
	}
	size := b.cfg.BatchSize
	if size > len(b.queue) {
		size = len(b.queue)
	}
	batch := make([]runtimeevents.Event, size)
	copy(batch, b.queue[:size])
	remaining := len(b.queue) - size
	copy(b.queue, b.queue[size:])
	b.queue = b.queue[:remaining]
	b.queueBytes -= eventBatchBytes(batch)
	if b.queueBytes < 0 {
		b.queueBytes = 0
	}
	// 队列腾出空间：唤醒 AsyncDispatch 下等待中的发布链。
	b.signalSpace()
	return batch, true
}

func (b *EventPersistBuffer) hasWork() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending) > 0 || len(b.queue) > 0
}

func (b *EventPersistBuffer) hasFullBatch() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.pending)+len(b.queue) >= b.cfg.BatchSize
}

// persistBatch 调用 store 批量写；失败按 50ms→200ms→500ms 退避重试（≤MaxRetries）。
func (b *EventPersistBuffer) persistBatch(parent context.Context, batch []runtimeevents.Event) error {
	attempts := b.cfg.MaxRetries
	var lastErr error
	for attempt := 0; attempt <= attempts; attempt++ {
		if attempt > 0 {
			backoff := eventPersistRetryBackoff[len(eventPersistRetryBackoff)-1]
			if attempt-1 < len(eventPersistRetryBackoff) {
				backoff = eventPersistRetryBackoff[attempt-1]
			}
			b.counters.retried.Add(1)
			select {
			case <-time.After(backoff):
			case <-b.stopCh:
			}
		}
		ctx, cancel := context.WithTimeout(parent, b.cfg.FlushTimeout)
		_, err := b.store.AppendEvents(ctx, batch)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("append events batch (%d events): %w", len(batch), lastErr)
}

// drainOnClose 在 ShutdownTimeout 内尽力 flush 剩余队列。
func (b *EventPersistBuffer) drainOnClose() {
	deadline := time.Now().Add(b.cfg.ShutdownTimeout)
	for b.hasWork() && time.Now().Before(deadline) {
		if !b.flushBatch(context.Background()) {
			break
		}
	}
	if remaining := b.remainingCount(); remaining > 0 {
		b.counters.shutdownRemaining.Add(remaining)
		b.warnRateLimited("event persist shutdown flush incomplete: %d event(s) not persisted", remaining)
	}
}

// Close 停止入队并在 ShutdownTimeout 内 flush；ctx 更短时按 ctx 截止。
func (b *EventPersistBuffer) Close(ctx context.Context) error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	b.closed = true
	b.mu.Unlock()
	b.stopOnce.Do(func() { close(b.stopCh) })

	timeout := b.cfg.ShutdownTimeout
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining < timeout {
				timeout = remaining
			}
		}
	}
	if timeout < 0 {
		timeout = 0
	}
	select {
	case <-b.doneCh:
		return nil
	case <-time.After(timeout):
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("event persist buffer shutdown timed out after %s with %d event(s) pending", timeout, b.remainingCount())
	}
}

func (b *EventPersistBuffer) remainingCount() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.pending) + len(b.queue))
}

// Stats 返回统计快照。
func (b *EventPersistBuffer) Stats() EventPersistBufferStats {
	if b == nil {
		return EventPersistBufferStats{}
	}
	b.mu.Lock()
	depth := int64(len(b.queue) + len(b.pending))
	queueBytes := b.queueBytes + b.pendingBytes
	b.mu.Unlock()
	return EventPersistBufferStats{
		Enqueued:             b.counters.enqueued.Load(),
		FlushedEvents:        b.counters.flushedEvents.Load(),
		Batches:              b.counters.batches.Load(),
		SkippedPersisted:     b.counters.skippedPersisted.Load(),
		SyncFallback:         b.counters.syncFallback.Load(),
		Dropped:              b.counters.dropped.Load(),
		Retried:              b.counters.retried.Load(),
		Failed:               b.counters.failed.Load(),
		ShutdownRemaining:    b.counters.shutdownRemaining.Load(),
		QueueDepth:           depth,
		QueueBytes:           queueBytes,
		FlushLatencyTotal:    b.counters.flushLatencyTotal.Load(),
		FlushLatencyMax:      b.counters.flushLatencyMax.Load(),
		FlushP95Ns:           b.flushLatency.p95(),
		DispatchP95Ns:        b.dispatchLatency.p95(),
		DispatchMaxNs:        b.counters.dispatchLatencyMax.Load(),
		DispatchWaits:        b.counters.dispatchWaits.Load(),
		DispatchWaitTimeouts: b.counters.dispatchTimeouts.Load(),
		Degraded:             b.degraded.Load(),
		AsyncDispatch:        b.cfg.AsyncDispatch,
		BatchSize:            b.cfg.BatchSize,
		FlushIntervalMilli:   b.cfg.FlushInterval.Milliseconds(),
		QueueLimit:           b.cfg.QueueLimit,
		QueueBytesLimit:      b.cfg.QueueBytesLimit,
		FailMode:             string(b.cfg.FailMode),
	}
}

func eventBatchBytes(events []runtimeevents.Event) int64 {
	var total int64
	for index := range events {
		total += runtimeevents.ApproximateEventBytes(events[index])
	}
	return total
}

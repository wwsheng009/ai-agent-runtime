package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// DropPolicy 决定异步订阅队列满时的行为。
//
// 默认 DropOldest：流式 delta 这类"可重建"事件宁可丢旧保新；
// 告警类事件用 DropNewest（保旧）；命令/终态类事件用 DropBlock
// （带超时阻塞发布者，宁可慢一点也不丢）。
type DropPolicy int

const (
	DropOldest DropPolicy = iota
	DropNewest
	DropBlock
)

func (p DropPolicy) String() string {
	switch p {
	case DropNewest:
		return "drop_newest"
	case DropBlock:
		return "drop_block"
	default:
		return "drop_oldest"
	}
}

const (
	defaultAsyncQueueSize    = 256
	defaultAsyncBlockTimeout = 250 * time.Millisecond
)

// AsyncOptions 配置异步订阅。
type AsyncOptions struct {
	// Name 用于日志/指标标识（可选）。
	Name string
	// QueueSize 有界队列长度，<=0 时使用默认 256。
	QueueSize int
	// DropPolicy 队列满策略，默认 DropOldest。
	DropPolicy DropPolicy
	// BlockTimeout 仅 DropBlock 生效，<=0 时使用默认 250ms。
	BlockTimeout time.Duration
	// OnDrop 在每次丢弃时回调（宿主可上报指标）；reason 为 newest/oldest/block_timeout。
	OnDrop func(reason string, event Event)
	// OnPanic 在处理器 panic 时回调（异步订阅会隔离 panic，不影响发布者与其它订阅）。
	OnPanic func(event Event, recovered any)
}

// AsyncSubscription 是一条异步订阅的运行态句柄。
//
// 设计要点（docs/bots/05-review-and-hardening.md §2.3）：
//   - 处理器在独立 goroutine 中串行执行，事件按入队顺序处理；
//   - 队列有界；满时按 DropPolicy 处置，绝不在发布路径上阻塞（DropBlock 带超时）；
//   - 处理器 panic 被 recover 并计数，不传播到发布者（Bus.Publish 是同步调用栈）；
//   - 计数器可直接接指标系统。
type AsyncSubscription struct {
	name        string
	eventType   string
	handler     Handler
	ch          chan Event
	quit        chan struct{}
	done        chan struct{}
	policy      DropPolicy
	blockWait   time.Duration
	onDrop      func(string, Event)
	onPanic     func(Event, any)
	closed      atomic.Bool
	closeOnce   sync.Once
	unsubscribe Unsubscribe

	delivered atomic.Int64
	dropped   atomic.Int64
	panicked  atomic.Int64
}

// AsyncStats 是异步订阅的快照计数。
type AsyncStats struct {
	Name      string `json:"name,omitempty"`
	EventType string `json:"event_type,omitempty"`
	Queued    int    `json:"queued"`
	Capacity  int    `json:"capacity"`
	Delivered int64  `json:"delivered"`
	Dropped   int64  `json:"dropped"`
	Panicked  int64  `json:"panicked"`
	Closed    bool   `json:"closed"`
}

// SubscribeAsync 注册一条异步订阅：发布者只做入队，处理在独立 goroutine 中进行。
//
// 这是连接器、通知中台等"出口型"消费者的标准订阅方式；纯内存投影等
// 轻量消费者仍可用同步的 Subscribe（语义与性能不变）。
func (b *Bus) SubscribeAsync(eventType string, handler Handler, opts AsyncOptions) *AsyncSubscription {
	if b == nil || handler == nil {
		return nil
	}
	size := opts.QueueSize
	if size <= 0 {
		size = defaultAsyncQueueSize
	}
	sub := &AsyncSubscription{
		name:      opts.Name,
		eventType: eventType,
		handler:   handler,
		ch:        make(chan Event, size),
		quit:      make(chan struct{}),
		done:      make(chan struct{}),
		policy:    opts.DropPolicy,
		blockWait: opts.BlockTimeout,
		onDrop:    opts.OnDrop,
		onPanic:   opts.OnPanic,
	}
	_, sub.unsubscribe = b.subscribeDispatch(eventType, sub.enqueue)
	go sub.run()
	return sub
}

// enqueue 由发布者 goroutine 调用：只做非阻塞入队（或 DropBlock 下的受限等待）。
func (s *AsyncSubscription) enqueue(event Event) {
	if s == nil || s.closed.Load() {
		return
	}
	select {
	case s.ch <- event:
		return
	default:
	}

	switch s.policy {
	case DropNewest:
		s.reportDrop("newest", event)
	case DropBlock:
		wait := s.blockWait
		if wait <= 0 {
			wait = defaultAsyncBlockTimeout
		}
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case s.ch <- event:
		case <-timer.C:
			s.reportDrop("block_timeout", event)
		case <-s.quit:
			s.reportDrop("closed", event)
		}
	default: // DropOldest：丢掉最旧的一条再入队
		select {
		case old := <-s.ch:
			s.dropped.Add(1)
			s.reportDropQuiet("oldest", old)
		default:
		}
		select {
		case s.ch <- event:
		default:
			s.reportDrop("oldest", event)
		}
	}
}

func (s *AsyncSubscription) reportDrop(reason string, event Event) {
	s.dropped.Add(1)
	s.reportDropQuiet(reason, event)
}

func (s *AsyncSubscription) reportDropQuiet(reason string, event Event) {
	if s.onDrop != nil {
		s.onDrop(reason, event)
	}
}

// run 串行消费队列；Unsubscribe 后先排空已入队事件再退出。
func (s *AsyncSubscription) run() {
	defer close(s.done)
	for {
		select {
		case event := <-s.ch:
			s.handle(event)
		case <-s.quit:
			for {
				select {
				case event := <-s.ch:
					s.handle(event)
				default:
					return
				}
			}
		}
	}
}

// handle 隔离处理器 panic：只废该条事件并计数，不传播到发布者。
func (s *AsyncSubscription) handle(event Event) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.panicked.Add(1)
			if s.onPanic != nil {
				s.onPanic(event, recovered)
			}
		}
	}()
	s.handler(event)
	s.delivered.Add(1)
}

// Unsubscribe 停止订阅：先从总线摘除（后续发布不再入队），再让消费协程排空退出。
func (s *AsyncSubscription) Unsubscribe() {
	if s == nil {
		return
	}
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		if s.unsubscribe != nil {
			s.unsubscribe()
		}
		close(s.quit)
	})
}

// Stats 返回计数快照（可直接映射到 connectors_*/notify_* 指标）。
func (s *AsyncSubscription) Stats() AsyncStats {
	if s == nil {
		return AsyncStats{}
	}
	return AsyncStats{
		Name:      s.name,
		EventType: s.eventType,
		Queued:    len(s.ch),
		Capacity:  cap(s.ch),
		Delivered: s.delivered.Load(),
		Dropped:   s.dropped.Load(),
		Panicked:  s.panicked.Load(),
		Closed:    s.closed.Load(),
	}
}

// WaitDrained 等待队列排空（测试与优雅停机用）；ctx 结束时返回 ctx.Err()。
func (s *AsyncSubscription) WaitDrained(ctx context.Context) error {
	if s == nil {
		return nil
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if len(s.ch) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

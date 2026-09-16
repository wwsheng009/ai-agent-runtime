package skills

import (
	"strings"
	"sync"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// Batch 3 · 传输增强（方案 §4）：B 通道（live-only 旁路）由「满了就丢」改为
// latest-wins。
//
// 背景：`tool.progress` / `subagent.progress` 是高频进度帧，消费者（SSE 连接）
// 慢时旧实现直接丢弃新帧（`default:` 分支），于是进度条要么停在旧值、要么跳变，
// 且「丢了多少」不可观测。latest-wins 的语义是：**同一合并键只保留最新值**——
//
//	tool_call_id=call-1: percent 10 → 20 → 30  ⇒  只下发 percent 30
//	（payload.coalesced_count=3 供前端选择展示「已合并 N 帧」）
//
// 内存上界：合并键数量封顶（streamLiveQueueKeyLimit，超出后丢弃新键并计数），
// 因此一个永远不消费的连接不会让内存无界增长。真正丢弃的帧数经 keepalive 注释帧
// 回传（`: keepalive drops=… drops_total=…`），并计入 runtime_event_delivery
// 快照的 stream.dropped_live（§7 验收：丢弃必须可观测）。
type streamLiveQueue struct {
	out      chan runtimeevents.Event
	notify   chan struct{}
	stop     chan struct{}
	stopOnce sync.Once

	mu      sync.Mutex
	pending map[string]runtimeevents.Event
	order   []string
	limit   int

	mergedTotal     uint64
	droppedTotal    uint64
	reportedMerged  uint64
	reportedDropped uint64
}

// streamLiveQueueKeyLimit 是同时在途的合并键上限（≈ 并发工具数 × 进度类型）。
const streamLiveQueueKeyLimit = 256

func newStreamLiveQueue(limit int) *streamLiveQueue {
	if limit <= 0 {
		limit = streamLiveQueueKeyLimit
	}
	queue := &streamLiveQueue{
		out:     make(chan runtimeevents.Event, 64),
		notify:  make(chan struct{}, 1),
		stop:    make(chan struct{}),
		pending: make(map[string]runtimeevents.Event, limit),
		limit:   limit,
	}
	go queue.run()
	return queue
}

// publish 由总线回调调用：绝不阻塞 Publish（工具执行路径不能被慢消费者卡住），
// 同一合并键的新帧覆盖旧帧并累计 merged/coalesced_count。
func (q *streamLiveQueue) publish(event runtimeevents.Event) {
	if q == nil {
		return
	}
	key := streamLiveMergeKey(event)
	q.mu.Lock()
	if previous, exists := q.pending[key]; exists {
		q.pending[key] = streamLiveWithCoalescedCount(event, previous)
		q.mergedTotal++
		q.mu.Unlock()
		q.signal()
		return
	}
	if len(q.order) >= q.limit {
		q.droppedTotal++
		q.mu.Unlock()
		// 两处记账：B 通道按类型（runtime_event_delivery）与连接级指标
		// （runtime_event_stream_metrics.dropped_live）。
		recordRuntimeEventDeliveryLiveDrop(event.Type)
		return
	}
	q.pending[key] = streamLiveWithCoalescedCount(event, runtimeevents.Event{})
	q.order = append(q.order, key)
	q.mu.Unlock()
	q.signal()
}

// takePending 取走当前全部在途帧（保持首次到达顺序），新帧可继续写入而不必等待
// 消费者——消费者慢只会让「合并」发生得更多，不会丢帧（除非键数超限）。
func (q *streamLiveQueue) takePending() []runtimeevents.Event {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.order) == 0 {
		return nil
	}
	batch := make([]runtimeevents.Event, 0, len(q.order))
	for _, key := range q.order {
		if event, ok := q.pending[key]; ok {
			batch = append(batch, event)
		}
	}
	q.pending = make(map[string]runtimeevents.Event, q.limit)
	q.order = q.order[:0]
	return batch
}

func (q *streamLiveQueue) run() {
	for {
		select {
		case <-q.stop:
			return
		case <-q.notify:
		}
		for {
			batch := q.takePending()
			if len(batch) == 0 {
				break
			}
			for _, event := range batch {
				select {
				case q.out <- event:
				case <-q.stop:
					return
				}
			}
		}
	}
}

func (q *streamLiveQueue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// close 停止投递协程；out 不关闭（handler 退出由 ctx 驱动，关闭 chan 会引入
// 与总线回调的竞态）。
func (q *streamLiveQueue) close() {
	if q == nil {
		return
	}
	q.stopOnce.Do(func() { close(q.stop) })
}

// statsSinceLastReport 返回自上次调用以来新增的合并数/丢弃数，以及累计丢弃数。
// 供 keepalive 注释帧增量回传，避免每次 keepalive 都发一遍全量计数。
func (q *streamLiveQueue) statsSinceLastReport() (mergedDelta, droppedDelta, droppedTotal uint64) {
	if q == nil {
		return 0, 0, 0
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	mergedDelta = q.mergedTotal - q.reportedMerged
	droppedDelta = q.droppedTotal - q.reportedDropped
	q.reportedMerged = q.mergedTotal
	q.reportedDropped = q.droppedTotal
	return mergedDelta, droppedDelta, q.droppedTotal
}

// streamLiveMergeKey 是 live 帧的合并判据：优先 tool_call_id（同一工具的进度帧
// 天然是一串状态）；否则按 agent（子代理进度）；再否则退化为类型 + trace。
func streamLiveMergeKey(event runtimeevents.Event) string {
	if id := streamCoalesceFirstPayloadString(event.Payload, "tool_call_id", "toolCallId"); id != "" {
		return event.Type + "\x00call\x00" + id
	}
	if agent := strings.TrimSpace(event.AgentName); agent != "" {
		return event.Type + "\x00agent\x00" + agent
	}
	return event.Type + "\x00trace\x00" + event.TraceID
}

// streamLiveWithCoalescedCount 给合并后的帧带上 coalesced_count（浅拷贝 payload，
// 不修改总线持有的原对象）。previous 为零值时表示首次入队，计数为 1。
func streamLiveWithCoalescedCount(event runtimeevents.Event, previous runtimeevents.Event) runtimeevents.Event {
	if previous.Payload == nil {
		// 首次入队：没有发生合并，保持原帧形状（不注入合并字段）。
		return event
	}
	// 已合并过的帧带 coalesced_count；没有该字段说明上一次入队还没合并过（计 1），
	// 本次到达的行并入后计数 +1。缺了这一步，第二次入队会被记成 1 而不是 2，
	// 长链路的 coalesced_count 会永远少一行。
	count := 1
	if existing, ok := asInt64(previous.Payload[streamCoalesceCountKey]); ok && existing > 0 {
		count = int(existing)
	}
	count++
	cloned := streamCoalesceClonePayload(event.Payload, nil)
	cloned[streamCoalesceCountKey] = count
	event.Payload = cloned
	return event
}

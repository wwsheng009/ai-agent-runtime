package runtimeapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// StreamSessionRuntimeEvents streams runtime events for a session via SSE.
//
// Durable path (default): polls / watches the session event store.
// Optional live path: query live=1|true|yes (or include_live_progress=1)
// also subscribes to the in-process runtime event bus for high-frequency
// live-only events such as tool.progress. Live events are marked with
// payload["live"]=true and are never written to the durable store by this path.
func (h *Handler) StreamSessionRuntimeEvents(w http.ResponseWriter, r *http.Request) {
	sessionID := chat.NormalizeSessionID(mux.Vars(r)["id"])
	if sessionID == "" {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "session id is required"))
		return
	}

	store := h.getSessionEventStore()
	if store == nil {
		h.writeError(w, http.StatusServiceUnavailable, errors.New(errors.ErrConfigInvalid, "session event store not configured"))
		return
	}

	afterSeq := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("after")); raw != "" {
		if parsed, err := parseInt64(raw); err == nil && parsed >= 0 {
			afterSeq = parsed
		} else {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid after value"))
			return
		}
	}
	// Batch 4：记住客户端给的续传起点 —— afterSeq 会被 sendEvents 推着往前走，
	// 只有这里还能拿到原始游标，dump 结束后据此下发 `: resumed from=… to=…`。
	resumedAfterSeq := afterSeq

	pollInterval := 500 * time.Millisecond
	if raw := strings.TrimSpace(r.URL.Query().Get("poll_ms")); raw != "" {
		if parsed, err := time.ParseDuration(raw + "ms"); err == nil && parsed > 0 {
			pollInterval = parsed
		}
	}

	// Batch 3 传输增强（方案 §4/§6）：合帧与 latest-wins 都是**按连接可配**的
	// 灰度开关，默认关（缺省即旧路径，回滚面 = 不传参数）。
	coalesceDeltas := parseTruthyQueryFlag(r.URL.Query().Get("coalesce"))
	latestWins := parseTruthyQueryFlag(r.URL.Query().Get("latest_wins"))

	tailLimit := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 || parsed > streamTailMax {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid tail value"))
			return
		}
		tailLimit = parsed
	}
	if tailLimit > 0 && afterSeq > 0 {
		// 尾部窗口与增量游标是两种互斥的起始语义：同时给出无法判断「从 after
		// 续传」还是「只看最后 N 条」，宁可显式报错也不静默偏向一边。
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "tail and after are mutually exclusive"))
		return
	}

	keepaliveInterval := streamKeepaliveInterval
	if raw := strings.TrimSpace(r.URL.Query().Get("keepalive_ms")); raw != "" {
		parsed, err := time.ParseDuration(raw + "ms")
		if err != nil || parsed < streamKeepaliveMin || parsed > streamKeepaliveMax {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid keepalive_ms value"))
			return
		}
		keepaliveInterval = parsed
	}

	retryHint := time.Duration(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("retry_ms")); raw != "" {
		parsed, err := time.ParseDuration(raw + "ms")
		if err != nil || parsed < streamRetryHintMin || parsed > streamRetryHintMax {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid retry_ms value"))
			return
		}
		retryHint = parsed
	}

	// 建议 4（服务端 flush 背压）：flush_ms 是本连接的出站合并窗口。
	//
	// 默认 50ms：稳态下 provider 按 token 回调（实测 400+ 帧/s），逐帧 flush 会把
	// 一条普通回复放大成数百次 socket 写；合并窗口把出站压到 ≤20 次/s，而帧内容、
	// 顺序、游标都不变（只改「什么时候把攒好的字节推出去」）。显式传 0 退回旧行为
	// （每次 Flush 立即出站），这就是灰度/回滚面。
	flushInterval := streamFlushTickInterval
	if raw := strings.TrimSpace(r.URL.Query().Get("flush_ms")); raw != "" {
		parsed, err := time.ParseDuration(raw + "ms")
		if err != nil || parsed < streamFlushTickMin || parsed > streamFlushTickMax {
			h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "invalid flush_ms value"))
			return
		}
		flushInterval = parsed
	}

	includeLive := parseTruthyQueryFlag(r.URL.Query().Get("live")) ||
		parseTruthyQueryFlag(r.URL.Query().Get("include_live_progress"))

	h.prepareSSEHeaders(w)

	// 16KB 缓冲：SSE 帧按批写出，实测大会话首连 dump 2.28MB/2288 帧时 syscall
	// 从同量级降到 ~140 次。注意 bufio 只是「批写」优化，它和 socket 之间还隔着
	// net/http 自己的 2048B 响应缓冲（server.go:bufferBeforeChunkingSize），
	// 因此 flushSSE 必须同时完成两级 flush，否则字节根本到不了客户端 ——
	// 这正是本文件此前的回归：只调 bufio.Flush 时，`: open` 与 `: keepalive`
	// 都滞留在 net/http 内部缓冲里，客户端要等到 2KB 攒满或 handler 返回才收到
	// 任何字节，表现为 ttfB 数十秒、代理 idle timeout 掐断、前端持续重连。
	// bytes_sent 口径：在 bufio 与 net/http 之间加一层计数 writer，统计真正写进
	// 响应体的字节（Batch 3 连接级指标，见 session_runtime_stream_metrics.go）。
	// 建议 4 起「码帧」与「出站时刻」解耦：帧照旧进 bufio，flush 由
	// pacedFlushWriter 按 tick 合并（flush_ms 可配，0 = 旧行为「写即 flush」）。
	// 计数层刻意不实现 http.Flusher，所以这里把原始 w 的 Flusher 显式传进去 ——
	// 否则 pacedFlushWriter 只能推 bufio、推不动 net/http 的响应缓冲，静默退化成
	// 「什么都没发出去」（见 flushLocked 的两级 flush 注释）。
	flushTarget, _ := w.(http.Flusher)
	paced := newPacedFlushWriter(streamCountingResponseWriter{ResponseWriter: w}, flushTarget, streamWriteBufferSize, flushInterval)
	defer func() { _ = paced.Close() }()
	sseWriter := http.ResponseWriter(paced)
	flushSSE := func() error {
		paced.Flush()
		return paced.Err()
	}
	emitter := newSSEEmitter(sseWriter)

	// 立即提交响应头：Go 的 net/http 直到第一帧写出才发送响应头，而游标已追平的
	// 会话（after == 最新 seq，例如刚跑完一轮的空闲会话）在 keepalive 之前不会产生
	// 任何帧，客户端 fetch 因此长期不 resolve（onOpen 不触发、连接状态卡在
	// connecting），中间代理也只能看到「等待响应」。先写 200 + flush 让建连本身
	// 可观测，与 `: keepalive` 注释帧共同构成存活证据。
	if flusher, ok := w.(http.Flusher); ok {
		w.WriteHeader(http.StatusOK)
		flusher.Flush()
	}
	// 首字节握手帧：仅 flush 响应头还不够 —— 中间代理（Vite dev proxy / nginx 等）
	// 在拿到第一个响应体字节前不会把响应头转发给客户端（Node http.ServerResponse
	// 同样在首次 write 才发送头部），于是浏览器仍要空等到第一个 keepalive。
	// 实测：经 5193 代理 ttfB=15.2s（正好等于 keepalive 周期），直连 8101 为 2ms。
	// 建连即写一帧注释，让「头 + 首字节」在毫秒级到达客户端。
	writeSSEComment(sseWriter, "open")
	if err := flushSSE(); err != nil {
		return
	}
	// SSE 规范的重连退避提示帧（毫秒）。默认不发（Batch 3 灰度约定），仅当连接
	// 显式带 retry_ms 时下发；既有解析器对 `retry:` 行静默忽略 —— 兼容性由
	// frontend/src/api/runtime/sse.test.ts 的「忽略 id:/retry: 行」用例锁定。
	if retryHint > 0 {
		writeSSERetry(sseWriter, retryHint)
		// retry_count 口径 = 本连接下发的重连建议次数（含存储层退避的注释帧留痕，
		// 见 queryPage 失败分支）；`retry:` 提示帧同样计入，否则指标只反映故障、
		// 不反映握手期的正常提示。
		recordRuntimeEventStreamRetry()
		// 握手期的提示帧必须立刻可见（不能被合并窗口推迟），显式强制出站。
		if err := paced.FlushNow(); err != nil {
			return
		}
	}
	runtimeEventStreamConnectionOpened()
	defer runtimeEventStreamConnectionClosed()

	ctx := r.Context()
	var eventWake <-chan runtimeevents.Event
	unwatch := func() {}
	if watcher, ok := store.(chat.EventWatcherStore); ok && watcher != nil {
		eventWake, unwatch = watcher.WatchEvents(ctx, sessionID)
	}
	defer unwatch()

	fallbackInterval := pollInterval
	if eventWake != nil {
		fallbackInterval = 5 * time.Second
	}
	ticker := time.NewTicker(fallbackInterval)
	defer ticker.Stop()

	// keepalive 与是否有事件无关：空闲期每 15s 写一行 SSE 注释帧，既向客户端/代理
	// 证明连接存活（防止 LB / proxy idle timeout 掐断静默长连接），也让前端不必靠
	// 「有没有事件」来判断在线。间隔与 aicli web 事件流
	//（chatWebEventKeepaliveInterval）同口径，不跟随 poll_ms —— poll_ms 只决定
	// 兜底拉取频率，不该决定注释帧频率。
	keepalive := time.NewTicker(keepaliveInterval)
	defer keepalive.Stop()

	var liveCh <-chan runtimeevents.Event
	var liveQueue *streamLiveQueue
	var unsubLive func()
	if includeLive {
		liveCh, liveQueue, unsubLive = h.subscribeSessionLiveRuntimeEvents(sessionID, latestWins)
		if unsubLive != nil {
			defer unsubLive()
		}
	}

	sendEvents := func(events []runtimeevents.Event) {
		// Batch 3：按页合帧（页边界不跨页折叠——折叠窗口与单页查询结果同界，
		// 内存与编码成本保持有界）。合并帧自带 coalesced_from/coalesced_count，
		// payload["seq"] 保持区间末行，客户端游标不变量成立。
		for _, event := range coalesceRuntimeEventPage(events, coalesceDeltas) {
			recordRuntimeEventStreamFrame()
			// Batch 4：store 侧帧（初始 dump / 断点补齐）一律标记 replay:true，
			// 实时帧走 buildSessionRuntimeLiveEventView（live:true），两者互斥。
			emitter.Emit("runtime_event", buildSessionRuntimeReplayEventView(event))
			if seq, ok := runtimeEventSeq(event); ok && seq > afterSeq {
				afterSeq = seq
			}
		}
	}

	// drain / tail 共用分页读：streamEventPageSize 取代了原来的「一次不限量
	// 查询」——服务端内存与单次查询结果有界，每页结束 flush 一次让客户端渐进
	// 渲染，flush 出错（客户端提前断开）立即返回，及时释放 watch/ticker/连接。
	//
	// 存储层的瞬时错误不再终止流。此前任何一次 ListEvents 报错都会写一个 error
	// 帧后 return：客户端立刻重连，重连本身又加剧 SQLite 争用（写事务持有唯一
	// 连接，见 chat.SessionRuntimeStore），形成「超时 → 断开 → 重连 → 更慢」的
	// 正反馈。现在改为注释帧留痕 + 指数退避 + 重新解析 store（热重载会关闭旧
	// store 实例，旧指针只会持续报错），只要连接还在就继续重试。
	//
	// 单次查询限时：SQLite 侧只有一条连接（SetMaxOpenConns(1)），写事务持锁
	// 期间读会排队；driver 的 busy_timeout 不响应 Go ctx 取消，但 database/sql
	// 在等待空闲连接时是可取消的。限时保证即使存储层长时间无响应，handler 也能
	// 回到循环、继续给客户端发存活字节。
	consecutiveFailures := 0
	retryBackoff := streamRetryInitialBackoff
	// queryPage 拉取单页；ok=false 表示连接已不可用（ctx 结束或注释帧 flush 失败）。
	queryPage := func(cursor int64) ([]runtimeevents.Event, bool) {
		for {
			queryCtx, cancelQuery := context.WithTimeout(ctx, streamQueryTimeout)
			started := time.Now()
			events, err := store.ListEvents(queryCtx, sessionID, cursor, streamEventPageSize)
			cancelQuery()
			if err != nil {
				if ctx.Err() != nil {
					return nil, false
				}
				consecutiveFailures++
				recordRuntimeEventStreamRetry()
				// 用注释帧（`:` 前缀）而非新增 event 类型上报降级：所有 SSE 客户端
				// 都按规范忽略注释帧，不会把可恢复的重试误判为致命错误；同时在
				// DevTools 的 EventStream 面板与代理日志里留下可观测痕迹。
				writeSSEComment(sseWriter, fmt.Sprintf(
					"stream-retry attempt=%d error=%s",
					consecutiveFailures, sanitizeSSEComment(err.Error()),
				))
				if flushErr := flushSSE(); flushErr != nil {
					return nil, false
				}
				if fresh := h.getSessionEventStore(); fresh != nil {
					store = fresh
				}
				select {
				case <-ctx.Done():
					return nil, false
				case <-time.After(retryBackoff):
				}
				if retryBackoff < streamRetryMaxBackoff {
					retryBackoff *= 2
					if retryBackoff > streamRetryMaxBackoff {
						retryBackoff = streamRetryMaxBackoff
					}
				}
				continue
			}
			consecutiveFailures = 0
			retryBackoff = streamRetryInitialBackoff
			recordRuntimeEventStreamDump(time.Since(started), 1)
			return events, true
		}
	}

	// drainEvents 按页拉取并写出，直到没有新事件；返回 false 表示连接已不可用。
	drainEvents := func() bool {
		for {
			events, ok := queryPage(afterSeq)
			if !ok {
				return false
			}
			if len(events) == 0 {
				return true
			}
			sendEvents(events)
			if err := flushSSE(); err != nil {
				return false
			}
			if len(events) < streamEventPageSize {
				return true
			}
		}
	}

	// Batch 3：tail=N —— 首帧只回放最后 N 条（与 after 互斥，见入口校验）。
	// 复用分页读 + 环形缓冲保留尾窗：内存 O(N)、下发字节 O(N)，读放大仍是
	// 一次全量分页（存储层暂无逆序读接口，命中「只增不改」约定，见方案 §8-4）。
	dumpTailEvents := func(limit int) bool {
		ring := make([]runtimeevents.Event, 0, limit)
		cursor := int64(0)
		for {
			events, ok := queryPage(cursor)
			if !ok {
				return false
			}
			if len(events) == 0 {
				break
			}
			for _, event := range events {
				if seq, hasSeq := runtimeEventSeq(event); hasSeq && seq > cursor {
					cursor = seq
				}
				ring = append(ring, event)
			}
			if len(ring) > limit {
				ring = append(ring[:0], ring[len(ring)-limit:]...)
			}
			if len(events) < streamEventPageSize {
				break
			}
		}
		sendEvents(ring)
		return flushSSE() == nil
	}

	// Initial dump：tail 模式只发尾窗，否则按页全量补齐到最新 seq。
	if tailLimit > 0 {
		if !dumpTailEvents(tailLimit) {
			return
		}
	} else if !drainEvents() {
		return
	}

	// Batch 4 · 断点续传起始帧：只有「带游标续传且确实补到了新行」才下发。
	// from 取客户端游标的下一行（含跨页/跨窗口的整段跨度），to 是补齐后的最高 seq，
	// 因此客户端拿到它就等于拿到「已追平到哪里」，无需再靠 isResponding 猜。
	if resumedAfterSeq > 0 && afterSeq > resumedAfterSeq {
		writeSSEComment(sseWriter, fmt.Sprintf("resumed from=%d to=%d", resumedAfterSeq+1, afterSeq))
		if err := flushSSE(); err != nil {
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case liveEvent, ok := <-liveCh:
			if !ok {
				liveCh = nil
				continue
			}
			recordRuntimeEventStreamFrame()
			emitter.Emit("runtime_event", buildSessionRuntimeLiveEventView(liveEvent))
			if err := flushSSE(); err != nil {
				return
			}
		case <-eventWake:
			if !drainEvents() {
				return
			}
		case <-ticker.C:
			if !drainEvents() {
				return
			}
		case <-keepalive.C:
			// Batch 3：latest-wins 的合并/丢弃计数经注释帧增量回传——丢弃不再
			// 静默（`drops`），且每次只报增量，空闲期不产生额外噪声。
			comment := "keepalive"
			if mergedDelta, droppedDelta, droppedTotal := liveQueue.statsSinceLastReport(); mergedDelta > 0 || droppedDelta > 0 {
				comment = fmt.Sprintf("keepalive merged=%d drops=%d drops_total=%d",
					mergedDelta, droppedDelta, droppedTotal)
			}
			writeSSEComment(sseWriter, comment)
			if err := flushSSE(); err != nil {
				return
			}
		}
	}
}

// streamKeepaliveInterval 是静默长连接上发送 SSE 注释帧（`: keepalive`）的周期。
const streamKeepaliveInterval = 15 * time.Second

// Batch 3 连接参数上界（查询参数属于外部输入，必须有界；也是灰度开关的护栏）。
const (
	// keepalive_ms：允许 100ms~5min（默认 15s 不变，测试用短周期验证注释帧）。
	streamKeepaliveMin = 100 * time.Millisecond
	streamKeepaliveMax = 5 * time.Minute
	// retry_ms：SSE 规范 `retry:` 建议值，允许 100ms~60s。
	streamRetryHintMin = 100 * time.Millisecond
	streamRetryHintMax = 60 * time.Second
	// tail=N：首帧只回放最后 N 条（上界防止一次请求拉走整个会话日志）。
	streamTailMax = 5000
)

// streamWriteBufferSize 是 SSE 响应体的缓冲区大小：攒满即写出，兼顾首字节
// 延迟（dump 立刻开始写，无需等整批）与 syscall 次数。
const streamWriteBufferSize = 16 * 1024

// 建议 4 的出站合并窗口（见 session_runtime_stream_flush.go 的 pacedFlushWriter）。
const (
	// streamFlushTickInterval 是默认合并窗口：稳态 400+ 帧/s 下把 flush 压到
	// ≤20 次/s，而单帧迟滞不超过一个窗口 —— token 流场景不可感知。
	streamFlushTickInterval = 50 * time.Millisecond
	// flush_ms 的取值下界：0 表示关闭合并（旧行为「写即 flush」，显式回滚面）。
	streamFlushTickMin = time.Duration(0)
	// 上界 1s：再长就会让人误判「流卡住」，也失去渐进渲染的意义。
	streamFlushTickMax = time.Second
)

// streamQueryTimeout 是单次 ListEvents 的最长等待。取 10s 而非更短：正常分页查询
// 在毫秒级，只有唯一连接被写事务占住时才会触顶；而它必须明显小于代理的 idle
// timeout，确保存储层卡住时 handler 仍能定期回到循环发出存活字节。
const streamQueryTimeout = 10 * time.Second

// SSE 读路径的重试退避区间：瞬时错误（SQLite busy、连接排队超时、store 热重载）
// 逐次翻倍退避，上限 5s，避免「报错即重试」把唯一连接压得更死。
const (
	streamRetryInitialBackoff = 200 * time.Millisecond
	streamRetryMaxBackoff     = 5 * time.Second
)

// streamEventPageSize 是 dump/兜底拉取的单页事件数上限：取代 limit=0 的不限量
// 查询，保证服务端内存、单次查询耗时与客户端首包大小都有界。
const streamEventPageSize = 500

// runtimeEventSeq 读取事件载荷里的 EventStore 持久化序号（ListEvents 会写入），
// 用于推进游标；live-only 事件没有 seq，返回 false。
func runtimeEventSeq(event runtimeevents.Event) (int64, bool) {
	if event.Payload == nil {
		return 0, false
	}
	raw, ok := event.Payload["seq"]
	if !ok {
		return 0, false
	}
	return asInt64(raw)
}

// subscribeSessionLiveRuntimeEvents fans out bus events for one session onto a
// buffered channel. Non-blocking send drops when the consumer is slow so Publish
// never stalls tool execution. Only live-only types are forwarded (tool.progress
// and the parent-side subagent.progress mirror); durable types continue via the
// store path.
//
// Batch 3：latestWins=true 时改用 streamLiveQueue —— 同一合并键（tool_call_id /
// agent）只保留最新值，慢消费者触发的是**合并**而不是丢弃；真正丢弃（键数超限）
// 计入 live_dropped 并经 keepalive 注释帧回传。默认仍走原来的「满了就丢」路径。
func (h *Handler) subscribeSessionLiveRuntimeEvents(sessionID string, latestWins bool) (<-chan runtimeevents.Event, *streamLiveQueue, func()) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil, nil, func() {}
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil, nil, func() {}
	}

	var ch chan runtimeevents.Event
	var queue *streamLiveQueue
	if latestWins {
		queue = newStreamLiveQueue(streamLiveQueueKeyLimit)
	} else {
		ch = make(chan runtimeevents.Event, 64)
	}
	unsubscribes := make([]func(), 0, len(sessionLiveOnlyRuntimeEventTypes))
	for _, eventType := range sessionLiveOnlyRuntimeEventTypes {
		unsub := bus.SubscribeCancelable(eventType, func(event runtimeevents.Event) {
			if strings.TrimSpace(event.SessionID) != sessionID {
				return
			}
			if !isSessionLiveOnlyRuntimeEvent(event) {
				return
			}
			if queue != nil {
				// 入队即合并（同键 latest-wins）。转发量按「到达 B 通道」计，
				// 合并后的实际帧数看 coalesced_count / 指标 coalesced_rows。
				queue.publish(event)
				recordRuntimeEventDeliveryLiveForwarded(event.Type)
				return
			}
			select {
			case ch <- event:
				// P0-2：B 通道（live-only 旁路）的转发量按类型计数。这条通道不落盘，
				// 一旦某类事件的实时帧消失，能区分「没发生」与「没送达」的只有这里
				// （读侧：runtimeStatusSnapshot 的 runtime_event_delivery 键）。
				recordRuntimeEventDeliveryLiveForwarded(event.Type)
			default:
				// Drop when the SSE consumer lags; progress is best-effort.
				// Batch 3：丢弃不再静默——按类型计入 live_dropped，并经 keepalive
				// 注释帧与 stream.dropped_live 指标回传（latest_wins=1 可避免丢弃）。
				recordRuntimeEventDeliveryLiveDrop(event.Type)
			}
		})
		if unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}

	cleanup := func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
		queue.close()
		// Do not close ch: handlers may still race after unsubscribe until Publish
		// returns; GC reclaims the channel when the handler exits.
	}
	if queue != nil {
		return queue.out, queue, cleanup
	}
	return ch, nil, cleanup
}

// sessionLiveOnlyRuntimeEventTypes are the bus types that are delivered straight
// to live (live=1) SSE subscribers and are never persisted by that path.
//
// 清单来自 internal/events 注册表的 B 通道（Batch 2 事件契约单一真源）；
// 此处不再手写类型名，新增 live-only 类型改 contract.go。
var sessionLiveOnlyRuntimeEventTypes = runtimeevents.LiveOnlyEventTypes()

func isSessionLiveOnlyRuntimeEvent(event runtimeevents.Event) bool {
	return runtimeevents.IsLiveOnlyEventType(event.Type)
}

func buildSessionRuntimeLiveEventView(event runtimeevents.Event) map[string]interface{} {
	view := buildSessionRuntimeEventView(event)
	// Mark live so clients can distinguish bus-forwarded progress from durable rows.
	view["live"] = true
	if payload, ok := view["payload"].(map[string]interface{}); ok && payload != nil {
		// Shallow clone so we do not mutate the bus-retained event payload.
		cloned := make(map[string]interface{}, len(payload)+1)
		for key, value := range payload {
			cloned[key] = value
		}
		cloned["live"] = true
		view["payload"] = cloned
	} else {
		view["payload"] = map[string]interface{}{"live": true}
	}
	return view
}

func parseTruthyQueryFlag(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// sanitizeSSEComment 让任意错误文本可以安全地放进一行注释帧。CR/LF 会提前结束注释
// 行、把余下内容泄漏成非法事件行并破坏帧边界，因此统一压成空格；同时按 rune 限长，
// 避免截断出半个多字节字符。
func sanitizeSSEComment(raw string) string {
	cleaned := strings.NewReplacer("\r", " ", "\n", " ").Replace(strings.TrimSpace(raw))
	if cleaned == "" {
		return "unknown"
	}
	const maxRunes = 200
	if runes := []rune(cleaned); len(runes) > maxRunes {
		cleaned = string(runes[:maxRunes]) + "..."
	}
	return cleaned
}

func parseInt64(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

// writeSSERetry 写一行 SSE 规范帧 `retry: <ms>`：告诉客户端断线后建议的重连
// 退避时间。批次 3 默认不下发（见 StreamSessionRuntimeEvents 的 retry_ms 参数），
// 因为它改变的是「客户端重连节奏」，属于需要灰度观察的行为。调用方负责 flush。
func writeSSERetry(w io.Writer, delay time.Duration) {
	if delay <= 0 {
		return
	}
	_, _ = fmt.Fprintf(w, "retry: %d\n\n", delay.Milliseconds())
}

func asInt64(raw interface{}) (int64, bool) {
	switch v := raw.(type) {
	case int64:
		return v, true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case json.Number:
		if parsed, err := v.Int64(); err == nil {
			return parsed, true
		}
	case string:
		parsed, err := parseInt64(v)
		return parsed, err == nil
	}
	return 0, false
}

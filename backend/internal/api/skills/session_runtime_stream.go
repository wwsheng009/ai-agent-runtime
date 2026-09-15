package skills

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/chat"
	errors "github.com/wwsheng009/ai-agent-runtime/internal/errors"
	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
	"github.com/wwsheng009/ai-agent-runtime/internal/supervision"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolprotocol"
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

	pollInterval := 500 * time.Millisecond
	if raw := strings.TrimSpace(r.URL.Query().Get("poll_ms")); raw != "" {
		if parsed, err := time.ParseDuration(raw + "ms"); err == nil && parsed > 0 {
			pollInterval = parsed
		}
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
	buffered := bufio.NewWriterSize(w, streamWriteBufferSize)
	sseWriter := bufferedSSEWriter{ResponseWriter: w, buf: buffered}
	// flushSSE 必须做两级 flush，缺一不可：
	//   1) buffered.Flush() 把 SSE 帧从 16KB bufio 缓冲写进 http.ResponseWriter；
	//   2) http.Flusher.Flush() 把 net/http 的 2048B 响应缓冲推到 socket。
	// 只做 (1) 时数据仍停在 response.w 里，对客户端等价于「什么都没发出去」。
	flushSSE := func() error {
		if err := buffered.Flush(); err != nil {
			return err
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
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
	keepalive := time.NewTicker(streamKeepaliveInterval)
	defer keepalive.Stop()

	var liveCh <-chan runtimeevents.Event
	var unsubLive func()
	if includeLive {
		liveCh, unsubLive = h.subscribeSessionLiveRuntimeEvents(sessionID)
		if unsubLive != nil {
			defer unsubLive()
		}
	}

	sendEvents := func(events []runtimeevents.Event) {
		for _, event := range events {
			emitter.Emit("runtime_event", buildSessionRuntimeEventView(event))
			if seq, ok := runtimeEventSeq(event); ok && seq > afterSeq {
				afterSeq = seq
			}
		}
	}

	// drainEvents 按页拉取并写出，直到没有新事件。
	// 分页（streamEventPageSize）取代了原来的「一次不限量查询」：
	//   - 服务端内存与单次查询结果有界（不再把整份事件日志一次性读进内存）；
	//   - 每页结束 flush 一次 ⇒ 客户端渐进渲染，首屏不再等整份 dump 编码完；
	//   - flush 出错（客户端提前断开）立即返回，及时释放 watch/ticker/连接。
	// 返回 false 表示连接已不可用（客户端断开或 flush 失败），调用方应结束 handler。
	//
	// 存储层的瞬时错误不再终止流。此前任何一次 ListEvents 报错都会写一个 error
	// 帧后 return：客户端立刻重连，重连本身又加剧 SQLite 争用（写事务持有唯一
	// 连接，见 chat.SessionRuntimeStore），形成「超时 → 断开 → 重连 → 更慢」的
	// 正反馈。现在改为注释帧留痕 + 指数退避 + 重新解析 store（热重载会关闭旧
	// store 实例，旧指针只会持续报错），只要连接还在就继续重试。
	consecutiveFailures := 0
	retryBackoff := streamRetryInitialBackoff
	drainEvents := func() bool {
		for {
			// 单次查询限时：SQLite 侧只有一条连接（SetMaxOpenConns(1)），写事务
			// 持锁期间读会排队；driver 的 busy_timeout 不响应 Go ctx 取消，
			// 但 database/sql 在等待空闲连接时是可取消的。限时保证即使存储层
			// 长时间无响应，handler 也能回到循环、继续给客户端发存活字节。
			queryCtx, cancelQuery := context.WithTimeout(ctx, streamQueryTimeout)
			events, err := store.ListEvents(queryCtx, sessionID, afterSeq, streamEventPageSize)
			cancelQuery()
			if err != nil {
				if ctx.Err() != nil {
					return false
				}
				consecutiveFailures++
				// 用注释帧（`:` 前缀）而非新增 event 类型上报降级：所有 SSE 客户端
				// 都按规范忽略注释帧，不会把可恢复的重试误判为致命错误；同时在
				// DevTools 的 EventStream 面板与代理日志里留下可观测痕迹。
				writeSSEComment(sseWriter, fmt.Sprintf(
					"stream-retry attempt=%d error=%s",
					consecutiveFailures, sanitizeSSEComment(err.Error()),
				))
				if flushErr := flushSSE(); flushErr != nil {
					return false
				}
				// 热重载（refreshSessionRuntimeStore → closeRuntimeStore）会关闭
				// 本 handler 捕获的 store 指针；重新解析一次即可自愈，避免对着
				// 一个已经关闭的 store 无休止重试。
				if fresh := h.getSessionEventStore(); fresh != nil {
					store = fresh
				}
				select {
				case <-ctx.Done():
					return false
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

	// Initial dump（按页全量补齐到最新 seq）。
	if !drainEvents() {
		return
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
			writeSSEComment(sseWriter, "keepalive")
			if err := flushSSE(); err != nil {
				return
			}
		}
	}
}

// streamKeepaliveInterval 是静默长连接上发送 SSE 注释帧（`: keepalive`）的周期。
const streamKeepaliveInterval = 15 * time.Second

// streamWriteBufferSize 是 SSE 响应体的缓冲区大小：攒满即写出，兼顾首字节
// 延迟（dump 立刻开始写，无需等整批）与 syscall 次数。
const streamWriteBufferSize = 16 * 1024

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

// bufferedSSEWriter 把 SSE 帧写进缓冲区，并刻意「不」暴露 http.Flusher：
// writeSSEEventWithEnvelope / writeSSEComment 只有在 w 实现 http.Flusher 时才会
// 逐帧 flush，这里让它落到缓冲，由调用方按批 flush。
// 注意：嵌入 http.ResponseWriter 只提升接口自身的方法（Header/Write/WriteHeader），
// Flush 不会被提升，因此 *不会* 意外满足 http.Flusher。
type bufferedSSEWriter struct {
	http.ResponseWriter
	buf *bufio.Writer
}

func (b bufferedSSEWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }

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
func (h *Handler) subscribeSessionLiveRuntimeEvents(sessionID string) (<-chan runtimeevents.Event, func()) {
	sessionID = strings.TrimSpace(sessionID)
	if h == nil || sessionID == "" {
		return nil, func() {}
	}
	bus := h.getRuntimeEventBus()
	if bus == nil {
		return nil, func() {}
	}

	ch := make(chan runtimeevents.Event, 64)
	unsubscribes := make([]func(), 0, len(sessionLiveOnlyRuntimeEventTypes))
	for _, eventType := range sessionLiveOnlyRuntimeEventTypes {
		unsub := bus.SubscribeCancelable(eventType, func(event runtimeevents.Event) {
			if strings.TrimSpace(event.SessionID) != sessionID {
				return
			}
			if !isSessionLiveOnlyRuntimeEvent(event) {
				return
			}
			select {
			case ch <- event:
			default:
				// Drop when the SSE consumer lags; progress is best-effort.
			}
		})
		if unsub != nil {
			unsubscribes = append(unsubscribes, unsub)
		}
	}

	return ch, func() {
		for _, unsub := range unsubscribes {
			unsub()
		}
		// Do not close ch: handlers may still race after unsubscribe until Publish
		// returns; GC reclaims the channel when the handler exits.
	}
}

// sessionLiveOnlyRuntimeEventTypes are the bus types that are delivered straight
// to live (live=1) SSE subscribers and are never persisted by that path.
var sessionLiveOnlyRuntimeEventTypes = []string{
	toolprotocol.EventTypeProgress,
	supervision.EventTypeSubagentProgress,
}

func isSessionLiveOnlyRuntimeEvent(event runtimeevents.Event) bool {
	switch strings.TrimSpace(event.Type) {
	case toolprotocol.EventTypeProgress, supervision.EventTypeSubagentProgress:
		return true
	default:
		return false
	}
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
